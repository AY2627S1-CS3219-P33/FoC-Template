package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// addDeadlineJob registers the job that fires ev on errands due for kind.
func (s *Scheduler) addDeadlineJob(kind errand.DueKind, ev errand.Event, interval time.Duration) {
	s.jobs = append(s.jobs, &job{
		name:     string(kind),
		interval: interval,
		limit:    s.cfg.BatchSize,
		run: func(ctx context.Context, j *job) (BatchResult, error) {
			return s.runDeadline(ctx, j, kind, ev)
		},
	})
}

// runDeadline runs one ClaimDue batch for kind at the clock's current time.
func (s *Scheduler) runDeadline(ctx context.Context, j *job, kind errand.DueKind, ev errand.Event) (BatchResult, error) {
	now := s.clock.Now()
	var decided []errand.Transition
	seen := 0
	decide := func(e errand.Errand) (errand.Transition, bool, error) {
		seen++
		t, ok := s.decide(ctx, j, kind, ev, e, now)
		if ok {
			decided = append(decided, t)
		}
		return t, ok, nil
	}

	applied, err := s.store.ClaimDue(ctx, kind, now, j.limit, decide)
	if err != nil {
		return BatchResult{}, err
	}
	for _, t := range decided {
		s.logger.LogAttrs(ctx, slog.LevelInfo, "scheduler applied transition",
			slog.String("job", j.name),
			slog.String("errand_id", t.ErrandID),
			slog.String("event", string(t.Event)),
			slog.String("from", string(t.From)),
			slog.String("to", string(t.To)),
			slog.Int("version", t.NewVersion),
		)
	}
	return BatchResult{Seen: seen, Applied: applied}, nil
}

// decide builds the transition for the locked errand e at now, or reports a
// skip. The errand is skipped when it is no longer due for kind (its
// deadline was extended or it changed state after it was selected) or when
// Next refuses the event; neither aborts the batch.
func (s *Scheduler) decide(ctx context.Context, j *job, kind errand.DueKind, ev errand.Event, e errand.Errand, now time.Time) (errand.Transition, bool) {
	if !kind.Due(e, now) {
		s.logger.LogAttrs(ctx, slog.LevelDebug, "scheduler skipped errand that is no longer due",
			slog.String("job", j.name),
			slog.String("errand_id", e.ID),
			slog.String("status", string(e.Status)),
		)
		return errand.Transition{}, false
	}
	t, err := errand.Next(e, ev, errand.SystemActor(), errand.EventInput{}, now)
	if err != nil {
		s.logger.LogAttrs(ctx, slog.LevelWarn, "scheduler skipped errand refused by the state machine",
			slog.String("job", j.name),
			slog.String("errand_id", e.ID),
			slog.String("status", string(e.Status)),
			slog.String("error", err.Error()),
		)
		return errand.Transition{}, false
	}
	return t, true
}
