package facts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/migrations"
)

func migratedStore(t *testing.T) (*Store, *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL facts store integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	suffix := make([]byte, 8)
	_, err = rand.Read(suffix)
	require.NoError(t, err)
	schema := pgx.Identifier{"admin_facts_" + hex.EncodeToString(suffix)}.Sanitize()
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = conn.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close(cleanupCtx)
	})
	_, err = conn.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = conn.Exec(ctx, up)
	require.NoError(t, err)
	return NewStore(conn), conn
}

type row struct {
	status          string
	version         int
	courier         *string
	reward          int
	openedAt        *time.Time
	firstAcceptedAt *time.Time
	terminalStatus  *string
	terminalAt      *time.Time
	enrichmentDue   bool
	supplierName    *string
	supplierID      *string
	buildingName    *string
	enrichedAt      *time.Time
}

func readRow(t *testing.T, conn *pgx.Conn) row {
	t.Helper()
	var r row
	require.NoError(t, conn.QueryRow(context.Background(), `
		SELECT status, version, courier_id, reward, opened_at, first_accepted_at, terminal_status,
		       terminal_at, enrichment_due, supplier_name, supplier_id::text, delivery_building_name, enriched_at
		FROM errand_facts WHERE errand_id = $1`, errandID).Scan(
		&r.status, &r.version, &r.courier, &r.reward, &r.openedAt, &r.firstAcceptedAt, &r.terminalStatus,
		&r.terminalAt, &r.enrichmentDue, &r.supplierName, &r.supplierID, &r.buildingName, &r.enrichedAt))
	return r
}

func count(t *testing.T, conn *pgx.Conn, table string) int {
	t.Helper()
	var n int
	require.NoError(t, conn.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n))
	return n
}

func applyAll(t *testing.T, store *Store, events []Event) {
	t.Helper()
	for _, e := range events {
		_, err := store.Apply(context.Background(), e)
		require.NoError(t, err, e.MessageID)
	}
}

func TestApplyBuildsTheRowFromTheLifecycle(t *testing.T) {
	store, conn := migratedStore(t)

	applyAll(t, store, lifecycle())

	r := readRow(t, conn)
	require.Equal(t, "COMPLETED", r.status)
	require.Equal(t, 8, r.version)
	require.True(t, r.openedAt.Equal(at(2)))
	require.True(t, r.firstAcceptedAt.Equal(at(10)))
	require.Equal(t, "COMPLETED", *r.terminalStatus)
	require.True(t, r.terminalAt.Equal(at(60)))
	require.True(t, r.enrichmentDue)
	require.Equal(t, 8, count(t, conn, "transition_facts"))
	require.Equal(t, 1, count(t, conn, "errand_facts"))
}

func TestApplyDuplicateDeliveryChangesNothing(t *testing.T) {
	store, conn := migratedStore(t)
	ctx := context.Background()
	first := accepted(3, 10, "auth0|cou1")

	applied, err := store.Apply(ctx, first)
	require.NoError(t, err)
	require.True(t, applied)
	before := readRow(t, conn)

	for range 3 {
		applied, err = store.Apply(ctx, first)
		require.NoError(t, err)
		require.False(t, applied, "the messageId is already stored")
	}

	require.Equal(t, before, readRow(t, conn))
	require.Equal(t, 1, count(t, conn, "transition_facts"))
}

// A crash between the commit and the ack makes the broker deliver the message
// again. The second delivery must change nothing, even after later events.
func TestApplyReplayAfterCrashBetweenCommitAndAck(t *testing.T) {
	store, conn := migratedStore(t)
	ctx := context.Background()
	events := lifecycle()

	applyAll(t, store, events[:5]) // opened, accepted, withdrawn, re-accepted, committed
	applyAll(t, store, events[5:])
	want := readRow(t, conn)

	// Redelivery of the unacknowledged ones, in the broker's order.
	for _, e := range events[3:6] {
		applied, err := store.Apply(ctx, e)
		require.NoError(t, err)
		require.False(t, applied)
	}

	require.Equal(t, want, readRow(t, conn))
	require.Equal(t, len(events), count(t, conn, "transition_facts"))
}

func TestApplyOutOfOrderDelivery(t *testing.T) {
	store, conn := migratedStore(t)
	events := lifecycle()
	// Newest first.
	for i := len(events) - 1; i >= 0; i-- {
		_, err := store.Apply(context.Background(), events[i])
		require.NoError(t, err)
	}

	r := readRow(t, conn)

	require.Equal(t, "COMPLETED", r.status, "a stale event does not roll the state back")
	require.Equal(t, 8, r.version)
	require.True(t, r.openedAt.Equal(at(2)))
	require.True(t, r.firstAcceptedAt.Equal(at(10)), "the earliest accept wins even when it arrives last")
	require.Equal(t, 8, count(t, conn, "transition_facts"), "every event is kept as a fact")
}

func TestApplyEqualVersionPair(t *testing.T) {
	for name, order := range map[string][]Event{
		"withdrawn then expired": {
			ev(TypeWithdrawn, 4, StatusAssigned, StatusOpen, 30),
			ev(TypeExpired, 4, StatusAssigned, StatusExpired, 30),
		},
		"expired then withdrawn": {
			ev(TypeExpired, 4, StatusAssigned, StatusExpired, 30),
			ev(TypeWithdrawn, 4, StatusAssigned, StatusOpen, 30),
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, conn := migratedStore(t)
			applyAll(t, store, []Event{
				ev(TypeOpened, 2, StatusPendingReservation, StatusOpen, 2),
				accepted(3, 10, "auth0|cou1"),
			})
			applyAll(t, store, order)

			r := readRow(t, conn)

			require.Equal(t, "EXPIRED", r.status)
			require.Equal(t, "EXPIRED", *r.terminalStatus)
			require.Equal(t, 4, r.version)
			require.Equal(t, 4, count(t, conn, "transition_facts"), "both events of the pair are facts")
		})
	}
}

func TestApplyRejectsAnInvalidEvent(t *testing.T) {
	store, conn := migratedStore(t)
	bad := accepted(3, 10, "auth0|cou1")
	bad.To = "LOST"

	applied, err := store.Apply(context.Background(), bad)

	require.ErrorIs(t, err, ErrInvalid)
	require.False(t, applied)
	require.Zero(t, count(t, conn, "transition_facts"))
}

func TestApplyIsAtomic(t *testing.T) {
	store, conn := migratedStore(t)
	// A trigger makes the errand_facts write fail after the transition fact
	// was inserted in the same transaction.
	_, err := conn.Exec(context.Background(), `
		CREATE FUNCTION refuse_errand_facts() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$;
		CREATE TRIGGER refuse BEFORE INSERT ON errand_facts FOR EACH ROW EXECUTE FUNCTION refuse_errand_facts();`)
	require.NoError(t, err)

	_, err = store.Apply(context.Background(), accepted(3, 10, "auth0|cou1"))

	require.Error(t, err)
	require.Zero(t, count(t, conn, "transition_facts"), "the transition fact rolled back with the failed merge")
	require.Zero(t, count(t, conn, "errand_facts"))
}

func TestApplyConcurrentEventsOfOneErrand(t *testing.T) {
	_, conn := migratedStore(t)
	ctx := context.Background()
	// A pool on the same schema, so the events really run on several connections.
	var searchPath string
	require.NoError(t, conn.QueryRow(ctx, `SHOW search_path`).Scan(&searchPath))
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	require.NoError(t, err)
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["search_path"] = searchPath
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := NewStore(pool)

	// Every event twice, all at once: the first writers of an errand race to
	// create its row, the rest merge into it in turn.
	events := append(lifecycle(), lifecycle()...)
	errs := make(chan error, len(events))
	applied := make(chan bool, len(events))
	for _, e := range events {
		go func() {
			ok, err := store.Apply(ctx, e)
			applied <- ok
			errs <- err
		}()
	}
	newEvents := 0
	for range events {
		require.NoError(t, <-errs)
		if <-applied {
			newEvents++
		}
	}

	require.Equal(t, len(lifecycle()), newEvents, "each messageId is applied exactly once")
	r := readRow(t, conn)
	require.Equal(t, "COMPLETED", r.status)
	require.Equal(t, 8, r.version)
	require.True(t, r.firstAcceptedAt.Equal(at(10)))
	require.True(t, r.openedAt.Equal(at(2)))
	require.Equal(t, 1, count(t, conn, "errand_facts"))
}

func name(s string) *string { return &s }

func TestEnrichmentRetriesANullSupplierName(t *testing.T) {
	store, conn := migratedStore(t)
	ctx := context.Background()
	applyAll(t, store, []Event{
		ev(TypeOpened, 2, StatusPendingReservation, StatusOpen, 2),
		accepted(3, 10, "auth0|cou1"),
	})
	supplier, building := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	now := at(100)

	due, err := store.EnrichmentDue(ctx, now, 10)
	require.NoError(t, err)
	require.Equal(t, []string{errandID}, due, "an accepted errand owes enrichment")

	// supplier-service was down: no supplier name.
	stored, err := store.StoreEnrichment(ctx, Enrichment{
		ErrandID: errandID, SupplierID: supplier, DeliveryBuildingID: building, DeliveryBuildingName: name("COM1"),
	}, now)
	require.NoError(t, err)
	require.True(t, stored)
	r := readRow(t, conn)
	require.True(t, r.enrichmentDue, "still due: the supplier name is unknown")
	require.Nil(t, r.supplierName)
	require.Equal(t, supplier, *r.supplierID)
	require.Equal(t, "COM1", *r.buildingName)

	due, err = store.EnrichmentDue(ctx, now.Add(-time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, due, "not retried before the retry time")
	due, err = store.EnrichmentDue(ctx, now.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Equal(t, []string{errandID}, due, "retried once the retry time has passed")

	stored, err = store.StoreEnrichment(ctx, Enrichment{
		ErrandID: errandID, SupplierID: supplier, SupplierName: name("Kopi Corner"),
		DeliveryBuildingID: building, DeliveryBuildingName: name("COM1"),
	}, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, stored)
	r = readRow(t, conn)
	require.False(t, r.enrichmentDue)
	require.Equal(t, "Kopi Corner", *r.supplierName)

	due, err = store.EnrichmentDue(ctx, now.Add(time.Hour), 10)
	require.NoError(t, err)
	require.Empty(t, due)

	// The terminal event refreshes: edits stop at accept, but the row is
	// enriched again to be sure.
	applyAll(t, store, []Event{ev(TypeCompleted, 8, StatusDelivered, StatusCompleted, 60)})
	require.True(t, readRow(t, conn).enrichmentDue)
}

func TestEnrichmentOfAnUnknownBuildingIsNotRetried(t *testing.T) {
	store, conn := migratedStore(t)
	applyAll(t, store, []Event{accepted(3, 10, "auth0|cou1")})

	_, err := store.StoreEnrichment(context.Background(), Enrichment{
		ErrandID: errandID, SupplierID: "11111111-1111-4111-8111-111111111111", SupplierName: name("Kopi Corner"),
		DeliveryBuildingID: "22222222-2222-4222-8222-222222222222", // no name
	}, at(100))
	require.NoError(t, err)

	r := readRow(t, conn)
	require.False(t, r.enrichmentDue, "a missing building name does not depend on another service")
	require.Nil(t, r.buildingName)
}

func TestStoreEnrichmentForAnUnknownErrand(t *testing.T) {
	store, _ := migratedStore(t)

	stored, err := store.StoreEnrichment(context.Background(), Enrichment{ErrandID: errandID}, at(1))

	require.NoError(t, err)
	require.False(t, stored)
	_, err = store.StoreEnrichment(context.Background(), Enrichment{ErrandID: "nope"}, at(1))
	require.ErrorIs(t, err, ErrInvalid)
}

func TestEnrichmentDueIsOldestFirstAndBounded(t *testing.T) {
	store, _ := migratedStore(t)
	other := "7a2d3c1f-9e5b-4f2c-8d4a-3b8e6f9c0d21"
	late := accepted(3, 50, "auth0|cou1")
	early := accepted(3, 10, "auth0|cou2")
	early.ErrandID = other
	early.MessageID = other + ":accepted:3"
	applyAll(t, store, []Event{late, early})

	due, err := store.EnrichmentDue(context.Background(), at(1000), 10)
	require.NoError(t, err)
	require.Equal(t, []string{other, errandID}, due)

	due, err = store.EnrichmentDue(context.Background(), at(1000), 1)
	require.NoError(t, err)
	require.Equal(t, []string{other}, due)
	_, err = store.EnrichmentDue(context.Background(), at(1000), 0)
	require.ErrorIs(t, err, ErrInvalid)
}
