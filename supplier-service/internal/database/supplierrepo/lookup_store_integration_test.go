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

func TestLookupListOrdersByKeyHidesRetiredAndIsCapped(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, name := range []string{"Utown", "com 3", "Com 2"} {
		_, err := repository.CreateLookup(ctx, supplier.BuildingLookup, name, now)
		require.NoError(t, err)
	}
	old, err := repository.CreateLookup(ctx, supplier.BuildingLookup, "Arts Link", now)
	require.NoError(t, err)
	require.NoError(t, repository.RetireLookup(ctx, supplier.BuildingLookup, old.ID, now))

	live, err := repository.ListLookups(ctx, supplier.BuildingLookup, false, supplier.MaximumLookupListSize)
	require.NoError(t, err)
	require.Equal(t, []string{"Com 2", "com 3", "Utown"}, lookupNames(live))

	all, err := repository.ListLookups(ctx, supplier.BuildingLookup, true, supplier.MaximumLookupListSize)
	require.NoError(t, err)
	require.Equal(t, []string{"Arts Link", "Com 2", "com 3", "Utown"}, lookupNames(all))
	require.True(t, all[0].Retired)
	require.False(t, all[1].Retired)

	types, err := repository.ListLookups(ctx, supplier.SupplierTypeLookup, true, supplier.MaximumLookupListSize)
	require.NoError(t, err)
	require.Empty(t, types)

	_, err = pool.Exec(ctx, `INSERT INTO supplier_types (name) SELECT 'Type ' || n FROM generate_series(1, 505) AS n`)
	require.NoError(t, err)
	types, err = repository.ListLookups(ctx, supplier.SupplierTypeLookup, false, supplier.MaximumLookupListSize)
	require.NoError(t, err)
	require.Len(t, types, supplier.MaximumLookupListSize)
}

func TestCreateLookupConflictUsesTheLiveKeyIndex(t *testing.T) {
	repository, _ := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	existing, err := repository.CreateLookup(ctx, supplier.BuildingLookup, "Com 2", now)
	require.NoError(t, err)
	require.Regexp(t, uuidPattern, string(existing.ID))

	_, err = repository.CreateLookup(ctx, supplier.BuildingLookup, "Com2", now)
	conflict := &supplier.LookupConflictError{}
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, existing, conflict.Existing)

	// The two tables are independent, and a retired key can be reused.
	_, err = repository.CreateLookup(ctx, supplier.SupplierTypeLookup, "Com2", now)
	require.NoError(t, err)
	require.NoError(t, repository.RetireLookup(ctx, supplier.BuildingLookup, existing.ID, now))
	replacement, err := repository.CreateLookup(ctx, supplier.BuildingLookup, "COM-2", now)
	require.NoError(t, err)
	require.NotEqual(t, existing.ID, replacement.ID)
	require.Equal(t, "COM-2", replacement.Name)
}

func TestRetireLookupChecksCurrentVersionsOnlyAndIsIdempotent(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	details := validDetails("Retirement Supplier")
	details.Building, details.Type = "Com 2", "Printing"
	createLookups(t, repository, details)
	created, err := repository.Create(ctx, details, now)
	require.NoError(t, err)
	buildingID := supplier.LookupID(created.Details.BuildingID)
	typeID := supplier.LookupID(created.Details.TypeID)

	err = repository.RetireLookup(ctx, supplier.BuildingLookup, buildingID, now)
	inUse := &supplier.LookupInUseError{}
	require.ErrorAs(t, err, &inUse)
	require.Equal(t, "Com 2", inUse.Lookup.Name)
	require.Equal(t, 1, inUse.Suppliers)
	err = repository.RetireLookup(ctx, supplier.SupplierTypeLookup, typeID, now)
	require.ErrorAs(t, err, &inUse)

	// A superseded version does not keep the building in use.
	moved := "Com 3"
	mustCreateLookup(t, repository, supplier.BuildingLookup, moved)
	updated, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Building: &moved}, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repository.RetireLookup(ctx, supplier.BuildingLookup, buildingID, now))
	require.NoError(t, repository.RetireLookup(ctx, supplier.BuildingLookup, buildingID, now.Add(time.Hour)), "retiring twice succeeds")
	var retiredAt time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT retired_at FROM buildings WHERE id = $1`, buildingID).Scan(&retiredAt))
	require.WithinDuration(t, now, retiredAt, time.Second, "the second retirement does not move retired_at")
	require.Equal(t, "Com 2", mustGetVersion(t, repository, created.VersionID).Details.Building, "history still resolves")

	// A deleted supplier does not keep its lookups in use.
	_, err = pool.Exec(ctx, `UPDATE suppliers SET deleted_at = GREATEST(clock_timestamp(), created_at), updated_at = GREATEST(clock_timestamp(), updated_at) WHERE supplier_id = $1`, string(created.SupplierID))
	require.NoError(t, err)
	require.NoError(t, repository.RetireLookup(ctx, supplier.SupplierTypeLookup, typeID, now))
	require.NoError(t, repository.RetireLookup(ctx, supplier.BuildingLookup, supplier.LookupID(updated.Details.BuildingID), now))

	require.ErrorIs(t, repository.RetireLookup(ctx, supplier.BuildingLookup, supplier.LookupID(testUUID(t)), now), supplier.ErrLookupNotFound)
	require.ErrorIs(t, repository.RetireLookup(ctx, supplier.SupplierTypeLookup, buildingID, now), supplier.ErrLookupNotFound, "IDs are per table")
}

// TestConcurrentRetireAndAssignNeverLeaveACurrentVersionOnARetiredRow races
// RetireLookup against an Update that moves a supplier onto the same
// building. Exactly one of them succeeds: either the update commits first and
// retirement fails as in use, or retirement commits first and the update is
// rejected because the building no longer exists.
func TestConcurrentRetireAndAssignNeverLeaveACurrentVersionOnARetiredRow(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	const rounds = 16
	retiredFirst, assignedFirst := 0, 0
	for round := range rounds {
		now := time.Now().UTC()
		name := fmt.Sprintf("Race Hall %d", round)
		contested, err := repository.CreateLookup(ctx, supplier.BuildingLookup, name, now)
		require.NoError(t, err)
		details := validDetails(fmt.Sprintf("Race Supplier %d", round))
		details.Building = "Base Hall"
		createLookups(t, repository, details)
		created, err := repository.Create(ctx, details, now)
		require.NoError(t, err)

		start := make(chan struct{})
		retireResult := make(chan error, 1)
		updateResult := make(chan supplier.Supplier, 1)
		updateErr := make(chan error, 1)
		go func() {
			<-start
			// The update does more work before it reaches the building, so
			// odd rounds give it a growing head start to cover both orders.
			time.Sleep(time.Duration(round%2*round) * time.Millisecond)
			retireResult <- repository.RetireLookup(ctx, supplier.BuildingLookup, contested.ID, time.Now().UTC())
		}()
		go func() {
			<-start
			building := name
			updated, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Building: &building}, time.Now().UTC())
			updateResult <- updated
			updateErr <- err
		}()
		close(start)
		retireErr := <-retireResult
		updated := <-updateResult
		assignErr := <-updateErr

		current := mustGetCurrent(t, repository, created.SupplierID)
		if retireErr == nil {
			retiredFirst++
			validation := &supplier.ValidationError{}
			require.ErrorAs(t, assignErr, &validation, "round %d: an update that waited for retirement must not use the retired row", round)
			require.Equal(t, []supplier.FieldViolation{{Field: "building", Message: "must be an existing building"}}, validation.Violations)
			require.Equal(t, created.Details.BuildingID, current.Details.BuildingID, "round %d", round)
		} else {
			assignedFirst++
			inUse := &supplier.LookupInUseError{}
			require.ErrorAs(t, retireErr, &inUse, "round %d", round)
			require.NoError(t, assignErr, "round %d", round)
			require.Equal(t, string(contested.ID), updated.Details.BuildingID, "round %d", round)
			require.Equal(t, string(contested.ID), current.Details.BuildingID, "round %d", round)
		}
		requireNoCurrentVersionOnRetiredLookup(t, pool)
	}
	t.Logf("retire won %d rounds, assignment won %d rounds", retiredFirst, assignedFirst)
}

// TestAssignmentWaitingOnRetirementDoesNotUseTheRetiredRow forces the
// interleaving the FOR SHARE resolution exists for: the assigning update
// starts while retirement holds the row lock. Without the shared lock the
// update would resolve the still-live row from its snapshot, and the foreign
// key check would accept it once retirement committed.
func TestAssignmentWaitingOnRetirementDoesNotUseTheRetiredRow(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	contested, err := repository.CreateLookup(ctx, supplier.BuildingLookup, "Contested Hall", now)
	require.NoError(t, err)
	details := validDetails("Waiting Supplier")
	details.Building = "Base Hall"
	createLookups(t, repository, details)
	created, err := repository.Create(ctx, details, now)
	require.NoError(t, err)

	retirement, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = retirement.Rollback(context.Background()) }()
	_, err = retirement.Exec(ctx, `SELECT 1 FROM buildings WHERE id = $1 FOR UPDATE`, string(contested.ID))
	require.NoError(t, err)
	_, err = retirement.Exec(ctx, `UPDATE buildings SET retired_at = GREATEST(clock_timestamp(), created_at) WHERE id = $1`, string(contested.ID))
	require.NoError(t, err)
	var xid string
	require.NoError(t, retirement.QueryRow(ctx, `SELECT pg_current_xact_id()::xid::text`).Scan(&xid))

	type outcome struct {
		updated supplier.Supplier
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		building := "contested hall"
		updated, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Building: &building}, time.Now().UTC())
		done <- outcome{updated, err}
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_locks WHERE locktype = 'transactionid' AND NOT granted AND transactionid::text = $1
		)`, xid).Scan(&waiting)
		return err == nil && waiting
	}, 10*time.Second, 10*time.Millisecond, "the update must block on the retirement's row lock")
	require.NoError(t, retirement.Commit(ctx))

	result := <-done
	requireViolations(t, result.err, supplier.FieldViolation{Field: "building", Message: "must be an existing building"})
	require.Equal(t, created.Details.BuildingID, mustGetCurrent(t, repository, created.SupplierID).Details.BuildingID)
	requireNoCurrentVersionOnRetiredLookup(t, pool)
}

func requireNoCurrentVersionOnRetiredLookup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM suppliers AS s
		JOIN supplier_versions AS v ON v.version_id = s.current_version_id
		JOIN buildings AS b ON b.id = v.building_id
		JOIN supplier_types AS t ON t.id = v.supplier_type_id
		WHERE s.deleted_at IS NULL
		  AND (b.retired_at IS NOT NULL OR t.retired_at IS NOT NULL)`).Scan(&count))
	require.Zero(t, count, "a current supplier version references a retired lookup")
}

func lookupNames(lookups []supplier.Lookup) []string {
	result := make([]string, len(lookups))
	for i, item := range lookups {
		result[i] = item.Name
	}
	return result
}
