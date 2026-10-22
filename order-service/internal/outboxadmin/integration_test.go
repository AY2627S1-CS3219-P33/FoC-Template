package outboxadmin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/messaging"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func testDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	schema := pgx.Identifier{"order_outboxadmin_" + strings.ToLower(rand.Text())}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
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
	return pool
}
func TestReplayIsAuditedAndCreditIsAppliedOnce(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	var eid string
	err := pool.QueryRow(ctx, `INSERT INTO errands(status,requester_id,supplier_id,supplier_version_id,delivery_building_id,delivery_building_snapshot,delivery_detail,items,contact_name,contact_mobile,reward,acceptance_deadline,create_idempotency_key) VALUES('PENDING_RESERVATION','requester',gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),'{}','lobby','[{"description":"coffee"}]','Alex','91234567',5,clock_timestamp()+interval '1 hour','create') RETURNING id::text`).Scan(&eid)
	require.NoError(t, err)
	key := eid + ":reserve"
	payload, err := json.Marshal(errand.CreditCommand{ErrandID: eid, ErrandVersion: 1, RequesterID: "requester", Amount: 5})
	require.NoError(t, err)
	var id int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO outbox(kind,idempotency_key,errand_id,payload) VALUES('credit.reserve',$1,$2,$3) RETURNING id`, key, eid, payload).Scan(&id))
	repo := msgrepo.New(pool)
	pub := testfakes.NewFakePublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher, err := messaging.NewDispatcher(repo, pub, testfakes.NewFakeSettings(), logger, messaging.DispatcherConfig{PollInterval: time.Millisecond})
	require.NoError(t, err)
	credit := testfakes.NewFakeCreditService(testfakes.NewFakeClock(time.Now()))
	credit.SetBalance("requester", 20)
	dispatch := func() {
		t.Helper()
		before := len(pub.Messages())
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- dispatcher.Run(runCtx) }()
		require.Eventually(t, func() bool {
			r, e := repo.GetByKey(ctx, key)
			return e == nil && r.Status == msgrepo.StatusSent && len(pub.Messages()) > before
		}, 5*time.Second, time.Millisecond)
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		_, err := credit.ConsumeFrom(pub)
		require.NoError(t, err)
	}
	dispatch()
	require.Equal(t, 15, credit.Balance("requester"))
	_, err = repo.MarkFailedByKey(ctx, key, "RESULT_LOST")
	require.NoError(t, err)
	store := NewStore(pool)
	row, err := store.Requeue(ctx, "admin", id, "retry lost result")
	require.NoError(t, err)
	require.Equal(t, key, row.IdempotencyKey)
	require.Zero(t, row.Attempts)
	dispatch()
	require.Equal(t, 15, credit.Balance("requester"))
	require.Len(t, credit.Received(), 2)
	var audits int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE action='OUTBOX_REQUEUE' AND outcome='SUCCEEDED'`).Scan(&audits))
	require.Equal(t, 1, audits)
	// An audit failure must roll back the status reset as well.
	_, err = repo.MarkFailedByKey(ctx, key, "RESULT_LOST")
	require.NoError(t, err)
	_, err = store.Requeue(ctx, "", id, "bad actor")
	require.Error(t, err)
	row, err = repo.GetByKey(ctx, key)
	require.NoError(t, err)
	require.Equal(t, msgrepo.StatusFailed, row.Status)
}
