package supplierrepo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// F2.1.1/F2.1.2/NFR2.5: filter the current projection, then page in stable order.
func TestCatalogueSearchFiltersAndOrdering(t *testing.T) {
	r, _ := migratedRepository(t)
	ctx := context.Background()
	for _, name := range []string{"Zulu", " alpha   Cafe ", "Beta"} {
		details := validDetails(name)
		details.Type, details.Building, details.Floor, details.LocationDescription = " FOOD ", "COM\t  3", "Level   2", "Near  the\t library"
		if name == "Beta" {
			details.Type = "food truck"
		}
		createLookups(t, r, details)
		_, err := r.Create(ctx, details, time.Now().UTC())
		require.NoError(t, err)
	}
	for _, test := range []struct {
		filter supplier.ListFilter
		names  []string
	}{
		{supplier.ListFilter{}, []string{" alpha   Cafe ", "Beta", "Zulu"}},
		{supplier.ListFilter{Query: " ALPHA\t cafe "}, []string{" alpha   Cafe "}},
		{supplier.ListFilter{Query: " COM\n 3 "}, []string{" alpha   Cafe ", "Beta", "Zulu"}},
		{supplier.ListFilter{Query: " LEVEL\t 2 "}, []string{" alpha   Cafe ", "Beta", "Zulu"}},
		{supplier.ListFilter{Query: " near   THE library "}, []string{" alpha   Cafe ", "Beta", "Zulu"}},
		{supplier.ListFilter{Type: "FoOd"}, []string{" alpha   Cafe ", "Zulu"}},
		{supplier.ListFilter{Query: "com 3", Type: " FOOD TRUCK "}, []string{"Beta"}},
		{supplier.ListFilter{Query: "%"}, []string{}},
		{supplier.ListFilter{Query: "_"}, []string{}},
	} {
		page, err := r.ListAvailable(ctx, test.filter)
		require.NoError(t, err)
		names := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			names = append(names, item.Details.Name)
			require.True(t, item.Available)
			require.True(t, supplier.ValidSupplierID(item.SupplierID))
			require.True(t, supplier.ValidVersionID(item.VersionID))
		}
		require.Equal(t, test.names, names, "filter: %+v", test.filter)
		require.Empty(t, page.NextCursor)
	}
	filter := supplier.ListFilter{Type: "food", Limit: 1}
	first, err := r.ListAvailable(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, " alpha   Cafe ", first.Items[0].Details.Name)
	require.NotEmpty(t, first.NextCursor)
	filter.Cursor = first.NextCursor
	last, err := r.ListAvailable(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, "Zulu", last.Items[0].Details.Name)
	require.Empty(t, last.NextCursor)
	_, err = r.ListAvailable(ctx, supplier.ListFilter{Type: "shopping", Cursor: first.NextCursor})
	var application *apperror.Error
	require.ErrorAs(t, err, &application)
	require.Equal(t, apperror.InvalidArgument, application.Code)
}

func TestCatalogueReturnsNewestVersionAndExcludesDeletedSuppliers(t *testing.T) {
	r, pool := migratedRepository(t)
	createLookups(t, r, validDetails(""))
	ctx := context.Background()
	// Exercise a host clock ahead of PostgreSQL without relying on clock sync.
	createdAt := time.Now().UTC().Add(24 * time.Hour)
	created, err := r.Create(ctx, validDetails("Old Catalogue Name"), createdAt)
	require.NoError(t, err)
	name := "New Catalogue Name"
	updated, err := r.Update(ctx, created.SupplierID, supplier.Patch{Name: &name}, createdAt.Add(time.Minute))
	require.NoError(t, err)
	current, err := r.GetCurrentAvailable(ctx, created.SupplierID)
	require.NoError(t, err)
	require.Equal(t, updated, current)
	page, err := r.ListAvailable(ctx, supplier.ListFilter{})
	require.NoError(t, err)
	require.Equal(t, []supplier.Supplier{updated}, page.Items)
	oldSearch, err := r.ListAvailable(ctx, supplier.ListFilter{Query: "old catalogue"})
	require.NoError(t, err)
	require.Empty(t, oldSearch.Items)
	// Keep fixture deletion on the stored timeline, independent of Docker's clock.
	_, err = pool.Exec(ctx, "UPDATE suppliers SET deleted_at = updated_at + interval '1 second' WHERE supplier_id = $1", created.SupplierID)
	require.NoError(t, err)
	page, err = r.ListAvailable(ctx, supplier.ListFilter{})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	_, err = r.GetCurrentAvailable(ctx, created.SupplierID)
	var application *apperror.Error
	require.ErrorAs(t, err, &application)
	require.Equal(t, apperror.SupplierNotFound, application.Code)
	for _, snapshot := range []supplier.Supplier{created, updated} {
		version, err := r.GetVersion(ctx, snapshot.VersionID)
		require.NoError(t, err)
		require.Equal(t, snapshot.Details, version.Details)
		require.False(t, version.Available)
	}
}

func TestCursorRemainsBoundedForUnicodeAndResolvesDeletedAnchor(t *testing.T) {
	r, pool := migratedRepository(t)
	createLookups(t, r, validDetails(""))
	ctx := context.Background()
	createdAt := time.Now().UTC().Add(24 * time.Hour)
	for _, name := range []string{strings.Repeat("界", 120), strings.Repeat("界", 119) + "齊"} {
		_, err := r.Create(ctx, validDetails(name), createdAt)
		require.NoError(t, err)
	}
	first, err := r.ListAvailable(ctx, supplier.ListFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	require.LessOrEqual(t, len(first.NextCursor), 512)
	_, err = pool.Exec(ctx, "UPDATE suppliers SET deleted_at = updated_at + interval '1 second' WHERE supplier_id = $1", first.Items[0].SupplierID)
	require.NoError(t, err)
	last, err := r.ListAvailable(ctx, supplier.ListFilter{Limit: 1, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.Len(t, last.Items, 1)
	require.NotEqual(t, first.Items[0].SupplierID, last.Items[0].SupplierID)
	require.Empty(t, last.NextCursor)
}

// Server-side sorting: every sort pages through one total order, and cursors
// are bound to the sort that produced them.
func TestCatalogueSortsPageThroughOneSequence(t *testing.T) {
	r, _ := migratedRepository(t)
	ctx := context.Background()
	for name, place := range map[string]struct{ supplierType, building string }{
		"Delta":   {" Food ", "Arts"},
		" alpha ": {"retail", "COM 3"},
		"Charlie": {"food", "COM  3"},
		"Bravo":   {"FOOD", "  UTown "},
		"Echo":    {"Books", "utown"},
		"Foxtrot": {"retail", "com	3"},
	} {
		details := validDetails(name)
		details.Type, details.Building = place.supplierType, place.building
		createLookups(t, r, details)
		_, err := r.Create(ctx, details, time.Now().UTC())
		require.NoError(t, err)
	}
	for _, test := range []struct {
		sort  supplier.ListSort
		names []string
	}{
		{"", []string{" alpha ", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"}},
		{supplier.SortName, []string{" alpha ", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"}},
		{supplier.SortNameDesc, []string{"Foxtrot", "Echo", "Delta", "Charlie", "Bravo", " alpha "}},
		// Page boundaries at limit=2 fall inside the food and "com 3" groups,
		// exercising the name tie-break after type and building.
		{supplier.SortType, []string{"Echo", "Bravo", "Charlie", "Delta", " alpha ", "Foxtrot"}},
		{supplier.SortTypeDesc, []string{"Foxtrot", " alpha ", "Delta", "Charlie", "Bravo", "Echo"}},
		{supplier.SortBuilding, []string{"Delta", " alpha ", "Charlie", "Foxtrot", "Bravo", "Echo"}},
		{supplier.SortBuildingDesc, []string{"Echo", "Bravo", "Foxtrot", "Charlie", " alpha ", "Delta"}},
	} {
		unpaged, err := r.ListAvailable(ctx, supplier.ListFilter{Sort: test.sort, Limit: supplier.MaximumPageSize})
		require.NoError(t, err)
		require.Empty(t, unpaged.NextCursor)
		require.Equal(t, test.names, supplierNames(unpaged.Items), "sort %q", test.sort)

		var paged []supplier.Supplier
		filter := supplier.ListFilter{Sort: test.sort, Limit: 2}
		for pages := 0; ; pages++ {
			require.Less(t, pages, len(test.names), "sort %q did not terminate", test.sort)
			page, err := r.ListAvailable(ctx, filter)
			require.NoError(t, err)
			require.LessOrEqual(t, len(page.Items), 2)
			paged = append(paged, page.Items...)
			if page.NextCursor == "" {
				break
			}
			filter.Cursor = page.NextCursor
		}
		require.Equal(t, unpaged.Items, paged, "sort %q", test.sort)
		seen := map[supplier.SupplierID]bool{}
		for _, item := range paged {
			require.False(t, seen[item.SupplierID], "sort %q duplicated %s", test.sort, item.Details.Name)
			seen[item.SupplierID] = true
		}
	}
}

func TestCatalogueCursorIsRejectedUnderAnotherSort(t *testing.T) {
	r, _ := migratedRepository(t)
	createLookups(t, r, validDetails(""))
	ctx := context.Background()
	for _, name := range []string{"One", "Two", "Three"} {
		_, err := r.Create(ctx, validDetails(name), time.Now().UTC())
		require.NoError(t, err)
	}
	sorts := supplier.ListSorts
	for _, from := range sorts {
		first, err := r.ListAvailable(ctx, supplier.ListFilter{Sort: from, Limit: 1})
		require.NoError(t, err)
		require.NotEmpty(t, first.NextCursor)
		for _, to := range sorts {
			_, err := r.ListAvailable(ctx, supplier.ListFilter{Sort: to, Limit: 1, Cursor: first.NextCursor})
			if to == from {
				require.NoError(t, err)
				continue
			}
			var application *apperror.Error
			require.ErrorAs(t, err, &application, "%q cursor reused under %q", from, to)
			require.Equal(t, apperror.InvalidArgument, application.Code)
			require.Equal(t, "cursor is invalid", application.Message)
		}
	}
	// The omitted sort is the name sort, so their cursors are interchangeable.
	first, err := r.ListAvailable(ctx, supplier.ListFilter{Limit: 1})
	require.NoError(t, err)
	_, err = r.ListAvailable(ctx, supplier.ListFilter{Sort: supplier.SortName, Limit: 1, Cursor: first.NextCursor})
	require.NoError(t, err)
}

func supplierNames(items []supplier.Supplier) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Details.Name)
	}
	return names
}
