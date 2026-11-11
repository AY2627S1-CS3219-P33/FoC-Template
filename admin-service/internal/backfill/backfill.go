// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package backfill loads order-service's event history into the read model.
//
// It pages through the transition export and records each event exactly as
// the live consumer would, so the two meet cleanly: an event the stream
// already brought has the same messageId and is skipped, and the merge rules
// give the same errand rows whatever order events arrive in. Running it twice
// therefore changes nothing the second time. It does not enrich errands: it
// leaves them due, and the running service's enrichment worker fills in
// suppliers and buildings, so a backfill never rewrites a timestamp.
//
// Bind the live queue first (start the service once), then backfill. The
// export pages by outbox ID, which can differ from commit order for rows
// written at the same moment; such a row also arrives on the stream.
package backfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/orderclient"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/resilience"
)

// Source is order-service's transition export; *orderclient.Client
// implements it.
type Source interface {
	Transitions(ctx context.Context, after string, limit int) (orderclient.TransitionPage, error)
}

var _ Source = (*orderclient.Client)(nil)

// Recorder stores one event; *facts.Store implements it.
type Recorder interface {
	Apply(ctx context.Context, e facts.Event) (bool, error)
}

var _ Recorder = (*facts.Store)(nil)

// Config tunes the job. Zero fields take the defaults.
type Config struct {
	// PageSize is how many events one request asks for, at most 500.
	// Default 500.
	PageSize int
	// Attempts is how many times one page is requested while order-service
	// is unavailable. Default 5.
	Attempts int
	// Backoff spaces out those attempts. Default 1 s to 30 s.
	Backoff resilience.BackoffPolicy
	// Sleep waits between attempts; nil means a context-aware time.Sleep.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Result counts what one run did.
type Result struct {
	Pages int
	// Events is how many events the export returned.
	Events int
	// Applied is how many were new to the read model; the rest were already
	// there (from the stream or an earlier run).
	Applied int
	// Rejected is how many the read model refused as invalid. They are
	// logged, skipped and make Run return ErrRejected at the end.
	Rejected int
}

// ErrRejected means the export held events the read model refused. The rest
// were loaded.
var ErrRejected = errors.New("backfill: some events were rejected")

// Run loads every event, oldest first, and returns what it did. It stops at
// the first error that is not a refused event: an unavailable order-service
// after Attempts tries, or a database failure. Rerunning is always safe.
func Run(ctx context.Context, source Source, recorder Recorder, logger *slog.Logger, cfg Config) (Result, error) {
	if source == nil || recorder == nil || logger == nil {
		return Result{}, errors.New("backfill: source, recorder and logger are required")
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = orderclient.MaxPageSize
	}
	if cfg.Attempts == 0 {
		cfg.Attempts = 5
	}
	if cfg.Backoff.Base == 0 && cfg.Backoff.Cap == 0 {
		cfg.Backoff = resilience.BackoffPolicy{Base: time.Second, Cap: 30 * time.Second}
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	if cfg.PageSize < 1 || cfg.PageSize > orderclient.MaxPageSize || cfg.Attempts < 1 {
		return Result{}, errors.New("backfill: page size must be 1 to 500 and attempts at least 1")
	}
	if err := cfg.Backoff.Validate(); err != nil {
		return Result{}, err
	}

	var result Result
	after := ""
	for {
		page, err := fetch(ctx, source, after, cfg, logger)
		if err != nil {
			return result, err
		}
		result.Pages++
		for _, item := range page.Items {
			result.Events++
			applied, err := recorder.Apply(ctx, item.FactsEvent())
			switch {
			case errors.Is(err, facts.ErrInvalid):
				result.Rejected++
				logger.WarnContext(ctx, "backfill: skipping an event the read model refused",
					slog.String("message_id", item.MessageID), slog.String("reason", err.Error()))
			case err != nil:
				return result, fmt.Errorf("backfill: record %s: %w", item.MessageID, err)
			case applied:
				result.Applied++
			}
		}
		logger.InfoContext(ctx, "backfill progress",
			slog.Int("pages", result.Pages), slog.Int("events", result.Events), slog.Int("applied", result.Applied))
		if page.NextCursor == "" {
			break
		}
		if _, err := strconv.ParseInt(page.NextCursor, 10, 64); err != nil || page.NextCursor == after {
			// A cursor that does not advance would loop forever.
			return result, errors.New("backfill: order-service returned a cursor that does not advance")
		}
		after = page.NextCursor
	}
	if result.Rejected > 0 {
		return result, fmt.Errorf("%w: %d of %d", ErrRejected, result.Rejected, result.Events)
	}
	return result, nil
}

// fetch requests one page, retrying while order-service is unavailable.
func fetch(ctx context.Context, source Source, after string, cfg Config, logger *slog.Logger) (orderclient.TransitionPage, error) {
	var lastErr error
	for attempt := 1; attempt <= cfg.Attempts; attempt++ {
		page, err := source.Transitions(ctx, after, cfg.PageSize)
		if err == nil {
			return page, nil
		}
		if !errors.Is(err, orderclient.ErrUnavailable) {
			return orderclient.TransitionPage{}, err
		}
		lastErr = err
		if attempt == cfg.Attempts {
			break
		}
		delay := cfg.Backoff.Backoff(attempt)
		logger.WarnContext(ctx, "backfill: order-service unavailable; retrying",
			slog.Int("attempt", attempt), slog.Duration("retry_in", delay))
		if err := cfg.Sleep(ctx, delay); err != nil {
			return orderclient.TransitionPage{}, err
		}
	}
	return orderclient.TransitionPage{}, fmt.Errorf("backfill: giving up after %d attempts: %w", cfg.Attempts, lastErr)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
