package migrations

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// legacyVersion is a supplier version written before 00002, when building
// and type were free text.
type legacyVersion struct {
	name, supplierType, building, createdAt string
}

// The CSV's real variants: "Com 2"/"Com2" and straight/curly apostrophes.
var legacyVersions = []legacyVersion{
	{"Printer @ Com 2", "Printing", "Com 2", "2026-01-01T00:00:00Z"},
	{"Cool Spot", "Food", "Com2", "2026-01-02T00:00:00Z"},
	{"Com Cafe", "food ", "Com 2", "2026-01-03T00:00:00Z"},
	{"A Hot Hideout", "Food", "Prince George's Park", "2026-01-04T00:00:00Z"},
	{"Octobox", "Shopping", "Prince George’s Park", "2026-01-05T00:00:00Z"},
}

func TestLookupMigrationCanonicalisesExistingSpellingsUpDownUp(t *testing.T) {
	conn := emptyDatabase(t)
	ctx := context.Background()
	require.NoError(t, applyMigrationSection(ctx, conn, versionedSuppliersMigration, "Up"))
	for _, version := range legacyVersions {
		insertLegacyVersion(t, conn, version)
	}

	require.NoError(t, applyMigrationSection(ctx, conn, lookupsMigration, "Up"))
	requireCanonicalLookups(t, conn)

	require.NoError(t, applyMigrationSection(ctx, conn, lookupsMigration, "Down"))
	for _, relation := range []string{"buildings", "supplier_types"} {
		var found *string
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, relation).Scan(&found))
		require.Nilf(t, found, "%s remains after rollback", relation)
	}
	// Rollback restores free text holding each version's canonical name.
	rows, err := conn.Query(ctx, `
		SELECT name, building, supplier_type FROM supplier_versions ORDER BY created_at
	`)
	require.NoError(t, err)
	restored, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([3]string, error) {
		var values [3]string
		err := row.Scan(&values[0], &values[1], &values[2])
		return values, err
	})
	require.NoError(t, err)
	require.Equal(t, [][3]string{
		{"Printer @ Com 2", "Com 2", "Printing"},
		{"Cool Spot", "Com 2", "Food"},
		{"Com Cafe", "Com 2", "Food"},
		{"A Hot Hideout", "Prince George's Park", "Food"},
		{"Octobox", "Prince George's Park", "Shopping"},
	}, restored)
	// The append-only trigger was re-enabled by the rollback.
	_, err = conn.Exec(ctx, `UPDATE supplier_versions SET building = 'Changed'`)
	requirePostgresCode(t, err, "55000")

	require.NoError(t, applyMigrationSection(ctx, conn, lookupsMigration, "Up"))
	requireCanonicalLookups(t, conn)
}

func requireCanonicalLookups(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()

	// "Com 2" is used twice and wins over "Com2". Both apostrophes are used
	// once, so the spelling of the earliest version wins.
	require.Equal(t, []string{"Com 2", "Prince George's Park"}, lookupNames(t, conn, "buildings"))
	require.Equal(t, []string{"Food", "Printing", "Shopping"}, lookupNames(t, conn, "supplier_types"))

	var buildingCreatedAt string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') FROM buildings WHERE name = 'Com 2'
	`).Scan(&buildingCreatedAt))
	require.Equal(t, "2026-01-01", buildingCreatedAt, "a lookup dates from its key's first use")

	rows, err := conn.Query(ctx, `
		SELECT v.name, b.name, t.name
		FROM supplier_versions AS v
		JOIN buildings AS b ON b.id = v.building_id
		JOIN supplier_types AS t ON t.id = v.supplier_type_id
		ORDER BY v.created_at
	`)
	require.NoError(t, err)
	linked, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([3]string, error) {
		var values [3]string
		err := row.Scan(&values[0], &values[1], &values[2])
		return values, err
	})
	require.NoError(t, err)
	require.Equal(t, [][3]string{
		{"Printer @ Com 2", "Com 2", "Printing"},
		{"Cool Spot", "Com 2", "Food"},
		{"Com Cafe", "Com 2", "Food"},
		{"A Hot Hideout", "Prince George's Park", "Food"},
		{"Octobox", "Prince George's Park", "Shopping"},
	}, linked, "every version is linked")

	for _, column := range []string{"building", "supplier_type"} {
		var exists bool
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'supplier_versions'
				  AND column_name = $1
			)
		`, column).Scan(&exists))
		require.Falsef(t, exists, "supplier_versions.%s was not dropped", column)
	}

	// The append-only trigger was re-enabled after the backfill.
	_, err = conn.Exec(ctx, `UPDATE supplier_versions SET floor = 'Changed'`)
	requirePostgresCode(t, err, "55000")
}

func TestLookupMigrationRejectsHistoricalValuesWithoutAKey(t *testing.T) {
	conn := emptyDatabase(t)
	ctx := context.Background()
	require.NoError(t, applyMigrationSection(ctx, conn, versionedSuppliersMigration, "Up"))
	insertLegacyVersion(t, conn, legacyVersion{"Punctuation", "Food", "---", "2026-01-01T00:00:00Z"})

	err := applyMigrationSection(ctx, conn, lookupsMigration, "Up")
	requirePostgresCode(t, err, "23514")
	require.ErrorContains(t, err, "without a letter or digit")
}

func TestLookupKeySQLMatchesGo(t *testing.T) {
	conn := migratedDatabase(t)
	for _, input := range []string{
		"Com 2",
		"  COM-2 ",
		"Prince George's Park",
		"Prince George’s Park",
		"Food/Coffee",
		"innovation4.0",
		"Café Été",
		"ÉCOLE",
		"中央图书馆 1",
		"Ⅷ Hall",
		"हिंदी",
		"٣ Block",
		"tab\tand\nnewline",
		"---",
	} {
		var key string
		require.NoError(t, conn.QueryRow(context.Background(), `SELECT normalize_lookup_key($1)`, input).Scan(&key))
		require.Equal(t, supplier.LookupKey(input), key, "lookup key of %q", input)
	}
	// Non-ASCII letters are alphanumeric in both implementations.
	require.Equal(t, "caféété", supplier.LookupKey("Café Été"))
}

// retireNow is never before created_at. A container's wall clock can step
// backwards between statements, which the retired_after_creation check
// would rightly reject.
const retireNow = "GREATEST(clock_timestamp(), created_at)"

func TestLookupRowsAreImmutableExceptRetirement(t *testing.T) {
	for _, table := range []string{"buildings", "supplier_types"} {
		t.Run(table, func(t *testing.T) {
			conn := migratedDatabase(t)
			ctx := context.Background()
			identifier := pgx.Identifier{table}.Sanitize()
			var id string
			require.NoError(t, conn.QueryRow(ctx,
				`INSERT INTO `+identifier+` (name) VALUES ('Com 2') RETURNING id::text`,
			).Scan(&id))

			for _, change := range []string{
				`UPDATE ` + identifier + ` SET name = 'Com Two' WHERE id = $1`,
				`UPDATE ` + identifier + ` SET name = 'Com2' WHERE id = $1`,
				`UPDATE ` + identifier + ` SET created_at = created_at - interval '1 day' WHERE id = $1`,
				`UPDATE ` + identifier + ` SET id = gen_random_uuid() WHERE id = $1`,
				`UPDATE ` + identifier + ` SET name = 'Com Two', retired_at = clock_timestamp() WHERE id = $1`,
				`DELETE FROM ` + identifier + ` WHERE id = $1`,
			} {
				_, err := conn.Exec(ctx, change, id)
				requirePostgresCode(t, err, "55000")
			}

			_, err := conn.Exec(ctx, `UPDATE `+identifier+` SET retired_at = `+retireNow+` WHERE id = $1`, id)
			require.NoError(t, err)
			for _, change := range []string{
				`UPDATE ` + identifier + ` SET retired_at = ` + retireNow + ` WHERE id = $1`,
				`UPDATE ` + identifier + ` SET retired_at = NULL WHERE id = $1`,
				`DELETE FROM ` + identifier + ` WHERE id = $1`,
			} {
				_, err := conn.Exec(ctx, change, id)
				requirePostgresCode(t, err, "55000")
			}
		})
	}
}

func TestLookupKeysAreUniqueAmongLiveRows(t *testing.T) {
	for _, table := range []string{"buildings", "supplier_types"} {
		t.Run(table, func(t *testing.T) {
			conn := migratedDatabase(t)
			ctx := context.Background()
			identifier := pgx.Identifier{table}.Sanitize()
			insert := `INSERT INTO ` + identifier + ` (name) VALUES ($1)`

			_, err := conn.Exec(ctx, insert, "Com 2")
			require.NoError(t, err)
			_, err = conn.Exec(ctx, insert, "COM-2")
			requirePostgresCode(t, err, "23505")
			_, err = conn.Exec(ctx, insert, "---")
			requirePostgresCode(t, err, "23514")
			_, err = conn.Exec(ctx, insert, "   ")
			requirePostgresCode(t, err, "23514")

			// Retiring a row releases its key for a replacement.
			_, err = conn.Exec(ctx, `UPDATE `+identifier+` SET retired_at = `+retireNow+` WHERE name = 'Com 2'`)
			require.NoError(t, err)
			_, err = conn.Exec(ctx, insert, "Com2")
			require.NoError(t, err)
		})
	}
}

func insertLegacyVersion(t *testing.T, conn *pgx.Conn, version legacyVersion) {
	t.Helper()
	ctx := context.Background()
	supplierID, versionID := newUUID(t), newUUID(t)
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO suppliers (supplier_id, current_version_id, current_normalized_name, created_at, updated_at)
			VALUES ($1, $2, normalize_supplier_name($3), $4, $4)
		`, supplierID, versionID, version.name, version.createdAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO supplier_versions (
				version_id, supplier_id, name, supplier_type, building, floor,
				location_description, latitude, longitude, opening_time, closing_time, created_at
			) VALUES ($1, $2, $3, $4, $5, '1', 'Lobby', 1.29, 103.77, '09:00', '18:00', $6)
		`, versionID, supplierID, version.name, version.supplierType, version.building, version.createdAt)
		return err
	})
	require.NoError(t, err)
}

func lookupNames(t *testing.T, conn *pgx.Conn, table string) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		`SELECT name FROM `+pgx.Identifier{table}.Sanitize()+` ORDER BY lookup_key`)
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return names
}
