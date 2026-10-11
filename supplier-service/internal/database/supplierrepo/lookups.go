package supplierrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	db "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/generated"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// lookupAttempts bounds find-or-create when a concurrent writer keeps
// inserting and retiring rows with the same key between our statements.
const lookupAttempts = 3

// LookupQueries is the subset of generated queries used to resolve building
// and supplier-type lookups. Pass transaction-scoped queries so a created
// lookup commits or rolls back with the version that references it.
type LookupQueries interface {
	ResolveBuildingByKey(context.Context, string) (db.ResolveBuildingByKeyRow, error)
	InsertBuilding(context.Context, db.InsertBuildingParams) (db.InsertBuildingRow, error)
	ResolveSupplierTypeByKey(context.Context, string) (db.ResolveSupplierTypeByKeyRow, error)
	InsertSupplierType(context.Context, db.InsertSupplierTypeParams) (db.InsertSupplierTypeRow, error)
}

// FindOrCreateLookups sets details.Building/BuildingID and Type/TypeID to the
// live lookup rows whose keys match supplier.LookupKey of the given names,
// creating a row when none is live. The returned names are canonical, so
// "com2" resolves to an existing "Com 2". A field whose ID is already set is
// left unchanged; Update relies on this to keep an unpatched, possibly
// retired, lookup.
//
// The seed importer uses find-or-create permanently because its CSV is
// trusted bootstrap data. Administrator writes use it only until they are
// restricted to existing lookups.
func FindOrCreateLookups(ctx context.Context, queries LookupQueries, details supplier.Details, now time.Time) (supplier.Details, error) {
	if details.BuildingID == "" {
		id, name, err := findOrCreate(ctx, details.Building, now,
			func(ctx context.Context, name string) (string, string, error) {
				row, err := queries.ResolveBuildingByKey(ctx, name)
				return row.ID, row.Name, err
			},
			func(ctx context.Context, name string, now time.Time) (string, string, error) {
				row, err := queries.InsertBuilding(ctx, db.InsertBuildingParams{Name: name, Now: timestamp(now)})
				return row.ID, row.Name, err
			},
		)
		if err != nil {
			return supplier.Details{}, fmt.Errorf("resolve building: %w", err)
		}
		details.BuildingID, details.Building = id, name
	}
	if details.TypeID == "" {
		id, name, err := findOrCreate(ctx, details.Type, now,
			func(ctx context.Context, name string) (string, string, error) {
				row, err := queries.ResolveSupplierTypeByKey(ctx, name)
				return row.ID, row.Name, err
			},
			func(ctx context.Context, name string, now time.Time) (string, string, error) {
				row, err := queries.InsertSupplierType(ctx, db.InsertSupplierTypeParams{Name: name, Now: timestamp(now)})
				return row.ID, row.Name, err
			},
		)
		if err != nil {
			return supplier.Details{}, fmt.Errorf("resolve supplier type: %w", err)
		}
		details.TypeID, details.Type = id, name
	}
	return details, nil
}

// findOrCreate resolves, inserts on a miss, and resolves again when the
// insert lost a unique-key race. The insert's ON CONFLICT DO NOTHING waits
// for a concurrent inserter to finish, so under READ COMMITTED the next
// resolve sees its committed row.
func findOrCreate(
	ctx context.Context,
	name string,
	now time.Time,
	resolve func(context.Context, string) (string, string, error),
	insert func(context.Context, string, time.Time) (string, string, error),
) (string, string, error) {
	name = strings.TrimSpace(name)
	for range lookupAttempts {
		id, canonical, err := resolve(ctx, name)
		if err == nil {
			return id, canonical, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
		id, canonical, err = insert(ctx, name, now)
		if err == nil {
			return id, canonical, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", mapWriteError(err)
		}
	}
	return "", "", errors.New("lookup row changed concurrently")
}
