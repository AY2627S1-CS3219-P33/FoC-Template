// Package creditmsg consumes credit-service's results from
// order-service.credit-results and applies them to errands and to the outbox
// rows of the commands they answer (M4, gate C1 in docs/contracts.md).
//
// Every result is matched to its command by correlationId, the command's
// outbox idempotency key. Handling is idempotent by state, not by messageId:
// a result whose transition no longer applies (the errand moved on, or the
// pending edit it answers was cleared) is acknowledged without a change, so
// a redelivered or re-published result changes nothing.
//
// Store.Apply commits its own transaction (the Store port takes none), so
// the errand transition and the outbox confirmation are two commits, always
// in that order. If the second fails, the message is requeued; on
// redelivery the transition is already applied, Next refuses it as
// "already resolved", and the confirmation is retried. A crash between the
// two commits leaves the command row SENT, and if the result is lost too
// the dispatcher's unconfirmed sweep re-sends the command, which
// credit-service answers idempotently.
package creditmsg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Outbox is the part of msgrepo.Repository the handler needs.
type Outbox interface {
	// ConfirmByKey moves a PENDING or SENT credit row to CONFIRMED and
	// reports whether a row changed.
	ConfirmByKey(ctx context.Context, key string) (bool, error)
	// MarkFailedByKey moves a PENDING or SENT credit row to FAILED with
	// code and reports whether a row changed.
	MarkFailedByKey(ctx context.Context, key, code string) (bool, error)
}

// Disposition is what the consumer does with a delivery after Handle.
type Disposition int

const (
	// Ack: the result's effect is committed, or it was already resolved.
	Ack Disposition = iota
	// Requeue: a transient failure (database unavailable); deliver again.
	Requeue
	// Reject: the message can never be processed; dead-letter it.
	Reject
)

func (d Disposition) String() string {
	switch d {
	case Ack:
		return "ack"
	case Requeue:
		return "requeue"
	default:
		return "reject"
	}
}

// maxConflicts bounds how often one result re-reads the errand after
// Store.Apply lost its compare-and-swap to a concurrent change. Each retry
// re-runs Next on the fresh errand, which either applies or reports that the
// result no longer applies.
const maxConflicts = 3

// errPoison marks a message that breaks the contract.
var errPoison = errors.New("creditmsg: message breaks the credit contract")

// Handler applies one credit result. It is safe for concurrent use.
type Handler struct {
	store   errand.Store
	outbox  Outbox
	clock   errand.Clock
	logger  *slog.Logger
	metrics Metrics
}

// NewHandler returns a handler. A nil metrics means a fresh Counters.
func NewHandler(store errand.Store, outbox Outbox, clock errand.Clock, logger *slog.Logger, metrics Metrics) (*Handler, error) {
	if store == nil || outbox == nil || clock == nil || logger == nil {
		return nil, errors.New("creditmsg: handler dependencies are required")
	}
	if metrics == nil {
		metrics = NewCounters()
	}
	return &Handler{store: store, outbox: outbox, clock: clock, logger: logger, metrics: metrics}, nil
}

// Metrics returns the counters the handler writes to.
func (h *Handler) Metrics() Metrics { return h.metrics }

// result is one decoded credit result.
type result struct {
	routingKey string
	env        errand.Envelope
	data       errand.CreditResult
}

func (r result) attrs() []any {
	attrs := []any{
		slog.String("routing_key", r.routingKey),
		slog.String("errand_id", r.env.ErrandID),
		slog.String("correlation_id", r.env.CorrelationID),
		slog.String("message_id", r.env.MessageID),
	}
	if r.data.Amount != 0 {
		attrs = append(attrs, slog.Int("amount", r.data.Amount))
	}
	if r.data.Code != "" {
		attrs = append(attrs, slog.String("code", r.data.Code))
	}
	return attrs
}

// Handle decodes and applies one delivery and says how to settle it. It
// logs every outcome and counts it per result type.
func (h *Handler) Handle(ctx context.Context, routingKey string, body []byte) Disposition {
	r, err := decode(routingKey, body)
	if err != nil {
		h.logger.ErrorContext(ctx, "credit result rejected; dead-lettering",
			slog.String("routing_key", routingKey), slog.String("errand_id", r.env.ErrandID),
			slog.String("correlation_id", r.env.CorrelationID), slog.Any("error", err))
		h.metrics.Handled(metricKey(routingKey), OutcomeRejected)
		return Reject
	}

	outcome, err := h.apply(ctx, r)
	switch {
	case errors.Is(err, errPoison):
		h.logger.ErrorContext(ctx, "credit result cannot be applied; dead-lettering", append(r.attrs(), slog.Any("error", err))...)
		h.metrics.Handled(r.routingKey, OutcomeRejected)
		return Reject
	case err != nil:
		h.logger.WarnContext(ctx, "credit result not applied; will retry", append(r.attrs(), slog.Any("error", err))...)
		h.metrics.Handled(r.routingKey, OutcomeRetried)
		return Requeue
	}
	h.metrics.Handled(r.routingKey, outcome)
	return Ack
}

// decode validates the envelope against the contract. On error the returned
// result still carries whatever identity could be read, for the log.
func decode(routingKey string, body []byte) (result, error) {
	r := result{routingKey: routingKey}
	if !slices.Contains(errand.CreditResults(), routingKey) {
		return r, fmt.Errorf("unknown routing key %q", routingKey)
	}
	if err := json.Unmarshal(body, &r.env); err != nil {
		return r, errors.New("undecodable envelope")
	}
	e := r.env
	switch {
	case e.Type != routingKey:
		return r, fmt.Errorf("envelope type %q does not match the routing key", e.Type)
	case e.ErrandID == "" || e.CorrelationID == "":
		return r, errors.New("errandId and correlationId are required")
	case !strings.HasPrefix(e.CorrelationID, e.ErrandID+":"):
		return r, errors.New("correlationId is not a command of errandId")
	}
	if data := strings.TrimSpace(string(e.Data)); data != "" && data != "null" {
		if err := json.Unmarshal(e.Data, &r.data); err != nil {
			return r, errors.New("undecodable result data")
		}
	}

	key := e.CorrelationID
	switch routingKey {
	case errand.ResultCreditReserved, errand.ResultCreditReservationRefused:
		if key != errand.IdempotencyKey(e.ErrandID, "reserve", 0) {
			return r, errors.New("correlationId is not a reserve command")
		}
	case errand.ResultCreditAdjusted, errand.ResultCreditAdjustRefused:
		if !strings.HasPrefix(key, e.ErrandID+":adjust:") && !strings.HasPrefix(key, e.ErrandID+":adjust-revert:") {
			return r, errors.New("correlationId is not an adjust command")
		}
	case errand.ResultCreditReleased:
		if key != errand.IdempotencyKey(e.ErrandID, "release", 0) {
			return r, errors.New("correlationId is not a release command")
		}
	case errand.ResultCreditTransferred:
		if key != errand.IdempotencyKey(e.ErrandID, "transfer", 0) {
			return r, errors.New("correlationId is not a transfer command")
		}
	}
	switch routingKey {
	case errand.ResultCreditReservationRefused, errand.ResultCreditAdjustRefused, errand.ResultCreditOperationFailed:
		if r.data.Code == "" {
			return r, errors.New("code is required")
		}
	}
	return r, nil
}

// apply runs the M4 table for one result.
func (h *Handler) apply(ctx context.Context, r result) (string, error) {
	key := r.env.CorrelationID
	switch r.routingKey {
	case errand.ResultCreditReserved:
		return h.transition(ctx, r, errand.EventReservationConfirmed, errand.EventInput{})

	case errand.ResultCreditReservationRefused:
		return h.transition(ctx, r, errand.EventReservationRefused, errand.EventInput{Reason: r.data.Code})

	case errand.ResultCreditAdjusted:
		return h.transition(ctx, r, errand.EventEditConfirmed, errand.EventInput{EditKey: key})

	case errand.ResultCreditAdjustRefused:
		return h.transition(ctx, r, errand.EventEditRefused, errand.EventInput{EditKey: key, Reason: r.data.Code})

	case errand.ResultCreditReleased, errand.ResultCreditTransferred:
		changed, err := h.outbox.ConfirmByKey(ctx, key)
		if err != nil {
			return "", fmt.Errorf("confirm command: %w", err)
		}
		if !changed {
			h.logger.InfoContext(ctx, "credit result for a row already settled", r.attrs()...)
			return OutcomeResolved, nil
		}
		h.logger.InfoContext(ctx, "credit command confirmed", r.attrs()...)
		return OutcomeApplied, nil

	default: // credit.operation_failed
		changed, err := h.outbox.MarkFailedByKey(ctx, key, r.data.Code)
		if err != nil {
			return "", fmt.Errorf("mark command failed: %w", err)
		}
		if !changed {
			h.logger.WarnContext(ctx, "credit operation failed for a row already settled", r.attrs()...)
			return OutcomeResolved, nil
		}
		h.logger.ErrorContext(ctx, "credit operation failed; outbox row FAILED, needs an admin", r.attrs()...)
		return OutcomeApplied, nil
	}
}

// transition applies ev as SYSTEM, then confirms the command's row. A result
// that no longer applies is "already resolved": the row is still confirmed,
// because credit-service did answer the command.
func (h *Handler) transition(ctx context.Context, r result, ev errand.Event, input errand.EventInput) (string, error) {
	outcome, err := h.applyEvent(ctx, r, ev, input)
	if err != nil {
		return "", err
	}
	if _, err := h.outbox.ConfirmByKey(ctx, r.env.CorrelationID); err != nil {
		return "", fmt.Errorf("confirm command: %w", err)
	}
	return outcome, nil
}

func (h *Handler) applyEvent(ctx context.Context, r result, ev errand.Event, input errand.EventInput) (string, error) {
	for conflict := 0; ; conflict++ {
		e, err := h.store.Get(ctx, r.env.ErrandID)
		if code := codeOf(err); code == apperror.ErrandNotFound {
			return "", fmt.Errorf("%w: errand not found", errPoison)
		} else if err != nil {
			return "", fmt.Errorf("read errand: %w", err)
		}
		// An edit outcome answers one pending edit. Once it is cleared (an
		// accept dropped it, the reconciler refused it, or this result was
		// applied before) the result has nothing left to do.
		if input.EditKey != "" && (e.PendingEdit == nil || e.PendingEdit.Key != input.EditKey) {
			h.logger.WarnContext(ctx, "credit result for an edit no longer pending; ignoring", append(r.attrs(), slog.String("status", string(e.Status)))...)
			return OutcomeResolved, nil
		}

		t, err := errand.Next(e, ev, errand.SystemActor(), input, h.clock.Now())
		if err == nil {
			_, err = h.store.Apply(ctx, t)
			if err == nil {
				h.logger.InfoContext(ctx, "credit result applied",
					append(r.attrs(), slog.String("from", string(t.From)), slog.String("to", string(t.To)), slog.Int("version", t.NewVersion))...)
				return OutcomeApplied, nil
			}
			if resolved(err) && conflict+1 < maxConflicts {
				continue // lost the compare-and-swap; decide again on fresh state
			}
		}
		switch code := codeOf(err); {
		case resolved(err):
			h.logger.WarnContext(ctx, "credit result already resolved; ignoring",
				append(r.attrs(), slog.String("status", string(e.Status)), slog.String("reason", string(code)))...)
			return OutcomeResolved, nil
		case code != "":
			return "", fmt.Errorf("%w: %s", errPoison, code)
		default:
			return "", fmt.Errorf("apply transition: %w", err)
		}
	}
}

// resolved reports whether err says the errand has already moved past the
// state this result answers.
func resolved(err error) bool {
	code := codeOf(err)
	return code == apperror.ErrandUnavailable || code == apperror.ErrandChanged
}

func codeOf(err error) apperror.Code {
	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return ""
}

// metricKey bounds counter labels to the known result types.
func metricKey(routingKey string) string {
	if slices.Contains(errand.CreditResults(), routingKey) {
		return routingKey
	}
	return ResultUnknown
}
