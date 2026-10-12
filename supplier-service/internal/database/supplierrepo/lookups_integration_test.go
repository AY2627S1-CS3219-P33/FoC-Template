package supplierrepo

import (
	"context"
	"fmt"
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
	building := mustCreateLookup(t, repository, supplier.BuildingLookup, "Com 2")
	supplierType := mustCreateLookup(t, repository, supplier.SupplierTypeLookup, "Printing")

	first := validDetails("Printer @ Com 2")
	first.Building, first.Type = "Com 2", "Printing"
	// Lookup IDs on input are ignored.
	first.BuildingID, first.TypeID = testUUID(t), testUUID(t)
	created, err := repository.Create(ctx, first, now)
	require.NoError(t, err)
	require.Equal(t, "Com 2", created.Details.Building)
	require.Equal(t, "Printing", created.Details.Type)
	require.Equal(t, string(building.ID), created.Details.BuildingID)
	require.Equal(t, string(supplierType.ID), created.Details.TypeID)

	// Case, spacing, and punctuation variants resolve to the canonical row.
	for i, variant := range []struct{ building, supplierType string }{
		{"  com2 ", "PRINTING"},
		{"COM-2", " printing."},
		{"c.o.m 2", "Print-ing"},
	} {
		details := validDetails(fmt.Sprintf("Variant Supplier %d", i))
		details.Building, details.Type = variant.building, variant.supplierType
		other, err := repository.Create(ctx, details, now)
		require.NoError(t, err, "variant %+v", variant)
		require.Equal(t, "Com 2", other.Details.Building, "the response carries the canonical name")
		require.Equal(t, "Printing", other.Details.Type)
		require.Equal(t, created.Details.BuildingID, other.Details.BuildingID)
		require.Equal(t, created.Details.TypeID, other.Details.TypeID)
	}

	variant := "COM-2"
	updated, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Building: &variant}, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "Com 2", updated.Details.Building)
	require.Equal(t, created.Details.BuildingID, updated.Details.BuildingID)

	require.Equal(t, 1, countRows(t, pool, "buildings"), "writes never create lookups")
	require.Equal(t, 1, countRows(t, pool, "supplier_types"))

	for _, read := range []supplier.Details{
		mustGetCurrent(t, repository, created.SupplierID).Details,
		mustGetVersion(t, repository, created.VersionID).Details,
		mustGetVersion(t, repository, updated.VersionID).Details,
	} {
		require.Equal(t, "Com 2", read.Building)
		require.Equal(t, created.Details.BuildingID, read.BuildingID)
		require.Equal(t, "Printing", read.Type)
		require.Equal(t, created.Details.TypeID, read.TypeID)
	}

	page, err := repository.ListAvailable(ctx, supplier.ListFilter{Limit: supplier.MaximumPageSize})
	require.NoError(t, err)
	require.Len(t, page.Items, 4)
	for _, item := range page.Items {
		require.Equal(t, "Com 2", item.Details.Building)
		require.Equal(t, created.Details.BuildingID, item.Details.BuildingID)
		require.Equal(t, created.Details.TypeID, item.Details.TypeID)
	}
}

func TestWritesRejectUnknownBuildingAndTypeWithOtherViolations(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	createLookups(t, repository, validDetails(""))

	unknownBuilding := validDetails("Unknown Building Supplier")
	unknownBuilding.Building = "Nowhere Hall"
	_, err := repository.Create(ctx, unknownBuilding, now)
	requireViolations(t, err, supplier.FieldViolation{Field: "building", Message: "must be an existing building"})

	unknownType := validDetails("Unknown Type Supplier")
	unknownType.Type = "Laundry"
	_, err = repository.Create(ctx, unknownType, now)
	requireViolations(t, err, supplier.FieldViolation{Field: "type", Message: "must be an existing supplier type"})

	// Both lookups and unrelated fields are reported together.
	both := validDetails("Unknown Both Supplier")
	both.Building, both.Type, both.Latitude, both.ClosingTime = "Nowhere Hall", "Laundry", 91, both.OpeningTime
	_, err = repository.Create(ctx, both, now)
	requireViolations(t, err,
		supplier.FieldViolation{Field: "latitude", Message: "must be between -90 and 90"},
		supplier.FieldViolation{Field: "closingTime", Message: "must differ from openingTime"},
		supplier.FieldViolation{Field: "building", Message: "must be an existing building"},
		supplier.FieldViolation{Field: "type", Message: "must be an existing supplier type"},
	)

	created, err := repository.Create(ctx, validDetails("Known Supplier"), now)
	require.NoError(t, err)
	building, supplierType := "Nowhere Hall", "Laundry"
	opening := created.Details.ClosingTime
	_, err = repository.Update(ctx, created.SupplierID, supplier.Patch{
		Building: &building, Type: &supplierType, OpeningTime: &opening,
	}, now.Add(time.Minute))
	requireViolations(t, err,
		supplier.FieldViolation{Field: "closingTime", Message: "must differ from openingTime"},
		supplier.FieldViolation{Field: "building", Message: "must be an existing building"},
		supplier.FieldViolation{Field: "type", Message: "must be an existing supplier type"},
	)

	require.Equal(t, 1, countRows(t, pool, "buildings"), "a rejected write creates no lookup")
	require.Equal(t, 1, countRows(t, pool, "supplier_types"))
	require.Equal(t, 1, countRows(t, pool, "supplier_versions"))
}

func TestRetiredLookupCannotBeAssignedButAnUnpatchedOneIsKept(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	details := validDetails("Retired Building Supplier")
	details.Building, details.Type = "Old Hall", "Old Type"
	createLookups(t, repository, details, validDetails(""))
	created, err := repository.Create(ctx, details, now)
	require.NoError(t, err)
	// Retirement through the API is refused while the supplier uses the row,
	// so retire it directly to reach the state of a supplier left on one.
	retire(t, pool, "buildings", created.Details.BuildingID)
	retire(t, pool, "supplier_types", created.Details.TypeID)

	newcomer := validDetails("Newcomer Supplier")
	newcomer.Building = "old hall"
	_, err = repository.Create(ctx, newcomer, now)
	requireViolations(t, err, supplier.FieldViolation{Field: "building", Message: "must be an existing building"})

	floor := "2"
	kept, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Floor: &floor}, now.Add(time.Minute))
	require.NoError(t, err, "a supplier on a retired lookup stays editable")
	require.Equal(t, created.Details.BuildingID, kept.Details.BuildingID, "an unpatched building is not re-resolved")
	require.Equal(t, created.Details.TypeID, kept.Details.TypeID)
	require.Equal(t, "Old Hall", kept.Details.Building)
	require.Equal(t, "Old Hall", mustGetCurrent(t, repository, created.SupplierID).Details.Building)

	building, supplierType := "OLD HALL", "old type"
	_, err = repository.Update(ctx, created.SupplierID, supplier.Patch{Building: &building, Type: &supplierType}, now.Add(2*time.Minute))
	requireViolations(t, err,
		supplier.FieldViolation{Field: "building", Message: "must be an existing building"},
		supplier.FieldViolation{Field: "type", Message: "must be an existing supplier type"},
	)
	require.Equal(t, kept.VersionID, mustGetCurrent(t, repository, created.SupplierID).VersionID)

	// A replacement row with the same key is assignable.
	replacement := mustCreateLookup(t, repository, supplier.BuildingLookup, "Old Hall")
	replaced, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Building: &building}, now.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, string(replacement.ID), replaced.Details.BuildingID)
	require.Equal(t, "Old Hall", replaced.Details.Building)

	original := mustGetVersion(t, repository, created.VersionID)
	require.Equal(t, created.Details, original.Details, "history keeps the retired lookup")
	require.False(t, original.Available)
}

func TestTypeFilterAndSortUseTheLookupKey(t *testing.T) {
	repository, _ := migratedRepository(t)
	ctx := context.Background()
	// validDetails supplies the "food" type, which "Food" resolves to.
	createLookups(t, repository, validDetails(""))
	for _, name := range []string{"Food/Coffee", "Shopping"} {
		mustCreateLookup(t, repository, supplier.SupplierTypeLookup, name)
	}
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

func mustCreateLookup(t *testing.T, repository *Repository, kind supplier.LookupKind, name string) supplier.Lookup {
	t.Helper()
	created, err := repository.CreateLookup(context.Background(), kind, name, time.Now().UTC())
	require.NoError(t, err)
	return created
}

func requireViolations(t *testing.T, err error, expected ...supplier.FieldViolation) {
	t.Helper()
	validation := &supplier.ValidationError{}
	require.ErrorAs(t, err, &validation)
	require.ElementsMatch(t, expected, validation.Violations)
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
