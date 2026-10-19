package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// ReminderStore is what the reminder jobs need from opsrepo: a read-only
// candidate query and the reminder outbox insert. *opsrepo.Repository
// implements it.
type ReminderStore interface {
	ListReminderCandidates(ctx context.Context, kind opsrepo.ReminderKind, now, remindBefore time.Time, limit int) ([]opsrepo.ReminderCandidate, error)
	InsertCurrentReminder(ctx context.Context, row errand.OutboxRow, status errand.Status, version int) (inserted bool, err error)
}

var _ ReminderStore = (*opsrepo.Repository)(nil)

// ReminderData is the payload of a reminder row, the envelope data of
// errand.verification_reminder and errand.pickup_deadline_approaching
// (docs/contracts.md, "Reminder events: ReminderData").
type ReminderData struct {
	ErrandID      string        `json:"errandId"`
	ErrandVersion int           `json:"errandVersion"`
	Status        errand.Status `json:"status"`
	RequesterID   string        `json:"requesterId"`
	CourierID     string        `json:"courierId"`
	// Deadline is the verification or pickup deadline the reminder is about.
	Deadline time.Time `json:"deadline"`
	// At is when the reminder was raised.
	At time.Time `json:"at"`
}

// reminder describes one reminder job. A reminder is due once the deadline
// is at most lead away, and is raised at most once per errand version
// because the version is part of its idempotency key.
type reminder struct {
	name      string
	kind      opsrepo.ReminderKind
	event     errand.EventName
	operation string
	// lead returns how far ahead of the deadline to remind; settings is nil
	// when needsSettings is false.
	lead          func(settings *errand.SystemSettings) time.Duration
	needsSettings bool
}

// verificationReminder reminds the requester to verify a delivery once
// VerificationReminderAfter has passed since delivery, i.e. when the
// verification deadline is VerificationAfter − VerificationReminderAfter
// away (F3.10.3; 5 min after delivery by default).
var verificationReminder = reminder{
	name:      JobVerificationReminder,
	kind:      opsrepo.ReminderVerification,
	event:     errand.ErrandVerificationReminder,
	operation: "verify-reminder",
	lead: func(s *errand.SystemSettings) time.Duration {
		return s.Deadlines.VerificationAfter - s.Deadlines.VerificationReminderAfter
	},
	needsSettings: true,
}

// pickupReminder warns the courier when the pickup deadline is lead away.
func pickupReminder(lead time.Duration) reminder {
	return reminder{
		name:      JobPickupReminder,
		kind:      opsrepo.ReminderPickup,
		event:     errand.ErrandPickupDeadlineApproaching,
		operation: "pickup-reminder",
		lead:      func(*errand.SystemSettings) time.Duration { return lead },
	}
}

// addReminderJob registers r.
func (s *Scheduler) addReminderJob(r reminder, interval time.Duration) {
	s.jobs = append(s.jobs, &job{
		name:     r.name,
		interval: interval,
		limit:    min(s.cfg.BatchSize, opsrepo.MaxPageSize),
		run: func(ctx context.Context, j *job) (BatchResult, error) {
			return s.runReminder(ctx, j, r)
		},
	})
}

// runReminder raises r for one batch of candidates. It changes no errand: it
// only inserts reminder outbox rows, and a candidate that has left the state
// or version it was selected in gets no row.
func (s *Scheduler) runReminder(ctx context.Context, j *job, r reminder) (BatchResult, error) {
	var settings *errand.SystemSettings
	if r.needsSettings {
		got, err := s.settings.Get(ctx)
		if err != nil {
			return BatchResult{}, fmt.Errorf("read settings: %w", err)
		}
		settings = &got
	}
	now := s.clock.Now()
	candidates, err := s.reminders.ListReminderCandidates(ctx, r.kind, now, now.Add(r.lead(settings)), j.limit)
	if err != nil {
		return BatchResult{}, err
	}

	res := BatchResult{Seen: len(candidates)}
	for _, c := range candidates {
		row := errand.OutboxRow{
			Kind:           r.event.Kind(),
			IdempotencyKey: errand.IdempotencyKey(c.ErrandID, r.operation, c.Version),
			ErrandID:       c.ErrandID,
			Payload: ReminderData{
				ErrandID:      c.ErrandID,
				ErrandVersion: c.Version,
				Status:        c.Status,
				RequesterID:   c.RequesterID,
				CourierID:     c.CourierID,
				Deadline:      c.Deadline,
				At:            now,
			},
		}
		inserted, err := s.reminders.InsertCurrentReminder(ctx, row, c.Status, c.Version)
		if err != nil {
			return BatchResult{}, err
		}
		if !inserted {
			s.logger.LogAttrs(ctx, slog.LevelDebug, "scheduler skipped reminder for errand that moved on",
				slog.String("job", j.name),
				slog.String("errand_id", c.ErrandID),
				slog.Int("version", c.Version),
			)
			continue
		}
		res.Applied++
		s.logger.LogAttrs(ctx, slog.LevelInfo, "scheduler raised reminder",
			slog.String("job", j.name),
			slog.String("errand_id", c.ErrandID),
			slog.String("kind", row.Kind),
			slog.Int("version", c.Version),
		)
	}
	return res, nil
}
