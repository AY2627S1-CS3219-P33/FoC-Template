package supplierrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/generated"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

var _ supplier.LookupStore = (*Repository)(nil)

// lookupTable adapts the per-table generated queries of one lookup kind so
// the store logic is written once for buildings and supplier types.
type lookupTable struct {
	liveKeyIndex string
	list         func(context.Context, *db.Queries, bool, int32) ([]supplier.Lookup, error)
	create       func(context.Context, *db.Queries, string, pgtype.Timestamptz) (supplier.Lookup, error)
	findLive     func(context.Context, *db.Queries, string) (supplier.Lookup, error)
	lock         func(context.Context, *db.Queries, string) (supplier.Lookup, error)
	countUsers   func(context.Context, *db.Queries, string) (int32, error)
	retire       func(context.Context, *db.Queries, string, pgtype.Timestamptz) error
}

var lookupTables = map[supplier.LookupKind]lookupTable{
	supplier.BuildingLookup: {
		liveKeyIndex: "buildings_live_lookup_key_uidx",
		list: func(ctx context.Context, q *db.Queries, includeRetired bool, limit int32) ([]supplier.Lookup, error) {
			rows, err := q.ListBuildings(ctx, db.ListBuildingsParams{IncludeRetired: includeRetired, RowLimit: limit})
			lookups := make([]supplier.Lookup, len(rows))
			for i, row := range rows {
				lookups[i] = supplier.Lookup{ID: supplier.LookupID(row.ID), Name: row.Name, Retired: row.Retired}
			}
			return lookups, err
		},
		create: func(ctx context.Context, q *db.Queries, name string, now pgtype.Timestamptz) (supplier.Lookup, error) {
			row, err := q.CreateBuilding(ctx, db.CreateBuildingParams{Name: name, Now: now})
			return supplier.Lookup{ID: supplier.LookupID(row.ID), Name: row.Name}, err
		},
		findLive: func(ctx context.Context, q *db.Queries, name string) (supplier.Lookup, error) {
			row, err := q.FindLiveBuildingByKey(ctx, name)
			return supplier.Lookup{ID: supplier.LookupID(row.ID), Name: row.Name}, err
		},
		lock: func(ctx context.Context, q *db.Queries, id string) (supplier.Lookup, error) {
			row, err := q.LockBuilding(ctx, id)
			return supplier.Lookup{ID: supplier.LookupID(row.ID), Name: row.Name, Retired: row.Retired}, err
		},
		countUsers: func(ctx context.Context, q *db.Queries, id string) (int32, error) {
			return q.CountSuppliersUsingBuilding(ctx, id)
		},
		retire: func(ctx context.Context, q *db.Queries, id string, now pgtype.Timestamptz) error {
			return q.RetireBuilding(ctx, db.RetireBuildingParams{ID: id, Now: now})
		},
	},
	supplier.SupplierTypeLookup: {
		liveKeyIndex: "supplier_types_live_lookup_key_uidx",
		list: func(ctx context.Context, q *db.Queries, includeRetired bool, limit int32) ([]supplier.Lookup, error) {
			rows, err := q.ListSupplierTypes(ctx, db.ListSupplierTypesParams{IncludeRetired: includeRetired, RowLimit: limit})
			lookups := make([]supplier.Lookup, len(rows))
			for i, row := range rows {
				lookups[i] = supplier.Lookup{ID: supplier.LookupID(row.ID), Name: row.Name, Retired: row.Retired}
			}
			return lookups, err
		},
		create: func(ctx context.Context, q *db.Queries, name string, now pgtype.Timestamptz) (supplier.Lookup, error) {
			row, err := q.CreateSupplierType(ctx, db.CreateSupplierTypeParams{Name: name, Now: now})
			return supplier.Lookup{ID: supplier.LookupID(row.ID), Name: row.Name}, err
		},
		findLive: func(ctx context.Context, q *db.Queries, name string) (supplier.Lookup, error) {
			row, err := q.FindLiveSupplierTypeByKey(ctx, name)
			return supplier.Lookup{ID: supplier.LookupID(row.ID), Name: row.Name}, err
		},
		lock: func(ctx context.Context, q *db.Queries, id string) (supplier.Lookup, error) {
			row, err := q.LockSupplierType(ctx, id)
			return supplier.Lookup{ID: supplier.LookupID(row.ID), Name: row.Name, Retired: row.Retired}, err
		},
		countUsers: func(ctx context.Context, q *db.Queries, id string) (int32, error) {
			return q.CountSuppliersUsingSupplierType(ctx, id)
		},
		retire: func(ctx context.Context, q *db.Queries, id string, now pgtype.Timestamptz) error {
			return q.RetireSupplierType(ctx, db.RetireSupplierTypeParams{ID: id, Now: now})
		},
	},
}

func tableFor(kind supplier.LookupKind) (lookupTable, error) {
	table, found := lookupTables[kind]
	if !found {
		return lookupTable{}, fmt.Errorf("unknown lookup kind %q", kind)
	}
	return table, nil
}

func (r *Repository) ListLookups(ctx context.Context, kind supplier.LookupKind, includeRetired bool, limit int) (result []supplier.Lookup, err error) {
	defer r.observe("list_lookups", time.Now(), &err)
	table, err := tableFor(kind)
	if err != nil {
		return nil, err
	}
	rowLimit, err := boundedInt32(limit, "limit")
	if err != nil {
		return nil, err
	}
	return table.list(ctx, r.queries, includeRetired, rowLimit)
}

func (r *Repository) CreateLookup(ctx context.Context, kind supplier.LookupKind, name string, now time.Time) (result supplier.Lookup, err error) {
	defer r.observe("create_lookup", time.Now(), &err)
	table, err := tableFor(kind)
	if err != nil {
		return supplier.Lookup{}, err
	}
	created, err := table.create(ctx, r.queries, name, timestamp(now))
	if err == nil {
		return created, nil
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return supplier.Lookup{}, err
	}
	switch {
	case postgresError.Code == "23505" && postgresError.ConstraintName == table.liveKeyIndex:
		// The insert ran outside a transaction, so this read is not aborted.
		// The winning row may be retired meanwhile; the conflict still stands.
		existing, findErr := table.findLive(ctx, r.queries, name)
		if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
			return supplier.Lookup{}, findErr
		}
		return supplier.Lookup{}, &supplier.LookupConflictError{Existing: existing}
	case postgresError.Code == "23514":
		// Validation normally rejects these first.
		return supplier.Lookup{}, &supplier.ValidationError{Violations: []supplier.FieldViolation{
			{Field: "name", Message: "must contain a letter or digit"},
		}}
	}
	return supplier.Lookup{}, err
}

func (r *Repository) RetireLookup(ctx context.Context, kind supplier.LookupKind, id supplier.LookupID, now time.Time) (err error) {
	defer r.observe("retire_lookup", time.Now(), &err)
	table, err := tableFor(kind)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := r.queries.WithTx(tx)
	// The row lock precedes the use check. A supplier write that resolved the
	// row FOR SHARE has committed by the time the lock is granted, so the
	// check (a fresh READ COMMITTED snapshot) sees its version; a write that
	// resolves later waits for this commit and then sees the row retired.
	locked, err := table.lock(ctx, queries, string(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return supplier.ErrLookupNotFound
	}
	if err != nil {
		return err
	}
	if locked.Retired {
		return nil
	}
	users, err := table.countUsers(ctx, queries, string(id))
	if err != nil {
		return err
	}
	if users > 0 {
		return &supplier.LookupInUseError{Lookup: locked, Suppliers: int(users)}
	}
	if err := table.retire(ctx, queries, string(id), timestamp(now)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
