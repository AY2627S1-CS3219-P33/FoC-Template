package supplierrepo

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func TestWritesResolveSpellingVariantsToOneCanonicalLookup(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	first := validDetails("Printer @ Com 2")
	first.Building, first.Type = "Com 2", "Printing"
	// Lookup IDs on input are ignored.
	first.BuildingID, first.TypeID = testUUID(t), testUUID(t)
	created, err := repository.Create(ctx, first, now)
	require.NoError(t, err)
	require.Equal(t, "Com 2", created.Details.Building)
	require.Equal(t, "Printing", created.Details.Type)
	require.NotEqual(t, first.BuildingID, created.Details.BuildingID)

	second := validDetails("Cool Spot")
	second.Building, second.Type = "  com2 ", "PRINTING"
	other, err := repository.Create(ctx, second, now)
	require.NoError(t, err)
	require.Equal(t, "Com 2", other.Details.Building, "the response carries the canonical name")
	require.Equal(t, "Printing", other.Details.Type)
	require.Equal(t, created.Details.BuildingID, other.Details.BuildingID)
	require.Equal(t, created.Details.TypeID, other.Details.TypeID)

	variant := "COM-2"
	updated, err := repository.Update(ctx, other.SupplierID, supplier.Patch{Building: &variant}, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "Com 2", updated.Details.Building)
	require.Equal(t, created.Details.BuildingID, updated.Details.BuildingID)

	require.Equal(t, 1, countRows(t, pool, "buildings"))
	require.Equal(t, 1, countRows(t, pool, "supplier_types"))

	for _, read := range []supplier.Details{
		mustGetCurrent(t, repository, other.SupplierID).Details,
		mustGetVersion(t, repository, other.VersionID).Details,
		mustGetVersion(t, repository, updated.VersionID).Details,
	} {
		require.Equal(t, "Com 2", read.Building)
		require.Equal(t, created.Details.BuildingID, read.BuildingID)
		require.Equal(t, "Printing", read.Type)
		require.Equal(t, created.Details.TypeID, read.TypeID)
	}

	page, err := repository.ListAvailable(ctx, supplier.ListFilter{Limit: supplier.MaximumPageSize})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	for _, item := range page.Items {
		require.Equal(t, "Com 2", item.Details.Building)
		require.Equal(t, created.Details.BuildingID, item.Details.BuildingID)
		require.Equal(t, created.Details.TypeID, item.Details.TypeID)
	}
}

func TestPatchKeepsAnUnpatchedRetiredLookupAndHistoryStillResolves(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	details := validDetails("Retired Building Supplier")
	details.Building = "Old Hall"
	created, err := repository.Create(ctx, details, now)
	require.NoError(t, err)
	retire(t, pool, "buildings", created.Details.BuildingID)

	floor := "2"
	kept, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Floor: &floor}, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, created.Details.BuildingID, kept.Details.BuildingID, "an unpatched building is not re-resolved")
	require.Equal(t, "Old Hall", kept.Details.Building)
	require.Equal(t, "Old Hall", mustGetCurrent(t, repository, created.SupplierID).Details.Building)

	// TRANSITIONAL find-or-create: naming the retired building again creates
	// a new live row with the same key.
	building := "old hall"
	replaced, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Building: &building}, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.NotEqual(t, created.Details.BuildingID, replaced.Details.BuildingID)
	require.Equal(t, "old hall", replaced.Details.Building)

	original := mustGetVersion(t, repository, created.VersionID)
	require.Equal(t, created.Details, original.Details, "history keeps the retired lookup")
	require.False(t, original.Available)
}

func TestTypeFilterAndSortUseTheLookupKey(t *testing.T) {
	repository, _ := migratedRepository(t)
	ctx := context.Background()
	for name, supplierType := range map[string]string{
		"Cafe":  "Food/Coffee",
		"Deli":  "food coffee",
		"Shop":  "Shopping",
		"Bakes": "Food",
	} {
		details := validDetails(name)
		details.Type = supplierType
		_, err := repository.Create(ctx, details, time.Now().UTC())
		require.NoError(t, err)
	}

	for _, filter := range []string{"Food/Coffee", "FOODCOFFEE", " food-coffee "} {
		page, err := repository.ListAvailable(ctx, supplier.ListFilter{Type: filter, Limit: 1})
		require.NoError(t, err)
		require.Equal(t, []string{"Cafe"}, supplierNames(page.Items), "type %q", filter)
		require.Equal(t, "Food/Coffee", page.Items[0].Details.Type)
		// Filters with the same lookup key share cursors.
		next, err := repository.ListAvailable(ctx, supplier.ListFilter{Type: "food coffee", Limit: 1, Cursor: page.NextCursor})
		require.NoError(t, err)
		require.Equal(t, []string{"Deli"}, supplierNames(next.Items))
		require.Equal(t, "Food/Coffee", next.Items[0].Details.Type)
	}

	page, err := repository.ListAvailable(ctx, supplier.ListFilter{Sort: supplier.SortType, Limit: supplier.MaximumPageSize})
	require.NoError(t, err)
	require.Equal(t, []string{"Bakes", "Cafe", "Deli", "Shop"}, supplierNames(page.Items))

	page, err = repository.ListAvailable(ctx, supplier.ListFilter{Query: "com 3", Limit: supplier.MaximumPageSize})
	require.NoError(t, err)
	require.Len(t, page.Items, 4, "q still matches the building name")
}

func TestConcurrentWritesCreateOneLookupPerKey(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	const writers = 8
	spellings := []string{"University Town", "UTown", "university-town", "UNIVERSITY TOWN"}

	start := make(chan struct{})
	results := make(chan supplier.Supplier, writers)
	errs := make(chan error, writers)
	var wait sync.WaitGroup
	for i := range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			details := validDetails(fmt.Sprintf("Concurrent Lookup %d", i))
			details.Building = spellings[i%len(spellings)]
			details.Type = fmt.Sprintf("Type %c", 'A'+i%2)
			<-start
			created, err := repository.Create(ctx, details, time.Now().UTC())
			if err != nil {
				errs <- err
				return
			}
			results <- created
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	buildingIDs := map[string]bool{}
	for created := range results {
		buildingIDs[created.Details.BuildingID] = true
	}
	// "UTown" has a different key from "University Town".
	require.Len(t, buildingIDs, 2)
	require.Equal(t, 2, countRows(t, pool, "buildings"))
	require.Equal(t, 2, countRows(t, pool, "supplier_types"))
}

func TestWritesRejectBuildingAndTypeWithoutALookupKey(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	details := validDetails("Punctuation Supplier")
	details.Building, details.Type = "---", "//"

	_, err := repository.Create(ctx, details, time.Now().UTC())
	validation := &supplier.ValidationError{}
	require.ErrorAs(t, err, &validation)
	require.ElementsMatch(t, []supplier.FieldViolation{
		{Field: "building", Message: "must contain a letter or digit"},
		{Field: "type", Message: "must contain a letter or digit"},
	}, validation.Violations)
	require.Equal(t, 0, countRows(t, pool, "buildings"))
}

func mustGetCurrent(t *testing.T, repository *Repository, id supplier.SupplierID) supplier.Supplier {
	t.Helper()
	current, err := repository.GetCurrentAvailable(context.Background(), id)
	require.NoError(t, err)
	return current
}

func mustGetVersion(t *testing.T, repository *Repository, id supplier.VersionID) supplier.Version {
	t.Helper()
	version, err := repository.GetVersion(context.Background(), id)
	require.NoError(t, err)
	return version
}

func retire(t *testing.T, pool *pgxpool.Pool, table, id string) {
	t.Helper()
	// GREATEST guards against a container clock stepping backwards.
	_, err := pool.Exec(context.Background(),
		`UPDATE `+table+` SET retired_at = GREATEST(clock_timestamp(), created_at) WHERE id = $1`, id)
	require.NoError(t, err)
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&count))
	return count
}
