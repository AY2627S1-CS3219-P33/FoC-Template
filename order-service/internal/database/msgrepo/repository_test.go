package msgrepo_test

import (
	"context"
	"crypto/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// testDatabase migrates a fresh schema so tests never share rows.
func testDatabase(t *testing.T) (*pgxpool.Pool, *msgrepo.Repository) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	schema := pgx.Identifier{"order_msgrepo_" + strings.ToLower(rand.Text())}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
	})
	_, err = admin.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	sql, err := migrations.Up()
	require.NoError(t, err)
	_, err = admin.Exec(ctx, sql)
	require.NoError(t, err)
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, msgrepo.New(pool)
}

func insertRow(t *testing.T, pool *pgxpool.Pool, kind string) (int64, string) {
	t.Helper()
	ctx := context.Background()
	var errandID string
	err := pool.QueryRow(ctx, `INSERT INTO errands(status,requester_id,supplier_id,supplier_version_id,delivery_building_id,delivery_building_snapshot,delivery_detail,items,contact_name,contact_mobile,reward,acceptance_deadline,create_idempotency_key)
 VALUES('PENDING_RESERVATION','auth0|req1',gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),'{}','lobby','[{"description":"coffee"}]','Alex','91234567',5,clock_timestamp()+interval '1 hour',gen_random_uuid()::text) RETURNING id::text`).Scan(&errandID)
	require.NoError(t, err)
	key := errandID + ":" + kind
	var id int64
	err = pool.QueryRow(ctx, `INSERT INTO outbox(kind,idempotency_key,errand_id,payload) VALUES($1,$2,$3,'{}') RETURNING id`, kind, key, errandID).Scan(&id)
	require.NoError(t, err)
	return id, key
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

func status(t *testing.T, pool *pgxpool.Pool, id int64) string {
	t.Helper()
	var s string
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT status FROM outbox WHERE id=$1", id).Scan(&s))
	return s
}

func claimOne(t *testing.T, repo *msgrepo.Repository, worker string, credit bool) msgrepo.Row {
	t.Helper()
	rows, err := repo.Claim(context.Background(), worker, credit, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0]
}

func TestConcurrentClaimsNeverReturnTheSameRow(t *testing.T) {
	pool, repo := testDatabase(t)
	for range 20 {
		insertRow(t, pool, errand.KindCreditReserve)
	}
	var mu sync.Mutex
	seen := map[int64]string{}
	var wg sync.WaitGroup
	for _, worker := range []string{"a", "b"} {
		wg.Go(func() {
			for {
				rows, err := repo.Claim(context.Background(), worker, true, 3, time.Minute)
				require.NoError(t, err)
				if len(rows) == 0 {
					return
				}
				mu.Lock()
				for _, row := range rows {
					require.NotContains(t, seen, row.ID, "row claimed twice")
					seen[row.ID] = worker
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Len(t, seen, 20)
}

func TestClaimSeparatesCreditAndEventRows(t *testing.T) {
	pool, repo := testDatabase(t)
	creditID, _ := insertRow(t, pool, errand.KindCreditReserve)
	eventID, _ := insertRow(t, pool, errand.ErrandPlaced.Kind())
	require.Equal(t, eventID, claimOne(t, repo, "w", false).ID)
	require.Equal(t, creditID, claimOne(t, repo, "w", true).ID)
}

func TestExpiredLeaseIsReclaimedAndStaleWritesChangeNothing(t *testing.T) {
	pool, repo := testDatabase(t)
	ctx := context.Background()
	id, _ := insertRow(t, pool, errand.KindCreditReserve)
	old := claimOne(t, repo, "worker", true)
	rows, err := repo.Claim(ctx, "other", true, 1, time.Minute)
	require.NoError(t, err)
	require.Empty(t, rows, "a live lease is not reclaimed")

	exec(t, pool, "UPDATE outbox SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", id)
	require.ErrorIs(t, repo.MarkSent(ctx, old), msgrepo.ErrLeaseLost, "an expired lease cannot mark sent")
	require.Equal(t, msgrepo.StatusPending, status(t, pool, id))

	// The same worker name reclaiming the row still fences the old claim by attempts.
	current := claimOne(t, repo, "worker", true)
	require.Equal(t, old.Attempts+1, current.Attempts)
	require.ErrorIs(t, repo.MarkSent(ctx, old), msgrepo.ErrLeaseLost)
	require.ErrorIs(t, repo.MarkFailed(ctx, old, "OLD_WORKER"), msgrepo.ErrLeaseLost)
	require.ErrorIs(t, repo.Reschedule(ctx, old, 0, "OLD_WORKER"), msgrepo.ErrLeaseLost)
	require.Equal(t, msgrepo.StatusPending, status(t, pool, id))

	require.NoError(t, repo.MarkSent(ctx, current))
	require.Equal(t, msgrepo.StatusSent, status(t, pool, id))
	require.ErrorIs(t, repo.MarkSent(ctx, current), msgrepo.ErrLeaseLost, "a second MarkSent is reported")
}

func TestRescheduleDelaysAndKeepsFailureCode(t *testing.T) {
	pool, repo := testDatabase(t)
	ctx := context.Background()
	id, key := insertRow(t, pool, errand.KindCreditRelease)
	require.NoError(t, repo.Reschedule(ctx, claimOne(t, repo, "w", true), time.Hour, "PUBLISH_FAILED"))
	rows, err := repo.Claim(ctx, "w", true, 1, time.Minute)
	require.NoError(t, err)
	require.Empty(t, rows, "not due yet")
	record, err := repo.GetByKey(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "PUBLISH_FAILED", record.LastFailureCode)
	require.EqualValues(t, 1, record.Attempts)

	exec(t, pool, "UPDATE outbox SET next_attempt_at=clock_timestamp() WHERE id=$1", id)
	row := claimOne(t, repo, "w", true)
	require.Equal(t, key, row.IdempotencyKey)
	require.EqualValues(t, 2, row.Attempts)
	require.NoError(t, repo.MarkFailed(ctx, row, "ATTEMPTS_EXHAUSTED"))
	require.Equal(t, msgrepo.StatusFailed, status(t, pool, id))
}

func TestConfirmByKeyIsIdempotentAndTerminal(t *testing.T) {
	pool, repo := testDatabase(t)
	ctx := context.Background()
	id, key := insertRow(t, pool, errand.KindCreditReserve)
	row := claimOne(t, repo, "w", true)

	// A fast result can arrive before MarkSent.
	changed, err := repo.ConfirmByKey(ctx, key)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = repo.ConfirmByKey(ctx, key)
	require.NoError(t, err)
	require.False(t, changed, "a second confirmation is a no-op")
	require.ErrorIs(t, repo.MarkSent(ctx, row), msgrepo.ErrLeaseLost)
	changed, err = repo.MarkFailedByKey(ctx, key, "LATE_FAILURE")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, msgrepo.StatusConfirmed, status(t, pool, id))

	eventID, eventKey := insertRow(t, pool, errand.ErrandPlaced.Kind())
	changed, err = repo.ConfirmByKey(ctx, eventKey)
	require.NoError(t, err)
	require.False(t, changed, "events are never confirmed")
	require.Equal(t, msgrepo.StatusPending, status(t, pool, eventID))
}

func TestMarkFailedByKey(t *testing.T) {
	pool, repo := testDatabase(t)
	ctx := context.Background()
	id, key := insertRow(t, pool, errand.KindCreditTransfer)
	require.NoError(t, repo.MarkSent(ctx, claimOne(t, repo, "w", true)))
	changed, err := repo.MarkFailedByKey(ctx, key, "CONTRACT_VIOLATION")
	require.NoError(t, err)
	require.True(t, changed)
	record, err := repo.GetByKey(ctx, key)
	require.NoError(t, err)
	require.Equal(t, msgrepo.StatusFailed, record.Status)
	require.Equal(t, "CONTRACT_VIOLATION", record.LastFailureCode)
	changed, err = repo.MarkFailedByKey(ctx, key, "AGAIN")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, msgrepo.StatusFailed, status(t, pool, id))
}

func TestResendUnconfirmedOnlyTouchesOldSentCreditRows(t *testing.T) {
	pool, repo := testDatabase(t)
	ctx := context.Background()
	send := func(kind string) (int64, string) {
		id, key := insertRow(t, pool, kind)
		rows, err := repo.Claim(ctx, "w", strings.HasPrefix(kind, "credit."), 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.NoError(t, repo.MarkSent(ctx, rows[0]))
		return id, key
	}
	oldID, oldKey := send(errand.KindCreditRelease)
	recentID, _ := send(errand.KindCreditTransfer)
	eventID, _ := send(errand.ErrandPlaced.Kind())
	confirmedID, confirmedKey := send(errand.KindCreditReserve)
	_, err := repo.ConfirmByKey(ctx, confirmedKey)
	require.NoError(t, err)
	pendingID, pendingKey := insertRow(t, pool, errand.KindCreditAdjust)
	exec(t, pool, "UPDATE outbox SET sent_at=clock_timestamp()-interval '2 hours' WHERE id = ANY($1)", []int64{oldID, eventID, confirmedID})

	resent, err := repo.ResendUnconfirmed(ctx, time.Hour, 10)
	require.NoError(t, err)
	require.Len(t, resent, 1)
	require.Equal(t, oldID, resent[0].ID)
	require.Equal(t, oldKey, resent[0].IdempotencyKey)
	require.Equal(t, msgrepo.StatusPending, status(t, pool, oldID))
	require.Equal(t, msgrepo.StatusSent, status(t, pool, recentID))
	require.Equal(t, msgrepo.StatusSent, status(t, pool, eventID))
	require.Equal(t, msgrepo.StatusConfirmed, status(t, pool, confirmedID))
	require.Equal(t, msgrepo.StatusPending, status(t, pool, pendingID))

	rows, err := repo.Claim(ctx, "w", true, 5, time.Minute)
	require.NoError(t, err)
	var keys []string
	for _, row := range rows {
		keys = append(keys, row.IdempotencyKey)
	}
	require.ElementsMatch(t, []string{oldKey, pendingKey}, keys, "re-sent with the same key")
}

func TestResendUnconfirmedRespectsLimit(t *testing.T) {
	pool, repo := testDatabase(t)
	ctx := context.Background()
	for range 3 {
		insertRow(t, pool, errand.KindCreditRelease)
	}
	rows, err := repo.Claim(ctx, "w", true, 3, time.Minute)
	require.NoError(t, err)
	for _, row := range rows {
		require.NoError(t, repo.MarkSent(ctx, row))
	}
	exec(t, pool, "UPDATE outbox SET sent_at=clock_timestamp()-interval '2 hours'")
	resent, err := repo.ResendUnconfirmed(ctx, time.Hour, 2)
	require.NoError(t, err)
	require.Len(t, resent, 2)
}

func TestGetByKeyNotFound(t *testing.T) {
	_, repo := testDatabase(t)
	_, err := repo.GetByKey(context.Background(), "missing")
	require.ErrorIs(t, err, msgrepo.ErrNotFound)
}

func TestListFailedPagesOldestFirst(t *testing.T) {
	pool, repo := testDatabase(t)
	ctx := context.Background()
	var failed []int64
	for range 3 {
		id, _ := insertRow(t, pool, errand.KindCreditRelease)
		failed = append(failed, id)
	}
	insertRow(t, pool, errand.KindCreditRelease) // stays PENDING
	exec(t, pool, "UPDATE outbox SET status='FAILED', last_failure_code='ATTEMPTS_EXHAUSTED' WHERE id = ANY($1)", failed)

	page, err := repo.ListFailed(ctx, 2, 0)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, failed[:2], []int64{page[0].ID, page[1].ID})
	require.Equal(t, "ATTEMPTS_EXHAUSTED", page[0].LastFailureCode)
	page, err = repo.ListFailed(ctx, 2, page[1].ID)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, failed[2], page[0].ID)
	page, err = repo.ListFailed(ctx, 2, page[0].ID)
	require.NoError(t, err)
	require.Empty(t, page)
}

func TestRequeueKeepsKeyResetsAttemptsAndAudits(t *testing.T) {
	pool, repo := testDatabase(t)
	ctx := context.Background()
	id, key := insertRow(t, pool, errand.KindCreditRelease)
	require.NoError(t, repo.MarkFailed(ctx, claimOne(t, repo, "w", true), "ATTEMPTS_EXHAUSTED"))

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	txRepo := msgrepo.New(tx)
	record, err := txRepo.Requeue(ctx, id)
	require.NoError(t, err)
	require.NoError(t, txRepo.InsertRequeueAudit(ctx, "auth0|admin", id, "credit-service fixed", msgrepo.AuditSucceeded))
	require.NoError(t, tx.Commit(ctx))

	require.Equal(t, key, record.IdempotencyKey)
	require.Equal(t, msgrepo.StatusPending, record.Status)
	require.Zero(t, record.Attempts)
	row := claimOne(t, repo, "w", true)
	require.Equal(t, key, row.IdempotencyKey)
	require.EqualValues(t, 1, row.Attempts)

	var actor, action, targetType, targetID, reason, outcome string
	require.NoError(t, pool.QueryRow(ctx, "SELECT actor_id, action, target_type, target_id, reason, outcome FROM admin_audit").Scan(&actor, &action, &targetType, &targetID, &reason, &outcome))
	require.Equal(t, []string{"auth0|admin", "OUTBOX_REQUEUE", "OUTBOX", strconv.FormatInt(id, 10), "credit-service fixed", msgrepo.AuditSucceeded}, []string{actor, action, targetType, targetID, reason, outcome})

	_, err = repo.Requeue(ctx, id)
	require.ErrorIs(t, err, msgrepo.ErrNotFailed)
	_, err = repo.Requeue(ctx, id+1000)
	require.ErrorIs(t, err, msgrepo.ErrNotFound)
}

func TestInvalidParametersAreRejected(t *testing.T) {
	repo := msgrepo.New(nil)
	ctx := context.Background()
	_, err := repo.Claim(ctx, "", true, 1, time.Minute)
	require.Error(t, err)
	_, err = repo.Claim(ctx, "w", true, 501, time.Minute)
	require.Error(t, err)
	_, err = repo.ResendUnconfirmed(ctx, 0, 10)
	require.Error(t, err)
	_, err = repo.ListFailed(ctx, 0, 0)
	require.Error(t, err)
	_, err = repo.ConfirmByKey(ctx, "")
	require.Error(t, err)
	_, err = repo.MarkFailedByKey(ctx, "k", "")
	require.Error(t, err)
	require.Error(t, repo.InsertRequeueAudit(ctx, "a", 1, "r", "MAYBE"))
}
