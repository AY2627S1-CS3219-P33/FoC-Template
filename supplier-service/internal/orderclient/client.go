// Package orderclient is supplier-service's client for order-service's
// supplier deletion fence (B4). It implements deletefeature.OrderDeletionFence
// over the internal routes in order-service/docs/contracts.md ("Wire format"),
// authenticating with supplier-service's own Auth0 M2M token.
//
// Every fence call is idempotent, so a timeout, network error, 429 or 5xx is
// retried up to MaxAttempts. Any failure is returned as an error and never as
// a fence: the caller must not infer that deletion is safe from an error.
package orderclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
	deletefeature "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/delete"
)

const (
	maxResponseBytes = 64 << 10
	retryDelay       = 100 * time.Millisecond
)

var (
	// ErrUnavailable means order-service could not be reached or failed, so
	// the call's effect is unknown.
	ErrUnavailable = errors.New("orderclient: order-service is unavailable")
	// ErrFenceNotFound is 404: no fence exists for the operation ID.
	ErrFenceNotFound = errors.New("orderclient: fence not found")
	// ErrInvalidTransition is 409: the fence is in the other terminal state.
	ErrInvalidTransition = errors.New("orderclient: fence is in a conflicting state")
	// ErrRejected is any other 4xx, for example an operation ID bound to
	// another supplier or a token without orders:internal.
	ErrRejected = errors.New("orderclient: order-service rejected the request")
	// ErrInvalidResponse means a 200 response did not describe the
	// requested fence.
	ErrInvalidResponse = errors.New("orderclient: order-service returned an invalid fence")
)

// Config locates order-service. The HTTP client passed to New supplies
// authentication.
type Config struct {
	// BaseURL is the order-service origin, optionally with a path prefix,
	// without a trailing slash.
	BaseURL string
	// Timeout bounds each attempt, including obtaining a token.
	Timeout time.Duration
	// MaxAttempts is the number of tries for a retryable failure.
	MaxAttempts int
}

// Client calls order-service's fence routes. It is safe for concurrent use.
type Client struct {
	baseURL     string
	http        *http.Client
	maxAttempts int
	logger      *slog.Logger
}

var _ deletefeature.OrderDeletionFence = (*Client)(nil)

// New builds a client without contacting order-service. transport must add
// the bearer token (serviceauth.NewTransport); redirects are never followed
// so the token is only sent to BaseURL.
func New(cfg Config, transport http.RoundTripper, logger *slog.Logger) (*Client, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" ||
		strings.HasSuffix(cfg.BaseURL, "/") || base.RawQuery != "" || base.Fragment != "" || base.User != nil {
		return nil, errors.New("orderclient: base URL must be an http(s) URL without a trailing slash")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("orderclient: timeout must be positive")
	}
	if cfg.MaxAttempts < 1 {
		return nil, errors.New("orderclient: max attempts must be at least 1")
	}
	if transport == nil {
		return nil, errors.New("orderclient: transport is required")
	}
	if logger == nil {
		return nil, errors.New("orderclient: logger is required")
	}
	return &Client{
		baseURL: cfg.BaseURL,
		http: &http.Client{
			Transport:     transport,
			Timeout:       cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxAttempts: cfg.MaxAttempts,
		logger:      logger,
	}, nil
}

type fenceResponse struct {
	OperationID      string `json:"operationId"`
	SupplierID       string `json:"supplierId"`
	State            string `json:"state"`
	HasActiveErrands bool   `json:"hasActiveErrands"`
}

func (c *Client) Acquire(ctx context.Context, operationID supplier.DeletionOperationID, supplierID supplier.SupplierID) (deletefeature.Fence, error) {
	body, err := json.Marshal(struct {
		SupplierID string `json:"supplierId"`
	}{SupplierID: string(supplierID)})
	if err != nil {
		return deletefeature.Fence{}, err
	}
	fence, err := c.call(ctx, "acquire", http.MethodPost, operationID, "/acquire", body)
	if err != nil {
		return deletefeature.Fence{}, err
	}
	if !strings.EqualFold(string(fence.SupplierID), string(supplierID)) {
		return deletefeature.Fence{}, fmt.Errorf("%w: acquire returned another supplier's fence", ErrInvalidResponse)
	}
	return fence, nil
}

func (c *Client) Inspect(ctx context.Context, operationID supplier.DeletionOperationID) (deletefeature.Fence, error) {
	return c.call(ctx, "inspect", http.MethodGet, operationID, "", nil)
}

func (c *Client) Commit(ctx context.Context, operationID supplier.DeletionOperationID) error {
	return c.transition(ctx, "commit", operationID, deletefeature.FenceCommitted)
}

func (c *Client) Release(ctx context.Context, operationID supplier.DeletionOperationID) error {
	return c.transition(ctx, "release", operationID, deletefeature.FenceReleased)
}

// transition succeeds only when order-service confirms the fence is in want.
func (c *Client) transition(ctx context.Context, operation string, operationID supplier.DeletionOperationID, want deletefeature.FenceState) error {
	fence, err := c.call(ctx, operation, http.MethodPost, operationID, "/"+operation, nil)
	if err != nil {
		return err
	}
	if fence.State != want {
		return fmt.Errorf("%w: %s returned state %q", ErrInvalidResponse, operation, fence.State)
	}
	return nil
}

func (c *Client) call(ctx context.Context, operation, method string, operationID supplier.DeletionOperationID, suffix string, body []byte) (deletefeature.Fence, error) {
	if !supplier.ValidDeletionOperationID(operationID) {
		return deletefeature.Fence{}, fmt.Errorf("%w: operation ID must be a UUID", ErrRejected)
	}
	endpoint := c.baseURL + "/internal/supplier-fences/" + url.PathEscape(string(operationID)) + suffix

	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return deletefeature.Fence{}, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
			case <-time.After(retryDelay):
			}
		}
		fence, retryable, err := c.attempt(ctx, method, endpoint, body)
		if err == nil {
			if !strings.EqualFold(string(fence.OperationID), string(operationID)) {
				return deletefeature.Fence{}, fmt.Errorf("%w: %s returned another operation", ErrInvalidResponse, operation)
			}
			return fence, nil
		}
		lastErr = err
		c.logger.WarnContext(ctx, "order-service fence call failed",
			slog.String("operation", operation),
			slog.String("operation_id", string(operationID)),
			slog.Int("attempt", attempt),
			slog.Int("max_attempts", c.maxAttempts),
			slog.Bool("retryable", retryable),
			slog.String("error", err.Error()))
		if !retryable || ctx.Err() != nil {
			break
		}
	}
	return deletefeature.Fence{}, lastErr
}

// attempt sends one request. Errors never include the token or response
// body; only the status and order-service's error code are reported.
func (c *Client) attempt(ctx context.Context, method, endpoint string, body []byte) (deletefeature.Fence, bool, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return deletefeature.Fence{}, false, fmt.Errorf("%w: build request", ErrRejected)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		return deletefeature.Fence{}, true, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes)

	if response.StatusCode != http.StatusOK {
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(limited).Decode(&problem)
		_, _ = io.Copy(io.Discard, limited)
		detail := fmt.Sprintf("status %d", response.StatusCode)
		if problem.Code != "" {
			detail += " " + problem.Code
		}
		switch {
		case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
			return deletefeature.Fence{}, true, fmt.Errorf("%w: %s", ErrUnavailable, detail)
		case response.StatusCode == http.StatusNotFound:
			return deletefeature.Fence{}, false, fmt.Errorf("%w: %s", ErrFenceNotFound, detail)
		case response.StatusCode == http.StatusConflict:
			return deletefeature.Fence{}, false, fmt.Errorf("%w: %s", ErrInvalidTransition, detail)
		case response.StatusCode >= 400 && response.StatusCode < 500:
			return deletefeature.Fence{}, false, fmt.Errorf("%w: %s", ErrRejected, detail)
		default:
			return deletefeature.Fence{}, false, fmt.Errorf("%w: unexpected %s", ErrInvalidResponse, detail)
		}
	}

	var decoded fenceResponse
	if err := json.NewDecoder(limited).Decode(&decoded); err != nil {
		return deletefeature.Fence{}, false, fmt.Errorf("%w: malformed body", ErrInvalidResponse)
	}
	state := deletefeature.FenceState(decoded.State)
	switch state {
	case deletefeature.FenceOpen, deletefeature.FenceCommitted, deletefeature.FenceReleased:
	default:
		return deletefeature.Fence{}, false, fmt.Errorf("%w: unknown state %q", ErrInvalidResponse, decoded.State)
	}
	if !supplier.ValidSupplierID(supplier.SupplierID(decoded.SupplierID)) {
		return deletefeature.Fence{}, false, fmt.Errorf("%w: supplierId must be a UUID", ErrInvalidResponse)
	}
	return deletefeature.Fence{
		OperationID:      supplier.DeletionOperationID(decoded.OperationID),
		SupplierID:       supplier.SupplierID(decoded.SupplierID),
		State:            state,
		HasActiveErrands: decoded.HasActiveErrands,
	}, false, nil
}
