package supplierrepo

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

const capacityRecordCount = 1_000

func TestListAndSearchPaginationAcrossOneThousandRecords(t *testing.T) {
	repository, pool := migratedRepository(t)
	insertCapacityRecords(t, pool, capacityRecordCount)

	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "list"},
		{name: "search", query: "capacity supplier"},
	} {
		t.Run(test.name, func(t *testing.T) {
			seen := make(map[supplier.SupplierID]struct{}, capacityRecordCount)
			cursor := ""
			pages := 0
			for {
				page, err := repository.ListAvailable(context.Background(), supplier.ListFilter{
					Query: test.query, Limit: 97, Cursor: cursor,
				})
				require.NoError(t, err)
				require.LessOrEqual(t, len(page.Items), 97)
				pages++
				for _, item := range page.Items {
					_, duplicate := seen[item.SupplierID]
					require.False(t, duplicate, "cursor pagination returned a duplicate supplier")
					seen[item.SupplierID] = struct{}{}
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			require.Equal(t, capacityRecordCount, len(seen))
			require.Greater(t, pages, 1)
		})
	}
}

// BenchmarkListSearchPagination1000 is the repeatable data-layer capacity
// entry point. It traverses every page for both an unfiltered list and a
// search matching the full 1,000-record reference dataset.
func BenchmarkListSearchPagination1000(b *testing.B) {
	repository, pool := migratedRepository(b)
	insertCapacityRecords(b, pool, capacityRecordCount)
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		for _, query := range []string{"", "capacity supplier"} {
			cursor := ""
			for {
				page, err := repository.ListAvailable(context.Background(), supplier.ListFilter{
					Query: query, Limit: supplier.MaximumPageSize, Cursor: cursor,
				})
				if err != nil {
					b.Fatal(err)
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
		}
	}
}

func insertCapacityRecords(t testing.TB, pool *pgxpool.Pool, count int) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO suppliers (
			supplier_id, current_version_id, current_normalized_name, created_at, updated_at
		)
		SELECT gen_random_uuid(),
		       gen_random_uuid(),
		       'capacity supplier ' || lpad(sequence::text, 4, '0'),
		       clock_timestamp(),
		       clock_timestamp()
		FROM generate_series(1, $1) AS sequence
	`, count)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO supplier_versions (
			version_id, supplier_id, name, supplier_type, building, floor,
			location_description, latitude, longitude, opening_time, closing_time
		)
		SELECT current_version_id,
		       supplier_id,
		       current_normalized_name,
		       'food',
		       'COM 3',
		       '1',
		       'capacity fixture',
		       1.294,
		       103.773,
		       time '09:00',
		       time '18:00'
		FROM suppliers
	`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx), fmt.Sprintf("insert %d capacity records", count))
}
