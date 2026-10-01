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
