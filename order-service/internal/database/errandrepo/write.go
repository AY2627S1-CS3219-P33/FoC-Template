package errandrepo

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/errandgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Create follows "Errand creation" in docs/contracts.md. A replay of the
// requester's idempotency key returns the stored errand before any fence
// check, so a replay answers the same way after a fence is raised.
func (s *Store) Create(ctx context.Context, e errand.Errand, t errand.Transition) (errand.Errand, bool, error) {
	switch {
	case e.Status != errand.StatusNone, t.Event != errand.EventSubmit,
		t.From != errand.StatusNone, t.ErrandID != e.ID:
		return errand.Errand{}, false, appErr(apperror.InvalidArgument, "not a Submit transition for errand %q", e.ID)
	case e.CreateIdempotencyKey == "":
		return errand.Errand{}, false, appErr(apperror.InvalidArgument, "create idempotency key is required")
	}
	params, err := insertParams(t.ApplyTo(e))
	if err != nil {
		return errand.Errand{}, false, err
	}
	key := errandgen.GetErrandByCreateKeyParams{
		RequesterID:          e.RequesterID,
		CreateIdempotencyKey: e.CreateIdempotencyKey,
	}

	var stored errand.Errand
	var created bool
	err = s.inTx(ctx, func(q *errandgen.Queries) error {
		existing, err := q.GetErrandByCreateKey(ctx, key)
		if err == nil {
			stored, err = errandFromRow(existing)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := checkFence(ctx, q, params.SupplierID, e.Details.SupplierID); err != nil {
			return err
		}
		row, err := q.InsertErrand(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			// A concurrent request with the same key committed first. Under
			// READ COMMITTED this statement sees its row.
			if existing, err = q.GetErrandByCreateKey(ctx, key); err != nil {
				return err
			}
			stored, err = errandFromRow(existing)
			return err
		}
		if err != nil {
			return err
		}
		if stored, err = errandFromRow(row); err != nil {
			return err
		}
		created = true
		return writeTransitionRows(ctx, q, e, stored, t)
	})
	if err != nil {
		return errand.Errand{}, false, err
	}
	return stored, created, nil
}

func (s *Store) Apply(ctx context.Context, t errand.Transition) (errand.Errand, error) {
	if t.From == errand.StatusNone {
		return errand.Errand{}, appErr(apperror.InvalidArgument, "use Create for a Submit transition")
	}
	id, ok := toUUID(t.ErrandID)
	if !ok {
		return errand.Errand{}, notFound(t.ErrandID)
	}
	var stored errand.Errand
	err := s.inTx(ctx, func(q *errandgen.Queries) error {
		row, err := q.GetErrandForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(t.ErrandID)
		}
		if err != nil {
			return err
		}
		cur, err := errandFromRow(row)
		if err != nil {
			return err
		}
		stored, err = applyLocked(ctx, q, cur, t)
		return err
	})
	if err != nil {
		return errand.Errand{}, err
	}
	return stored, nil
}

func (s *Store) RecordRejection(ctx context.Context, r errand.Rejection) error {
	if !r.Action.Valid() {
		return appErr(apperror.InvalidArgument, "unknown rejected action %q", r.Action)
	}
	if r.RequestID == "" {
		return appErr(apperror.InvalidArgument, "request ID is required")
	}
	id, ok := toUUID(r.ErrandID)
	if !ok {
		return notFound(r.ErrandID)
	}
	return s.inTx(ctx, func(q *errandgen.Queries) error {
		row, err := q.GetErrand(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(r.ErrandID)
		}
		if err != nil {
			return err
		}
		payload, err := json.Marshal(errand.RejectionData{
			ErrandID:      r.ErrandID,
			ErrandVersion: int(row.Version),
			Status:        errand.Status(row.Status),
			Action:        r.Action,
			Code:          r.Code,
			ActorType:     r.Actor.Type,
			ActorID:       r.Actor.ID,
			At:            r.At,
		})
		if err != nil {
			return fmt.Errorf("errandrepo: encode rejection: %w", err)
		}
		_, err = q.InsertOutboxIfAbsent(ctx, errandgen.InsertOutboxIfAbsentParams{
			Kind:           r.Action.EventName().Kind(),
			IdempotencyKey: errand.RejectionKey(r.ErrandID, r.Action, r.RequestID),
			ErrandID:       id,
			Payload:        payload,
		})
		return err
	})
}

// ClaimDue selects and locks the batch, lets decide choose every transition
// while the rows are locked, then applies them in order. Any failure rolls
// back the whole batch.
func (s *Store) ClaimDue(ctx context.Context, kind errand.DueKind, now time.Time, limit int, decide errand.DecideFunc) (int, error) {
	claim, ok := claimQueries[kind]
	if !ok {
		return 0, appErr(apperror.InvalidArgument, "unknown due kind %q", kind)
	}
	if limit < 1 {
		return 0, appErr(apperror.InvalidArgument, "limit must be positive")
	}

	applied := 0
	err := s.inTx(ctx, func(q *errandgen.Queries) error {
		rows, err := claim(ctx, q, timestamp(now), rowLimit(limit))
		if err != nil {
			return err
		}
		batch, err := errandsFromRows(rows)
		if err != nil {
			return err
		}
		locked := make(map[string]errand.Errand, len(batch))
		var decided []errand.Transition
		for _, e := range batch {
			locked[e.ID] = e
			t, apply, err := decide(e)
			if err != nil {
				return err
			}
			if apply {
				decided = append(decided, t)
			}
		}
		for _, t := range decided {
			cur, ok := locked[t.ErrandID]
			if !ok {
				return fmt.Errorf("errandrepo: decide returned a transition for errand %q, which is not in the batch", t.ErrandID)
			}
			if t.From == errand.StatusNone {
				return appErr(apperror.InvalidArgument, "ClaimDue cannot apply a Submit transition")
			}
			stored, err := applyLocked(ctx, q, cur, t)
			if err != nil {
				return fmt.Errorf("errandrepo: decided transition for locked errand %q: %w", t.ErrandID, err)
			}
			// A second transition for the same errand must follow this one.
			locked[t.ErrandID] = stored
		}
		applied = len(decided)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return applied, nil
}

type claimQuery func(ctx context.Context, q *errandgen.Queries, now pgtype.Timestamptz, limit int32) ([]errandgen.Errand, error)

var claimQueries = map[errand.DueKind]claimQuery{
	errand.AcceptanceExpiry: func(ctx context.Context, q *errandgen.Queries, now pgtype.Timestamptz, limit int32) ([]errandgen.Errand, error) {
		return q.ClaimAcceptanceExpiry(ctx, errandgen.ClaimAcceptanceExpiryParams{Now: now, RowLimit: limit})
	},
	errand.PickupDeadline: func(ctx context.Context, q *errandgen.Queries, now pgtype.Timestamptz, limit int32) ([]errandgen.Errand, error) {
		return q.ClaimPickupDeadline(ctx, errandgen.ClaimPickupDeadlineParams{Now: now, RowLimit: limit})
	},
	errand.DeliveryDeadline: func(ctx context.Context, q *errandgen.Queries, now pgtype.Timestamptz, limit int32) ([]errandgen.Errand, error) {
		return q.ClaimDeliveryDeadline(ctx, errandgen.ClaimDeliveryDeadlineParams{Now: now, RowLimit: limit})
	},
	errand.VerificationLapse: func(ctx context.Context, q *errandgen.Queries, now pgtype.Timestamptz, limit int32) ([]errandgen.Errand, error) {
		return q.ClaimVerificationLapse(ctx, errandgen.ClaimVerificationLapseParams{Now: now, RowLimit: limit})
	},
	errand.StuckReservation: func(ctx context.Context, q *errandgen.Queries, now pgtype.Timestamptz, limit int32) ([]errandgen.Errand, error) {
		return q.ClaimStuckReservation(ctx, errandgen.ClaimStuckReservationParams{Now: now, RowLimit: limit})
	},
	errand.StuckEdit: func(ctx context.Context, q *errandgen.Queries, now pgtype.Timestamptz, limit int32) ([]errandgen.Errand, error) {
		return q.ClaimStuckEdit(ctx, errandgen.ClaimStuckEditParams{Now: now, RowLimit: limit})
	},
}

// applyLocked writes t for cur, whose row this transaction has locked: the
// compare-and-swap, the fence check for a supplier-changing edit, the errand
// row, and the transition, review and outbox rows.
func applyLocked(ctx context.Context, q *errandgen.Queries, cur errand.Errand, t errand.Transition) (errand.Errand, error) {
	if err := compareAndSwap(cur, t); err != nil {
		return errand.Errand{}, err
	}
	if to, ok := editTargetSupplier(cur, t); ok {
		id, err := parseUUID("supplierId", to)
		if err != nil {
			return errand.Errand{}, err
		}
		if err := checkFence(ctx, q, id, to); err != nil {
			return errand.Errand{}, err
		}
	}
	params, err := updateParams(t.ApplyTo(cur), t)
	if err != nil {
		return errand.Errand{}, err
	}
	row, err := q.UpdateErrand(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		// The row is locked and was just compared, so this is a bug.
		return errand.Errand{}, fmt.Errorf("errandrepo: compare-and-swap on locked errand %q matched no row", cur.ID)
	}
	if err != nil {
		return errand.Errand{}, err
	}
	stored, err := errandFromRow(row)
	if err != nil {
		return errand.Errand{}, err
	}
	if err := writeTransitionRows(ctx, q, cur, stored, t); err != nil {
		return errand.Errand{}, err
	}
	return stored, nil
}

// compareAndSwap is the UPDATE's WHERE clause, checked first so a mismatch
// gets the right code: ERRAND_UNAVAILABLE when the status moved on,
// ERRAND_CHANGED when only the version or pending edit did.
func compareAndSwap(cur errand.Errand, t errand.Transition) error {
	if cur.Status != t.From {
		return appErr(apperror.ErrandUnavailable, "errand %q is %s, not %s", cur.ID, cur.Status, t.From)
	}
	curKey := ""
	if cur.PendingEdit != nil {
		curKey = cur.PendingEdit.Key
	}
	if cur.Version != t.ExpectedVersion || curKey != t.ExpectedPendingEditKey {
		return appErr(apperror.ErrandChanged, "errand %q has changed", cur.ID)
	}
	return nil
}

// editTargetSupplier returns the supplier an EventEdit transition moves cur
// to, applied at once or stored as a pending edit, and false when t is not
// an edit or keeps the supplier ("Supplier-changing edits").
func editTargetSupplier(cur errand.Errand, t errand.Transition) (string, bool) {
	if t.Event != errand.EventEdit {
		return "", false
	}
	var to string
	switch c := t.Changes; {
	case c.Details != nil:
		to = c.Details.SupplierID
	case c.PendingEdit.Value != nil:
		to = c.PendingEdit.Value.Details.SupplierID
	}
	return to, to != "" && to != cur.Details.SupplierID
}

// checkFence takes the shared supplier lock and refuses a supplier with an
// OPEN or COMMITTED fence. The lock is held until the transaction ends, so a
// Fence Acquire either waits for this transaction or is seen by it.
func checkFence(ctx context.Context, q *errandgen.Queries, id pgtype.UUID, supplierID string) error {
	if err := q.LockSupplierShared(ctx, id); err != nil {
		return err
	}
	fenced, err := q.SupplierFenced(ctx, id)
	if err != nil {
		return err
	}
	if fenced {
		return appErr(apperror.SupplierDeletionInProgress, "supplier %q is being deleted", supplierID)
	}
	return nil
}

// writeTransitionRows records t, opens or resolves the errand's review, and
// inserts t's outbox rows. before is the errand as Next saw it and after as
// stored.
func writeTransitionRows(ctx context.Context, q *errandgen.Queries, before, after errand.Errand, t errand.Transition) error {
	id, err := parseUUID("errandId", after.ID)
	if err != nil {
		return err
	}
	err = q.InsertTransition(ctx, errandgen.InsertTransitionParams{
		ErrandID:   id,
		FromStatus: text(string(t.From)),
		ToStatus:   string(t.To),
		Event:      string(t.Event),
		ActorType:  string(t.Actor.Type),
		ActorID:    text(t.Actor.ID),
		// The courier before the transition, or after it when there was
		// none before (an accept).
		CourierID:     text(cmp.Or(before.CourierID, after.CourierID)),
		Reason:        text(t.Reason),
		ErrandVersion: int32(after.Version),
		At:            timestamp(t.At),
	})
	if err != nil {
		return err
	}

	switch {
	case t.To == errand.StatusReviewRequired:
		reporter := t.Actor.ID
		if t.Actor.Type == errand.ActorSystem {
			reporter = ""
		}
		note := t.Reason
		if strings.TrimSpace(note) == "" {
			note = ""
		}
		err := q.OpenReview(ctx, errandgen.OpenReviewParams{
			ErrandID:   id,
			Reason:     string(after.ReviewReason),
			ReporterID: text(reporter),
			Note:       text(note),
			CreatedAt:  timestamp(t.At),
		})
		if err != nil {
			return err
		}
	case t.From == errand.StatusReviewRequired:
		resolved, err := q.ResolveReview(ctx, errandgen.ResolveReviewParams{
			Resolution:       text(string(t.To)),
			ResolverID:       text(t.Actor.ID),
			ResolutionReason: text(t.Reason),
			ResolvedAt:       timestamp(t.At),
			ErrandID:         id,
		})
		if err != nil {
			return err
		}
		if resolved == 0 {
			return fmt.Errorf("errandrepo: errand %q has no open review to resolve", after.ID)
		}
	}

	for _, row := range t.Outbox {
		payload, err := json.Marshal(row.Payload)
		if err != nil {
			return fmt.Errorf("errandrepo: encode %s payload: %w", row.Kind, err)
		}
		rowErrand, err := parseUUID("errandId", row.ErrandID)
		if err != nil {
			return err
		}
		err = q.InsertOutbox(ctx, errandgen.InsertOutboxParams{
			Kind:           row.Kind,
			IdempotencyKey: row.IdempotencyKey,
			ErrandID:       rowErrand,
			Payload:        payload,
		})
		if err != nil {
			return fmt.Errorf("errandrepo: insert outbox row %q: %w", row.IdempotencyKey, err)
		}
	}
	return nil
}
