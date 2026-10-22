// Package reconciler recovers stuck credit operations. An eligible errand is
// resolved within timeout + reconciler interval (including up to 20% jitter)
// + dispatcher backoff cap, provided the database and broker are available.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

type Outbox interface {
	GetByKey(context.Context, string) (msgrepo.Record, error)
}
type Config struct {
	Interval  time.Duration
	BatchSize int
	Logger    *slog.Logger
}
type Stats struct{ Runs, Applied, Failures, Panics uint64 }
type Runner struct {
	store    errand.Store
	outbox   Outbox
	settings errand.Settings
	clock    errand.Clock
	cfg      Config
	mu       sync.Mutex
	stats    map[errand.DueKind]Stats
}

func New(store errand.Store, outbox Outbox, settings errand.Settings, clock errand.Clock, cfg Config) (*Runner, error) {
	if store == nil || outbox == nil || settings == nil || clock == nil {
		return nil, fmt.Errorf("reconciler: dependencies are required")
	}
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	if cfg.Interval < time.Nanosecond || cfg.BatchSize < 1 {
		return nil, fmt.Errorf("reconciler: invalid configuration")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Runner{store: store, outbox: outbox, settings: settings, clock: clock, cfg: cfg, stats: make(map[errand.DueKind]Stats)}, nil
}

func (r *Runner) Stats(kind errand.DueKind) Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats[kind]
}

// Run starts independent reservation and edit jobs and waits for cancellation.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, kind := range []errand.DueKind{errand.StuckReservation, errand.StuckEdit} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_, _ = r.RunOnce(ctx, kind)
				delay := time.Duration(float64(r.cfg.Interval) * (0.8 + 0.4*rand.Float64()))
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	wg.Wait()
}

// RunOnce runs one bounded batch, recovering a panic so later ticks can retry.
func (r *Runner) RunOnce(ctx context.Context, kind errand.DueKind) (applied int, runErr error) {
	panicked := false
	defer func() {
		if p := recover(); p != nil {
			panicked = true
			runErr = fmt.Errorf("reconciler: job panicked")
			r.cfg.Logger.ErrorContext(ctx, "reconciler panic", "job", kind, "panic", p)
		}
		r.mu.Lock()
		s := r.stats[kind]
		s.Runs++
		s.Applied += uint64(applied)
		if runErr != nil {
			s.Failures++
		}
		if panicked {
			s.Panics++
		}
		r.stats[kind] = s
		r.mu.Unlock()
		if runErr != nil {
			r.cfg.Logger.ErrorContext(ctx, "reconciler run failed", "job", kind, "error", runErr)
		}
	}()
	if kind != errand.StuckReservation && kind != errand.StuckEdit {
		return 0, fmt.Errorf("reconciler: unknown job")
	}
	settings, err := r.settings.Get(ctx)
	if err != nil {
		return 0, err
	}
	timeout := settings.ReservationTimeout
	if kind == errand.StuckEdit {
		timeout = settings.EditTimeout
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("reconciler: timeout must be positive")
	}
	now := r.clock.Now()
	cutoff := now.Add(-timeout)
	var transitions []errand.Transition
	applied, runErr = r.store.ClaimDue(ctx, kind, cutoff, r.cfg.BatchSize, func(e errand.Errand) (errand.Transition, bool, error) {
		if !kind.Due(e, cutoff) {
			return errand.Transition{}, false, nil
		}
		ev := errand.EventEditRefused
		input := errand.EventInput{Reason: "ADJUST_TIMEOUT", EditTimedOut: true}
		if kind == errand.StuckEdit {
			input.EditKey = e.PendingEdit.Key
		}
		if kind == errand.StuckReservation {
			row, err := r.outbox.GetByKey(ctx, errand.IdempotencyKey(e.ID, "reserve", 0))
			if err != nil {
				return errand.Transition{}, false, err
			}
			if row.Status != msgrepo.StatusSent && row.Status != msgrepo.StatusFailed {
				return errand.Transition{}, false, nil
			}
			ev = errand.EventReservationFailed
			input = errand.EventInput{Reason: errand.ReasonReservationFailed}
		}
		t, err := errand.Next(e, ev, errand.SystemActor(), input, now)
		if err != nil {
			return errand.Transition{}, false, err
		}
		transitions = append(transitions, t)
		return t, true, nil
	})
	if runErr == nil {
		for _, t := range transitions {
			r.cfg.Logger.InfoContext(ctx, "reconciler applied transition", "job", kind, "errand_id", t.ErrandID, "event", t.Event, "from", t.From, "to", t.To, "version", t.NewVersion)
		}
	}
	return applied, runErr
}
