// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package orderclient calls order-service's export routes
// (order-service/docs/contracts.md, "Export routes") with admin-service's
// machine-to-machine token, which needs orders:admin-read.
package orderclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/serviceauth"
)

// MaxPageSize and MaxIDs are the export routes' limits.
const (
	MaxPageSize = 500
	MaxIDs      = 100
	// maxResponseBytes bounds what one response may hold: 500 errands are
	// about 200 KiB.
	maxResponseBytes = 4 << 20
)

// ErrUnavailable means order-service could not answer: it was unreachable,
// timed out, or failed. The caller retries later.
var ErrUnavailable = errors.New("orderclient: order-service unavailable")

// Errand is one row of GET /internal/export/errands. The names are nil when
// order-service does not know them, in particular when supplier-service was
// unavailable for it.
type Errand struct {
	ErrandID             string       `json:"errandId"`
	Status               facts.Status `json:"status"`
	Version              int          `json:"version"`
	CourierID            *string      `json:"courierId"`
	Reward               int          `json:"reward"`
	SupplierID           string       `json:"supplierId"`
	SupplierName         *string      `json:"supplierName"`
	DeliveryBuildingID   string       `json:"deliveryBuildingId"`
	DeliveryBuildingName *string      `json:"deliveryBuildingName"`
	CreatedAt            time.Time    `json:"createdAt"`
	UpdatedAt            time.Time    `json:"updatedAt"`
}

// Transition is one item of GET /internal/export/transitions.
type Transition struct {
	ID            int64        `json:"id"`
	MessageID     string       `json:"messageId"`
	Type          string       `json:"type"`
	ErrandID      string       `json:"errandId"`
	ErrandVersion int          `json:"errandVersion"`
	Event         string       `json:"event"`
	From          facts.Status `json:"from"`
	To            facts.Status `json:"to"`
	CourierID     string       `json:"courierId"`
	Reward        int          `json:"reward"`
	OccurredAt    time.Time    `json:"occurredAt"`
}

// FactsEvent is the transition as a read-model event.
func (t Transition) FactsEvent() facts.Event {
	return facts.Event{
		MessageID: t.MessageID, Type: t.Type, ErrandID: t.ErrandID, Version: t.ErrandVersion,
		From: t.From, To: t.To, CourierID: t.CourierID, Reward: t.Reward, OccurredAt: t.OccurredAt,
	}
}

// Client is safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for the order-service at baseURL (no trailing slash).
// tokens supplies the bearer token; timeout bounds each request.
func New(baseURL string, tokens serviceauth.TokenSource, timeout time.Duration) (*Client, error) {
	if baseURL == "" || tokens == nil || timeout <= 0 {
		return nil, errors.New("orderclient: base URL, token source and a positive timeout are required")
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, errors.New("orderclient: invalid base URL")
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout:   timeout,
			Transport: serviceauth.NewTransport(nil, tokens),
			// An export response is never a redirect; following one would
			// resend the token elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// ErrandsByID returns the listed errands that exist, at most MaxIDs.
func (c *Client) ErrandsByID(ctx context.Context, ids []string) ([]Errand, error) {
	if len(ids) == 0 || len(ids) > MaxIDs {
		return nil, fmt.Errorf("orderclient: ids must hold between 1 and %d errand IDs", MaxIDs)
	}
	var page struct {
		Items []Errand `json:"items"`
	}
	if err := c.get(ctx, "/internal/export/errands", url.Values{"ids": {strings.Join(ids, ",")}}, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

// ErrandPage is one page of errands. NextCursor is the next call's after, empty
// on the last page.
type ErrandPage struct {
	Items      []Errand
	NextCursor string
}

// Errands pages every errand by ID; after is empty for the first page.
func (c *Client) Errands(ctx context.Context, after string, limit int) (ErrandPage, error) {
	var page struct {
		Items      []Errand `json:"items"`
		NextCursor string   `json:"nextCursor"`
	}
	if err := c.get(ctx, "/internal/export/errands", pageQuery(after, limit), &page); err != nil {
		return ErrandPage{}, err
	}
	return ErrandPage{Items: page.Items, NextCursor: page.NextCursor}, nil
}

// TransitionPage is one page of transition events. NextCursor is the next
// call's after, empty on the last page.
type TransitionPage struct {
	Items      []Transition
	NextCursor string
}

// Transitions pages every transition event, oldest first; after is empty for
// the first page.
func (c *Client) Transitions(ctx context.Context, after string, limit int) (TransitionPage, error) {
	var page struct {
		Items      []Transition `json:"items"`
		NextCursor string       `json:"nextCursor"`
	}
	if err := c.get(ctx, "/internal/export/transitions", pageQuery(after, limit), &page); err != nil {
		return TransitionPage{}, err
	}
	return TransitionPage{Items: page.Items, NextCursor: page.NextCursor}, nil
}

func pageQuery(after string, limit int) url.Values {
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		query.Set("after", after)
	}
	return query
}

func (c *Client) get(ctx context.Context, path string, query url.Values, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The error can hold the URL; say only that the call failed.
		return fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return fmt.Errorf("%w: response unreadable", ErrUnavailable)
	}
	switch {
	case response.StatusCode == http.StatusOK:
	case response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	default:
		// 400, 401 and 403 are our misconfiguration or a bug, not a reason to
		// retry as if the service were down.
		return fmt.Errorf("orderclient: %s refused: status %d", path, response.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return errors.New("orderclient: response is not the documented JSON")
	}
	return nil
}
