package seed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/generated"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// lookupAttempts bounds find-or-create when a concurrent writer keeps
// inserting and retiring rows with the same key between our statements.
const lookupAttempts = 3

type PostgresStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	if pool == nil {
		panic("seed: PostgreSQL pool is required")
	}
	return &PostgresStore{pool: pool, queries: db.New(pool)}
}

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Apply(
	ctx context.Context,
	datasetNamespace string,
	records []Record,
	seededAt time.Time,
) (ApplyResult, error) {
	if err := validateBatch(datasetNamespace, records); err != nil {
		return ApplyResult{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	queries := s.queries.WithTx(tx)

	// Serializing one namespace avoids check-then-insert races when multiple
	// service instances start against the same database simultaneously.
	if err := queries.LockSeedDataset(ctx, datasetNamespace); err != nil {
		return ApplyResult{}, err
	}

	result := ApplyResult{}
	for _, record := range records {
		exists, err := queries.SeedProvenanceExists(ctx, db.SeedProvenanceExistsParams{
			DatasetNamespace: datasetNamespace,
			SourceKey:        record.SourceKey,
		})
		if err != nil {
			return ApplyResult{}, err
		}
		if exists {
			result.Existing++
			continue
		}

		details := record.Details
		details.BuildingID, details.TypeID = "", ""
		details, err = findOrCreateLookups(ctx, queries, details, seededAt)
		if err != nil {
			return ApplyResult{}, err
		}
		identity, err := queries.InsertSupplierIdentity(ctx, db.InsertSupplierIdentityParams{
			NormalizedName: supplier.NormalizeName(record.Details.Name),
			Now:            seedTimestamp(seededAt),
		})
		if err != nil {
			return ApplyResult{}, err
		}
		if err := queries.InsertSupplierVersion(ctx, seedVersionParams(
			identity.VersionID,
			identity.SupplierID,
			details,
			seededAt,
		)); err != nil {
			return ApplyResult{}, err
		}
		if err := queries.InsertSeedProvenance(ctx, db.InsertSeedProvenanceParams{
			DatasetNamespace: datasetNamespace,
			SourceKey:        record.SourceKey,
			SupplierID:       identity.SupplierID,
			SeededAt:         seedTimestamp(seededAt),
		}); err != nil {
			return ApplyResult{}, err
		}
		result.Inserted++
	}

	if err := tx.Commit(ctx); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

func validateBatch(datasetNamespace string, records []Record) error {
	if strings.TrimSpace(datasetNamespace) == "" {
		return errors.New("seed dataset namespace is required")
	}
	seen := make(map[string]struct{}, len(records))
	for index, record := range records {
		if record.SourceKey == "" || record.SourceKey != supplier.NormalizeName(record.Details.Name) {
			return fmt.Errorf("seed record %d has an invalid source key", index+1)
		}
		if _, duplicate := seen[record.SourceKey]; duplicate {
			return fmt.Errorf("seed record %d has a duplicate source key", index+1)
		}
		seen[record.SourceKey] = struct{}{}
		if err := supplier.ValidateDetails(record.Details); err != nil {
			return fmt.Errorf("seed record %d failed domain validation: %w", index+1, err)
		}
	}
	return nil
}

func seedVersionParams(versionID, supplierID string, details supplier.Details, seededAt time.Time) db.InsertSupplierVersionParams {
	imageURL := ""
	if details.ImageURL != nil {
		imageURL = *details.ImageURL
	}
	return db.InsertSupplierVersionParams{
		VersionID:           versionID,
		SupplierID:          supplierID,
		Name:                details.Name,
		SupplierTypeID:      details.TypeID,
		BuildingID:          details.BuildingID,
		Floor:               details.Floor,
		LocationDescription: details.LocationDescription,
		Latitude:            details.Latitude,
		Longitude:           details.Longitude,
		OpeningTime:         details.OpeningTime,
		ClosingTime:         details.ClosingTime,
		ImageUrl:            imageURL,
		Now:                 seedTimestamp(seededAt),
	}
}

func seedTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

// findOrCreateLookups sets details.Building/BuildingID and Type/TypeID to the
// live lookup rows whose keys match supplier.LookupKey of the given names,
// creating a row when none is live. Spelling variants such as "Com 2" and
// "Com2" therefore share one row, named by the first spelling seen.
//
// This is the only writer that creates lookups implicitly. Administrator
// writes must name an existing, unretired building and type, because the
// lists are curated through their own endpoints. The seed CSV is trusted
// bootstrap data that runs before any administrator can have created those
// rows, so it builds the initial lists itself. Resolution locks a row FOR
// SHARE, like administrator writes, so a concurrent retirement waits.
func findOrCreateLookups(ctx context.Context, queries *db.Queries, details supplier.Details, now time.Time) (supplier.Details, error) {
	id, name, err := findOrCreate(ctx, details.Building, now,
		func(ctx context.Context, name string) (string, string, error) {
			row, err := queries.ResolveBuildingByKey(ctx, name)
			return row.ID, row.Name, err
		},
		func(ctx context.Context, name string, now time.Time) (string, string, error) {
			row, err := queries.InsertBuilding(ctx, db.InsertBuildingParams{Name: name, Now: seedTimestamp(now)})
			return row.ID, row.Name, err
		},
	)
	if err != nil {
		return supplier.Details{}, fmt.Errorf("resolve building: %w", err)
	}
	details.BuildingID, details.Building = id, name

	id, name, err = findOrCreate(ctx, details.Type, now,
		func(ctx context.Context, name string) (string, string, error) {
			row, err := queries.ResolveSupplierTypeByKey(ctx, name)
			return row.ID, row.Name, err
		},
		func(ctx context.Context, name string, now time.Time) (string, string, error) {
			row, err := queries.InsertSupplierType(ctx, db.InsertSupplierTypeParams{Name: name, Now: seedTimestamp(now)})
			return row.ID, row.Name, err
		},
	)
	if err != nil {
		return supplier.Details{}, fmt.Errorf("resolve supplier type: %w", err)
	}
	details.TypeID, details.Type = id, name
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
			return "", "", err
		}
	}
	return "", "", errors.New("lookup row changed concurrently")
}
