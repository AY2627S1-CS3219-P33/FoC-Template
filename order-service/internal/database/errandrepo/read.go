package errandrepo

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/errandgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

func (s *Store) Get(ctx context.Context, id string) (errand.Errand, error) {
	uuid, ok := toUUID(id)
	if !ok {
		return errand.Errand{}, notFound(id)
	}
	row, err := s.queries.GetErrand(ctx, uuid)
	if errors.Is(err, pgx.ErrNoRows) {
		return errand.Errand{}, notFound(id)
	}
	if err != nil {
		return errand.Errand{}, err
	}
	return errandFromRow(row)
}

// ListOpen treats a supplier filter that is not a UUID as matching nothing.
func (s *Store) ListOpen(ctx context.Context, q errand.OpenQuery) (errand.Page[errand.OpenCursor], error) {
	if err := checkLimit(q.Limit); err != nil {
		return errand.Page[errand.OpenCursor]{}, err
	}
	params := errandgen.ListOpenParams{Now: timestamp(q.Now), RowLimit: rowLimit(q.Limit + 1)}
	if q.SupplierID != "" {
		id, ok := toUUID(q.SupplierID)
		if !ok {
			return errand.Page[errand.OpenCursor]{Items: []errand.Errand{}}, nil
		}
		params.SupplierID = id
	}
	if q.After != nil {
		id, err := parseUUID("cursor", q.After.ID)
		if err != nil {
			return errand.Page[errand.OpenCursor]{}, err
		}
		params.AfterDeadline, params.AfterID = timestamp(q.After.AcceptanceDeadline), id
	}
	rows, err := s.queries.ListOpen(ctx, params)
	if err != nil {
		return errand.Page[errand.OpenCursor]{}, err
	}
	return page(rows, q.Limit, func(e errand.Errand) errand.OpenCursor {
		return errand.OpenCursor{AcceptanceDeadline: e.Details.AcceptanceDeadline, ID: e.ID}
	})
}

func (s *Store) ListRequested(ctx context.Context, q errand.UserListQuery) (errand.Page[errand.UserListCursor], error) {
	return listUser(q, func(e errand.Errand) time.Time { return e.CreatedAt },
		func(at pgtype.Timestamptz, id pgtype.UUID) ([]errandgen.Errand, error) {
			return s.queries.ListRequested(ctx, errandgen.ListRequestedParams{
				UserID: q.UserID, Statuses: activeStatuses(),
				AfterAt: at, AfterID: id, RowLimit: rowLimit(q.Limit + 1),
			})
		})
}

func (s *Store) ListDelivering(ctx context.Context, q errand.UserListQuery) (errand.Page[errand.UserListCursor], error) {
	return listUser(q, func(e errand.Errand) time.Time { return e.UpdatedAt },
		func(at pgtype.Timestamptz, id pgtype.UUID) ([]errandgen.Errand, error) {
			return s.queries.ListDelivering(ctx, errandgen.ListDeliveringParams{
				UserID: pgtype.Text{String: q.UserID, Valid: true}, Statuses: activeStatuses(),
				AfterAt: at, AfterID: id, RowLimit: rowLimit(q.Limit + 1),
			})
		})
}

func (s *Store) ListHistory(ctx context.Context, q errand.UserListQuery) (errand.Page[errand.UserListCursor], error) {
	return listUser(q, func(e errand.Errand) time.Time { return e.UpdatedAt },
		func(at pgtype.Timestamptz, id pgtype.UUID) ([]errandgen.Errand, error) {
			return s.queries.ListHistory(ctx, errandgen.ListHistoryParams{
				UserID: q.UserID, Statuses: terminalStatuses(),
				AfterAt: at, AfterID: id, RowLimit: rowLimit(q.Limit + 1),
			})
		})
}

// CourierHistory finds no withdrawal from an errand ID that is not a UUID.
func (s *Store) CourierHistory(ctx context.Context, errandID, courierID string, since time.Time) (errand.CourierHistory, error) {
	id, _ := toUUID(errandID)
	row, err := s.queries.CourierHistory(ctx, errandgen.CourierHistoryParams{
		ErrandID:  id,
		Since:     timestamp(since),
		CourierID: pgtype.Text{String: courierID, Valid: true},
	})
	if err != nil {
		return errand.CourierHistory{}, err
	}
	return errand.CourierHistory{
		WithdrewFromErrand: row.WithdrewFromErrand,
		WithdrawalsSince:   int(row.WithdrawalsSince),
	}, nil
}

// listUser runs one per-user list query with q's cursor and builds the page.
func listUser(q errand.UserListQuery, at func(errand.Errand) time.Time, list func(pgtype.Timestamptz, pgtype.UUID) ([]errandgen.Errand, error)) (errand.Page[errand.UserListCursor], error) {
	if err := checkLimit(q.Limit); err != nil {
		return errand.Page[errand.UserListCursor]{}, err
	}
	var afterAt pgtype.Timestamptz
	var afterID pgtype.UUID
	if q.After != nil {
		id, err := parseUUID("cursor", q.After.ID)
		if err != nil {
			return errand.Page[errand.UserListCursor]{}, err
		}
		afterAt, afterID = timestamp(q.After.At), id
	}
	rows, err := list(afterAt, afterID)
	if err != nil {
		return errand.Page[errand.UserListCursor]{}, err
	}
	return page(rows, q.Limit, func(e errand.Errand) errand.UserListCursor {
		return errand.UserListCursor{At: at(e), ID: e.ID}
	})
}

// page builds one page from up to limit+1 rows: the extra row only says that
// another page follows.
func page[C any](rows []errandgen.Errand, limit int, key func(errand.Errand) C) (errand.Page[C], error) {
	items, err := errandsFromRows(rows[:min(len(rows), limit)])
	if err != nil {
		return errand.Page[C]{}, err
	}
	p := errand.Page[C]{Items: items}
	if len(rows) > limit {
		next := key(items[limit-1])
		p.Next = &next
	}
	return p, nil
}
