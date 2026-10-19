package opsrepo

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Transition is one errand_transitions row. Empty strings stand for NULL
// columns: FromStatus for the creation transition, ActorID for SYSTEM.
type Transition struct {
	ID            int64
	ErrandID      string
	FromStatus    errand.Status
	ToStatus      errand.Status
	Event         errand.Event
	ActorType     errand.ActorType
	ActorID       string
	CourierID     string
	Reason        string
	ErrandVersion int
	At            time.Time
}

// TransitionCursor is the position after the last row of a page.
type TransitionCursor struct {
	At time.Time
	ID int64
}

// TransitionFilter selects transitions for the admin event log (F6.2.2).
// Zero fields match everything; the filters combine with AND.
type TransitionFilter struct {
	ErrandID string
	// UserID keeps transitions of errands the user requested, and those the
	// user caused or was the errand's courier at.
	UserID   string
	ToStatus errand.Status
	From     time.Time // inclusive
	To       time.Time // exclusive
	Before   *TransitionCursor
	Limit    int
}

// ListTransitions returns matching transitions newest first by (at, id).
// Each filter has its own query with one indexed access path; the most
// selective filter given picks it (errand, then user, then type, then the
// time index), and the others are applied to its rows.
func (r *Repository) ListTransitions(ctx context.Context, filter TransitionFilter) ([]Transition, error) {
	if err := checkLimit(filter.Limit); err != nil {
		return nil, err
	}
	if filter.ToStatus != "" && !filter.ToStatus.Valid() {
		return nil, invalid("%q is not an errand status", filter.ToStatus)
	}
	from, to := lowerBound(filter.From), upperBound(filter.To)
	cursorAt, cursorID := upperBound(time.Time{}), int64(math.MaxInt64)
	if filter.Before != nil {
		cursorAt, cursorID = timestamp(filter.Before.At), filter.Before.ID
	}
	toStatus := text(string(filter.ToStatus))
	limit := int32(filter.Limit)

	var (
		rows []opsgen.ErrandTransition
		err  error
	)
	switch {
	case filter.ErrandID != "":
		var errandID pgtype.UUID
		if errandID, err = parseUUID("errandId", filter.ErrandID); err != nil {
			return nil, err
		}
		rows, err = r.queries.ListTransitionsByErrand(ctx, opsgen.ListTransitionsByErrandParams{
			ErrandID: errandID, FromAt: from, ToAt: to, CursorAt: cursorAt, CursorID: cursorID,
			ToStatus: toStatus, UserID: text(filter.UserID), RowLimit: limit,
		})
	case filter.UserID != "":
		rows, err = r.queries.ListTransitionsByUser(ctx, opsgen.ListTransitionsByUserParams{
			UserID: filter.UserID, FromAt: from, ToAt: to, CursorAt: cursorAt, CursorID: cursorID,
			ToStatus: toStatus, RowLimit: limit,
		})
	case filter.ToStatus != "":
		rows, err = r.queries.ListTransitionsByType(ctx, opsgen.ListTransitionsByTypeParams{
			ToStatus: string(filter.ToStatus), FromAt: from, ToAt: to, CursorAt: cursorAt, CursorID: cursorID,
			RowLimit: limit,
		})
	default:
		rows, err = r.queries.ListTransitionsByTime(ctx, opsgen.ListTransitionsByTimeParams{
			FromAt: from, ToAt: to, CursorAt: cursorAt, CursorID: cursorID, RowLimit: limit,
		})
	}
	if err != nil {
		return nil, mapError(err)
	}
	transitions := make([]Transition, 0, len(rows))
	for _, row := range rows {
		transitions = append(transitions, Transition{
			ID:            row.ID,
			ErrandID:      uuidString(row.ErrandID),
			FromStatus:    errand.Status(row.FromStatus.String),
			ToStatus:      errand.Status(row.ToStatus),
			Event:         errand.Event(row.Event),
			ActorType:     errand.ActorType(row.ActorType),
			ActorID:       row.ActorID.String,
			CourierID:     row.CourierID.String,
			Reason:        row.Reason.String,
			ErrandVersion: int(row.ErrandVersion),
			At:            timeOrZero(row.At),
		})
	}
	return transitions, nil
}

// lowerBound and upperBound turn an unset time into -infinity or infinity,
// so the bound is always an index condition.
func lowerBound(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{InfinityModifier: pgtype.NegativeInfinity, Valid: true}
	}
	return timestamp(value)
}

func upperBound(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}
	}
	return timestamp(value)
}
