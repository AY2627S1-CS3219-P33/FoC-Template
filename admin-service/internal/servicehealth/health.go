// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package servicehealth reads each service's /readyz over the internal
// network, for the dashboard's readiness section.
//
// Every service checked here exposes /readyz: order-service, supplier-service,
// credit-service, user-service and admin-service itself. A service that has no
// URL configured is reported as not checked, never given an invented check.
//
// A service is "up" when it answers 200: ready, or degraded (order-service
// reports, for example, a down RabbitMQ that way). It is "down" when it does
// not answer, answers 503 not_ready, or answers anything else. The dependency
// detail a service reports is passed on as it is, after checking that it
// looks like the fixed vocabulary /readyz uses (short lower-case words), so
// nothing free-form from another service reaches the dashboard.
package servicehealth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// State is whether a service answered ready.
type State string

const (
	Up   State = "up"
	Down State = "down"
)

// Reason says why a service is down.
type Reason string

const (
	// ReasonUnreachable: the connection failed.
	ReasonUnreachable Reason = "unreachable"
	// ReasonTimeout: no answer within the timeout.
	ReasonTimeout Reason = "timeout"
	// ReasonNotReady: the service answered 503 and says it is not ready.
	ReasonNotReady Reason = "not_ready"
	// ReasonUnexpected: the service answered with another status.
	ReasonUnexpected Reason = "unexpected_status"
)

const (
	// maxBodyBytes bounds a /readyz body; they are a few hundred bytes.
	maxBodyBytes = 16 << 10
	// maxDependencies bounds how many dependencies one service may report.
	maxDependencies = 16
)

// word is the vocabulary of /readyz statuses and dependency names: short,
// lower-case or camelCase words.
var word = regexp.MustCompile(`^[A-Za-z][A-Za-z_]{0,31}$`)

// Target is one service to check. URL has no trailing slash and no path.
type Target struct {
	Name string
	URL  string
}

// NotChecked is a service the dashboard does not check, and why.
type NotChecked struct {
	Name   string
	Reason string
}

// Status is one service's readiness.
type Status struct {
	Name  string
	State State
	// Status is the service's own word, "ready" or "degraded", or empty when
	// it did not say.
	Status string
	// Dependencies is what the service reports about its own dependencies;
	// nil when it reports none (supplier-service and credit-service say only
	// ready or not ready).
	Dependencies map[string]string
	// Reason is set when State is Down.
	Reason Reason
	// LatencyMillis is how long the answer took; zero when there was none.
	LatencyMillis int64
}

// Report is the readiness of every service checked.
type Report struct {
	Services   []Status
	NotChecked []NotChecked
}

// Checker reads /readyz from its targets. It is safe for concurrent use.
type Checker struct {
	targets    []Target
	notChecked []NotChecked
	client     *http.Client
}

// New checks targets, each answer within timeout, and reports notChecked as
// it is. Target names must be unique.
func New(targets []Target, notChecked []NotChecked, timeout time.Duration) (*Checker, error) {
	if timeout <= 0 {
		return nil, errors.New("servicehealth: timeout must be positive")
	}
	seen := map[string]bool{}
	for _, target := range targets {
		parsed, err := url.Parse(target.URL)
		if target.Name == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || seen[target.Name] {
			return nil, errors.New("servicehealth: each target needs a unique name and an http(s) URL")
		}
		seen[target.Name] = true
	}
	return &Checker{
		targets:    slices.Clone(targets),
		notChecked: slices.Clone(notChecked),
		client: &http.Client{
			Timeout: timeout,
			// /readyz never redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Check reads every target at once and returns them in the order given.
func (c *Checker) Check(ctx context.Context) Report {
	report := Report{Services: make([]Status, len(c.targets)), NotChecked: slices.Clone(c.notChecked)}
	var wg sync.WaitGroup
	for i, target := range c.targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report.Services[i] = c.check(ctx, target)
		}()
	}
	wg.Wait()
	return report
}

func (c *Checker) check(ctx context.Context, target Target) Status {
	status := Status{Name: target.Name, State: Down}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(target.URL, "/")+"/readyz", nil)
	if err != nil {
		status.Reason = ReasonUnreachable
		return status
	}
	request.Header.Set("Accept", "application/json")
	started := time.Now()
	response, err := c.client.Do(request)
	if err != nil {
		status.Reason = ReasonUnreachable
		var timeout interface{ Timeout() bool }
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			status.Reason = ReasonTimeout
		}
		return status
	}
	defer func() { _ = response.Body.Close() }()
	status.LatencyMillis = time.Since(started).Milliseconds()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	reported, dependencies := parse(body)

	switch response.StatusCode {
	case http.StatusOK:
		status.State, status.Status, status.Dependencies = Up, reported, dependencies
		if status.Status == "" {
			// A 200 from /readyz means ready, whatever the body's detail.
			status.Status = "ready"
		}
	case http.StatusServiceUnavailable:
		status.Reason = ReasonNotReady
	default:
		status.Reason = ReasonUnexpected
	}
	return status
}

// parse reads {"status": "...", "dependencies": {"name": "status"}}. Anything
// that does not look like /readyz's vocabulary is dropped, not passed on.
func parse(body []byte) (string, map[string]string) {
	var decoded struct {
		Status       string          `json:"status"`
		Dependencies json.RawMessage `json:"dependencies"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return "", nil
	}
	reported := ""
	if word.MatchString(decoded.Status) {
		reported = decoded.Status
	}
	var raw map[string]any
	if len(decoded.Dependencies) == 0 || json.Unmarshal(decoded.Dependencies, &raw) != nil || len(raw) == 0 || len(raw) > maxDependencies {
		return reported, nil
	}
	dependencies := make(map[string]string, len(raw))
	for name, value := range raw {
		text, ok := value.(string)
		if !ok || !word.MatchString(name) || !word.MatchString(text) {
			continue
		}
		dependencies[name] = text
	}
	if len(dependencies) == 0 {
		return reported, nil
	}
	return reported, dependencies
}
