package review

import (
	"context"
	"fmt"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// EventLog is what the event-log route needs (F6.2.1, F6.2.2).
type EventLog interface {
	List(ctx context.Context, query EventQuery) (EventPage, error)
}

// TransitionLister reads errand_transitions; *opsrepo.Repository implements
// it.
type TransitionLister interface {
	ListTransitions(ctx context.Context, filter opsrepo.TransitionFilter) ([]opsrepo.Transition, error)
}

// EventQuery selects transitions. Zero fields match everything; filters
// combine with AND. From is inclusive and To exclusive. Before is the last
// row of the previous page.
type EventQuery struct {
	UserID   string
	ErrandID string
	Type     errand.Status
	From     time.Time
	To       time.Time
	Before   *opsrepo.TransitionCursor
	Limit    int
}

// EventPage is one page of transitions, newest first. Next is nil on the
// last page.
type EventPage struct {
	Items []opsrepo.Transition
	Next  *opsrepo.TransitionCursor
}

// EventService implements EventLog over errand_transitions.
type EventService struct {
	transitions TransitionLister
}

var _ EventLog = (*EventService)(nil)

func NewEventService(transitions TransitionLister) *EventService {
	if transitions == nil {
		panic("review: transition lister is required")
	}
	return &EventService{transitions: transitions}
}

func (s *EventService) List(ctx context.Context, query EventQuery) (EventPage, error) {
	if query.Limit < 1 || query.Limit > MaxPageSize {
		return EventPage{}, &apperror.Error{Code: apperror.InvalidArgument, Message: fmt.Sprintf("limit must be between 1 and %d", MaxPageSize)}
	}
	// One row more than the page shows whether another page exists.
	rows, err := s.transitions.ListTransitions(ctx, opsrepo.TransitionFilter{
		ErrandID: query.ErrandID,
		UserID:   query.UserID,
		ToStatus: query.Type,
		From:     query.From,
		To:       query.To,
		Before:   query.Before,
		Limit:    query.Limit + 1,
	})
	if err != nil {
		return EventPage{}, fmt.Errorf("list transitions: %w", err)
	}
	page := EventPage{Items: rows}
	if len(rows) > query.Limit {
		page.Items = rows[:query.Limit]
		last := page.Items[query.Limit-1]
		page.Next = &opsrepo.TransitionCursor{At: last.At, ID: last.ID}
	}
	return page, nil
}
