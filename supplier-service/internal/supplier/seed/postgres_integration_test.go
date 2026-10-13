package seed

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/supplierrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/migrations"
)

func TestPostgresImportIsIdempotentAcrossReorderAndAdministratorChanges(t *testing.T) {
	pool := migratedSeedDatabase(t)
	store := NewPostgresStore(pool)
	repository := supplierrepo.New(pool)
	path := writeSeedCSV(t, initialSeedCSV)
	importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, store)
	require.NoError(t, err)
	seededAt := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	importer.now = func() time.Time { return seededAt }
	ctx := context.Background()

	first, err := importer.Import(ctx)
	require.NoError(t, err)
	require.Equal(t, Result{Rows: 4, Inserted: 4}, first)
	requireDatabaseCounts(t, pool, 4, 4, 4)

	var openingTime, closingTime string
	var imageURL *string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT to_char(v.opening_time, 'HH24:MI'),
		       to_char(v.closing_time, 'HH24:MI'),
		       v.image_url
		FROM supplier_seed_provenance AS p
		JOIN suppliers AS s ON s.supplier_id = p.supplier_id
		JOIN supplier_versions AS v ON v.version_id = s.current_version_id
		WHERE p.dataset_namespace = 'template-v1'
		  AND p.source_key = 'alpha cafe'
	`).Scan(&openingTime, &closingTime, &imageURL))
	require.Equal(t, "09:00", openingTime)
	require.Equal(t, "18:00", closingTime)
	require.Equal(t, stringPointer("https://example.com/alpha.png"), imageURL)

	repeated, err := importer.Import(ctx)
	require.NoError(t, err)
	require.Equal(t, Result{Rows: 4, Existing: 4}, repeated)
	requireDatabaseCounts(t, pool, 4, 4, 4)

	alphaID := provenanceSupplierID(t, pool, "alpha cafe")
	bravoID := provenanceSupplierID(t, pool, "bravo shop")
	charlieID := provenanceSupplierID(t, pool, "charlie printer")

	renamed := "Administrator Renamed Alpha"
	alpha, err := repository.Update(ctx, alphaID, supplier.Patch{Name: &renamed}, seededAt.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, renamed, alpha.Details.Name)

	// Administrators may only assign an existing building.
	updatedBuilding := "Administrator Building"
	_, err = repository.CreateLookup(ctx, supplier.BuildingLookup, updatedBuilding, seededAt.Add(2*time.Hour))
	require.NoError(t, err)
	bravo, err := repository.Update(ctx, bravoID, supplier.Patch{Building: &updatedBuilding}, seededAt.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, updatedBuilding, bravo.Details.Building)

	operationID := supplier.DeletionOperationID(seedTestUUID(t))
	_, err = repository.CreateOrGetDeletion(ctx, operationID, charlieID, 3, seededAt.Add(3*time.Hour))
	require.NoError(t, err)
	claimID := supplier.DeletionClaimID(seedTestUUID(t))
	_, claimed, err := repository.ClaimDeletion(
		ctx,
		operationID,
		claimID,
		seededAt.Add(3*time.Hour),
		seededAt.Add(4*time.Hour),
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repository.DeleteCurrentAndMarkCommitPending(
		ctx,
		charlieID,
		operationID,
		claimID,
		seededAt.Add(3*time.Hour+time.Minute),
	))
	require.NoError(t, repository.CompleteDeletion(
		ctx,
		operationID,
		claimID,
		seededAt.Add(3*time.Hour+2*time.Minute),
	))

	require.NoError(t, os.WriteFile(path, []byte(reorderedSeedCSV), 0o600))
	reordered, err := importer.Import(ctx)
	require.NoError(t, err)
	require.Equal(t, Result{Rows: 4, Existing: 4}, reordered)
	requireDatabaseCounts(t, pool, 4, 6, 4)

	alpha, err = repository.GetCurrentAvailable(ctx, alphaID)
	require.NoError(t, err)
	require.Equal(t, renamed, alpha.Details.Name, "repeat import must not overwrite an administrator rename")
	bravo, err = repository.GetCurrentAvailable(ctx, bravoID)
	require.NoError(t, err)
	require.Equal(t, updatedBuilding, bravo.Details.Building, "repeat import must not overwrite an administrator update")
	_, err = repository.GetCurrentAvailable(ctx, charlieID)
	requireApplicationErrorCode(t, err, apperror.SupplierNotFound)
	require.Equal(t, charlieID, provenanceSupplierID(t, pool, "charlie printer"), "deleted seed records retain provenance")
}

// The template CSV spells "Com 2"/"Com2" and "Prince George's Park" with a
// straight and a curly apostrophe; each pair must become one lookup row.
func TestPostgresImportOfTemplateCSVDeduplicatesLookups(t *testing.T) {
	pool := migratedSeedDatabase(t)
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "..", "data", "csv", "supplier-seed-data.csv")
	importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, NewPostgresStore(pool))
	require.NoError(t, err)
	ctx := context.Background()

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	records, err := parseCSV(bytes.NewReader(contents))
	require.NoError(t, err)
	buildingKeys, typeKeys := map[string]bool{}, map[string]bool{}
	for _, record := range records {
		buildingKeys[supplier.LookupKey(record.Details.Building)] = true
		typeKeys[supplier.LookupKey(record.Details.Type)] = true
	}
	require.True(t, buildingKeys["com2"] && buildingKeys["princegeorgespark"])

	for range 2 {
		_, err := importer.Import(ctx)
		require.NoError(t, err)
		requireDatabaseCounts(t, pool, len(records), len(records), len(records))
		require.Equal(t, len(buildingKeys), countLookups(t, pool, "buildings"), "one building per lookup key")
		require.Equal(t, len(typeKeys), countLookups(t, pool, "supplier_types"), "one type per lookup key")
	}

	// The first spelling in the CSV becomes the canonical name.
	for name, building := range map[string]string{
		"printer @ com 2": "Com 2",
		"cool spot":       "Com 2",
		"a hot hideout":   "Prince George's Park",
		"octobox":         "Prince George's Park",
		"supersnacks":     "Prince George's Park",
	} {
		current, err := supplierrepo.New(pool).GetCurrentAvailable(ctx, provenanceSupplierID(t, pool, name))
		require.NoError(t, err)
		require.Equal(t, building, current.Details.Building, name)
		require.NotEmpty(t, current.Details.BuildingID)
	}
}

func countLookups(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count))
	return count
}

func TestPostgresImportRollsBackTheWholeBatchOnPersistenceFailure(t *testing.T) {
	pool := migratedSeedDatabase(t)
	repository := supplierrepo.New(pool)
	for kind, name := range map[supplier.LookupKind]string{supplier.BuildingLookup: "COM 3", supplier.SupplierTypeLookup: "Food"} {
		_, err := repository.CreateLookup(context.Background(), kind, name, time.Now().UTC())
		require.NoError(t, err)
	}
	_, err := repository.Create(context.Background(), supplier.Details{
		Name:                "Conflicting Supplier",
		Type:                "Food",
		Building:            "COM 3",
		Floor:               "1",
		LocationDescription: "Atrium",
		Latitude:            1.294,
		Longitude:           103.773,
		OpeningTime:         "09:00",
		ClosingTime:         "18:00",
	}, time.Now().UTC())
	require.NoError(t, err)

	path := writeSeedCSV(t, `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime
Would Otherwise Insert,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs
Conflicting Supplier,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs
`)
	importer, err := New(Config{CSVPath: path, DatasetNamespace: "template-v1"}, NewPostgresStore(pool))
	require.NoError(t, err)

	_, err = importer.Import(context.Background())
	require.ErrorContains(t, err, "seed rows could not be stored")
	requireDatabaseCounts(t, pool, 1, 1, 0)
}

const initialSeedCSV = `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime,ImageURL
Alpha Cafe,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs,https://example.com/alpha.png
Bravo Shop,Shopping,Central Library,2,Lobby,1.296,103.772,1000hrs,1900hrs,
Charlie Printer,Printing,COM 2,1,Next to LT19,1.293,103.774,0000hrs,2359hrs,
Delta Cafe,Food,The Ridge,1,Near COM2,1.295,103.771,0800hrs,2100hrs,
`

const reorderedSeedCSV = `Name,Type,Building,Floor,Location Description,Latitude,Longitude,StartingTime,ClosingTime,ImageURL
Delta Cafe,Food,The Ridge,1,Near COM2,1.295,103.771,0800hrs,2100hrs,
Charlie Printer,Printing,COM 2,1,Next to LT19,1.293,103.774,0000hrs,2359hrs,
Alpha Cafe,Food,COM 3,1,Atrium,1.294,103.773,0900hrs,1800hrs,https://example.com/alpha.png
Bravo Shop,Shopping,Central Library,2,Lobby,1.296,103.772,1000hrs,1900hrs,
`

func provenanceSupplierID(t *testing.T, pool *pgxpool.Pool, sourceKey string) supplier.SupplierID {
	t.Helper()
	var supplierID string
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT supplier_id::text
		FROM supplier_seed_provenance
		WHERE dataset_namespace = 'template-v1'
		  AND source_key = $1
	`, sourceKey).Scan(&supplierID))
	return supplier.SupplierID(supplierID)
}

func requireDatabaseCounts(t *testing.T, pool *pgxpool.Pool, suppliers, versions, provenance int) {
	t.Helper()
	ctx := context.Background()
	for relation, expected := range map[string]int{
		"suppliers":                suppliers,
		"supplier_versions":        versions,
		"supplier_seed_provenance": provenance,
	} {
		var actual int
		require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM "+relation).Scan(&actual))
		require.Equalf(t, expected, actual, "unexpected %s count", relation)
	}
}

func requireApplicationErrorCode(t *testing.T, err error, code apperror.Code) {
	t.Helper()
	require.Error(t, err)
	var applicationError *apperror.Error
	require.ErrorAs(t, err, &applicationError)
	require.Equal(t, code, applicationError.Code)
}

func migratedSeedDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL seed integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := "supplier_seed_" + strings.ReplaceAll(seedTestUUID(t), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)

	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err)
	config.MaxConns = 12
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close(cleanupCtx)
	})

	upSQL, err := migrations.Up()
	require.NoError(t, err)
	_, err = pool.Exec(ctx, upSQL)
	require.NoError(t, err)

	return pool
}

func seedTestUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	_, err := rand.Read(value)
	require.NoError(t, err)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
