package supplierrepo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/readiness"
)

func TestReadinessRequiresSchemaConnectivityAndSeedProvenance(t *testing.T) {
	repository, pool := migratedRepository(t)
	checker := readiness.NewPostgresChecker(pool, "template-v1")

	_, err := pool.Exec(context.Background(), `
		ALTER TABLE supplier_seed_provenance RENAME TO supplier_seed_provenance_missing
	`)
	require.NoError(t, err)
	err = checker.Check(context.Background())
	require.ErrorContains(t, err, "required database schema is missing")
	_, err = pool.Exec(context.Background(), `
		ALTER TABLE supplier_seed_provenance_missing RENAME TO supplier_seed_provenance
	`)
	require.NoError(t, err)

	err = checker.Check(context.Background())
	require.ErrorContains(t, err, "required seed dataset is missing")

	created, err := repository.Create(context.Background(), validDetails("Readiness Seed"), time.Now().UTC())
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO supplier_seed_provenance (dataset_namespace, source_key, supplier_id)
		VALUES ('template-v1', 'readiness seed', $1)
	`, created.SupplierID)
	require.NoError(t, err)
	require.NoError(t, checker.Check(context.Background()))
}
