package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"golang.org/x/sync/errgroup"
)

// Outbox is the delivery side of storage; business handlers insert messages
// through the errand Store in their own transaction.
type Outbox interface {
	Claim(context.Context, string, bool, int, time.Duration) ([]msgrepo.Row, error)
	MarkSent(context.Context, msgrepo.Row) error
	Reschedule(context.Context, msgrepo.Row, time.Duration, string) error
	MarkFailed(context.Context, msgrepo.Row, string) error
	ResendUnconfirmed(context.Context, time.Duration, int) ([]msgrepo.Resent, error)
}

type Dispatcher struct {
	outbox    Outbox
	publisher errand.Publisher
	settings  errand.Settings
	logger    *slog.Logger
	worker    string
}

func NewDispatcher(outbox Outbox, publisher errand.Publisher, settings errand.Settings, logger *slog.Logger, worker string) *Dispatcher {
	if outbox == nil || publisher == nil || settings == nil || logger == nil || worker == "" {
		panic("messaging: dispatcher dependencies and worker ID are required")
	}
	return &Dispatcher{outbox: outbox, publisher: publisher, settings: settings, logger: logger, worker: worker}
}

// Run isolates event backlogs from credit work. Each loop claims one row at a
// time so later rows do not lose their leases waiting behind a slow publish.
func (d *Dispatcher) Run(ctx context.Context) error {
	group, ctx := errgroup.WithContext(ctx)
	for _, credit := range []bool{true, false} {
		group.Go(func() error { return d.loop(ctx, credit) })
	}
	return group.Wait()
}

func (d *Dispatcher) loop(ctx context.Context, credit bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		worked, err := d.dispatch(ctx, credit)
		if err != nil && ctx.Err() == nil {
			d.logger.ErrorContext(ctx, "outbox dispatch failed", slog.Bool("credit", credit), slog.Any("error", err))
		}
		if worked && err == nil {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, credit bool) (bool, error) {
	if credit {
		settings, err := d.settings.Get(ctx)
		if err != nil {
			return false, err
		}
		if _, err := d.outbox.ResendUnconfirmed(ctx, settings.OutboxConfirmTimeout, 100); err != nil {
			return false, err
		}
	}
	rows, err := d.outbox.Claim(ctx, d.worker, credit, 1, time.Minute)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Attempts > 10 {
			if err := d.leased(ctx, row, d.outbox.MarkFailed(ctx, row, "ATTEMPTS_EXHAUSTED")); err != nil {
				return true, err
			}
			continue
		}
		message, err := MessageFromOutbox(row)
		if err == nil {
			publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = d.publisher.Publish(publishCtx, message)
			cancel()
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if err == nil {
			if err := d.leased(ctx, row, d.outbox.MarkSent(ctx, row)); err != nil {
				return true, err
			}
			continue
		}
		code := "PUBLISH_FAILED"
		if errors.Is(err, errand.ErrUnpublishable) {
			code = "UNPUBLISHABLE"
		}
		if errors.Is(err, ErrUnroutable) {
			code = "UNROUTABLE"
		}
		d.logger.WarnContext(ctx, "outbox publication failed", slog.Int64("outbox_id", row.ID), slog.String("code", code), slog.Int("attempt", int(row.Attempts)))
		if errors.Is(err, errand.ErrUnpublishable) || row.Attempts >= 10 {
			err = d.outbox.MarkFailed(ctx, row, code)
		} else {
			err = d.outbox.Reschedule(ctx, row, resilience.Backoff(int(row.Attempts)), code)
		}
		if err := d.leased(ctx, row, err); err != nil {
			return true, err
		}
	}
	return len(rows) > 0, nil
}

// leased drops ErrLeaseLost: another worker owns the row now, or a credit
// result already settled it.
func (d *Dispatcher) leased(ctx context.Context, row msgrepo.Row, err error) error {
	if errors.Is(err, msgrepo.ErrLeaseLost) {
		d.logger.WarnContext(ctx, "outbox lease lost", slog.Int64("outbox_id", row.ID), slog.String("kind", row.Kind))
		return nil
	}
	return err
}

// MessageFromOutbox preserves the original identity, version and occurrence
// time on every retry; it never reads the errand's current version.
func MessageFromOutbox(row msgrepo.Row) (errand.Message, error) {
	var data struct {
		ErrandID      string    `json:"errandId"`
		ErrandVersion int       `json:"errandVersion"`
		At            time.Time `json:"at"`
	}
	if err := json.Unmarshal(row.Payload, &data); err != nil || data.ErrandID != row.ErrandID || data.ErrandVersion < 1 {
		return errand.Message{}, fmt.Errorf("%w: invalid outbox payload", errand.ErrUnpublishable)
	}
	m := errand.Message{Envelope: errand.Envelope{MessageID: row.IdempotencyKey, ErrandID: row.ErrandID, ErrandVersion: data.ErrandVersion, OccurredAt: row.CreatedAt, Data: row.Payload}}
	if slices.Contains(errand.CreditCommands(), row.Kind) {
		m.Exchange, m.RoutingKey = errand.ExchangeCommands, row.Kind
	} else if strings.HasPrefix(row.Kind, "event.") && slices.Contains(errand.EventNames(), errand.EventName(strings.TrimPrefix(row.Kind, "event."))) {
		m.Exchange, m.RoutingKey = errand.ExchangeEvents, strings.TrimPrefix(row.Kind, "event.")
		if data.At.IsZero() {
			return errand.Message{}, fmt.Errorf("%w: event occurrence time is required", errand.ErrUnpublishable)
		}
		m.Envelope.OccurredAt = data.At
	} else {
		return errand.Message{}, fmt.Errorf("%w: unknown outbox kind", errand.ErrUnpublishable)
	}
	m.Envelope.Type = m.RoutingKey
	if _, err := encode(m); err != nil {
		return errand.Message{}, err
	}
	return m, nil
}
