package seed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/generated"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/supplierrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

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

		// The trusted seed CSV always finds or creates its buildings and
		// types, so spelling variants such as "Com 2" and "Com2" share one
		// lookup row and the canonical name is stored once.
		details := record.Details
		details.BuildingID, details.TypeID = "", ""
		details, err = supplierrepo.FindOrCreateLookups(ctx, queries, details, seededAt)
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
