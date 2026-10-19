package fence

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
)

const actor = "supplier-service@clients"

func TestAcquireIsIdempotentPerOperation_NFR7_3(t *testing.T) {
	service, db, _ := migratedService(t)
	ctx := context.Background()
	operationID, supplierID := newUUID(t), newUUID(t)

	fence, err := service.Acquire(ctx, actor, operationID, supplierID)
	require.NoError(t, err)
	require.Equal(t, Fence{OperationID: operationID, SupplierID: supplierID, State: StateOpen, HasActiveErrands: false}, fence)

	replay, err := service.Acquire(ctx, actor, operationID, supplierID)
	require.NoError(t, err)
	require.Equal(t, fence, replay)
	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM supplier_fences WHERE operation_id = $1`, operationID))

	inspected, err := service.Inspect(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, fence, inspected)

	// A replay after commit reports the committed fence and never reopens it.
	_, err = service.Commit(ctx, actor, operationID)
	require.NoError(t, err)
	replay, err = service.Acquire(ctx, actor, operationID, supplierID)
	require.NoError(t, err)
	require.Equal(t, StateCommitted, replay.State)

	// The same holds for a released fence.
	released := newUUID(t)
	_, err = service.Acquire(ctx, actor, released, supplierID)
	require.NoError(t, err)
	_, err = service.Release(ctx, actor, released)
	require.NoError(t, err)
	replay, err = service.Acquire(ctx, actor, released, supplierID)
	require.NoError(t, err)
	require.Equal(t, StateReleased, replay.State)
}

func TestAcquireCountsActiveErrands_F2_4_2(t *testing.T) {
	service, db, _ := migratedService(t)
	ctx := context.Background()
	supplierID := newUUID(t)

	insertErrand(t, db, "COMPLETED", supplierID)
	fence, err := service.Acquire(ctx, actor, newUUID(t), supplierID)
	require.NoError(t, err)
	require.False(t, fence.HasActiveErrands, "terminal errands do not block deletion")

	id := insertErrand(t, db, "ASSIGNED", supplierID)
	fence, err = service.Acquire(ctx, actor, newUUID(t), supplierID)
	require.NoError(t, err)
	require.True(t, fence.HasActiveErrands)

	// The count follows the errand, so Inspect sees it reach a terminal state.
	_, err = db.Exec(ctx, `UPDATE errands SET status = 'COMPLETED' WHERE id = $1`, id)
	require.NoError(t, err)
	fence, err = service.Inspect(ctx, fence.OperationID)
	require.NoError(t, err)
	require.False(t, fence.HasActiveErrands)

	// An active errand with a pending edit onto the supplier counts too.
	other := insertErrand(t, db, "OPEN", newUUID(t))
	_, err = db.Exec(ctx, `
		UPDATE errands
		SET pending_edit = jsonb_build_object('supplierId', $2::text),
		    pending_edit_key = gen_random_uuid()::text, pending_edit_at = clock_timestamp()
		WHERE id = $1`, other, supplierID)
	require.NoError(t, err)
	fence, err = service.Acquire(ctx, actor, newUUID(t), supplierID)
	require.NoError(t, err)
	require.True(t, fence.HasActiveErrands)
}

func TestAcquireRejectsAnotherSupplier_C2(t *testing.T) {
	service, db, _ := migratedService(t)
	ctx := context.Background()
	operationID, supplierID := newUUID(t), newUUID(t)

	_, err := service.Acquire(ctx, actor, operationID, supplierID)
	require.NoError(t, err)
	_, err = service.Acquire(ctx, actor, operationID, newUUID(t))
	requireCode(t, err, apperror.InvalidArgument)

	fence, err := service.Inspect(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, supplierID, fence.SupplierID, "the original fence is untouched")
	require.Equal(t, StateOpen, fence.State)
	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM supplier_fences`))
}

func TestCommitAndReleaseAreIdempotent_NFR7_3(t *testing.T) {
	service, _, _ := migratedService(t)
	ctx := context.Background()
	supplierID := newUUID(t)

	committed := newUUID(t)
	_, err := service.Acquire(ctx, actor, committed, supplierID)
	require.NoError(t, err)
	for range 2 {
		fence, err := service.Commit(ctx, actor, committed)
		require.NoError(t, err)
		require.Equal(t, Fence{OperationID: committed, SupplierID: supplierID, State: StateCommitted}, fence)
	}
	_, err = service.Release(ctx, actor, committed)
	requireCode(t, err, apperror.InvalidTransition)
	fence, err := service.Inspect(ctx, committed)
	require.NoError(t, err)
	require.Equal(t, StateCommitted, fence.State, "a committed fence is never lifted")

	released := newUUID(t)
	_, err = service.Acquire(ctx, actor, released, supplierID)
	require.NoError(t, err)
	for range 2 {
		fence, err := service.Release(ctx, actor, released)
		require.NoError(t, err)
		require.Equal(t, StateReleased, fence.State)
	}
	_, err = service.Commit(ctx, actor, released)
	requireCode(t, err, apperror.InvalidTransition)
	fence, err = service.Inspect(ctx, released)
	require.NoError(t, err)
	require.Equal(t, StateReleased, fence.State)
}

func TestMissingFence_C2(t *testing.T) {
	service, _, _ := migratedService(t)
	ctx := context.Background()
	missing := newUUID(t)

	_, err := service.Inspect(ctx, missing)
	requireCode(t, err, apperror.FenceNotFound)
	_, err = service.Commit(ctx, actor, missing)
	requireCode(t, err, apperror.FenceNotFound)
	_, err = service.Release(ctx, actor, missing)
	requireCode(t, err, apperror.FenceNotFound)
}

func TestMutationsAreAudited_F6_5(t *testing.T) {
	service, db, _ := migratedService(t)
	ctx := context.Background()
	operationID, supplierID := newUUID(t), newUUID(t)

	_, err := service.Acquire(ctx, actor, operationID, supplierID)
	require.NoError(t, err)
	_, err = service.Acquire(ctx, actor, operationID, newUUID(t))
	require.Error(t, err)
	_, err = service.Commit(ctx, actor, operationID)
	require.NoError(t, err)
	_, err = service.Release(ctx, actor, operationID)
	require.Error(t, err)
	_, err = service.Inspect(ctx, operationID)
	require.NoError(t, err)

	entries, err := opsrepo.New(db).ListAudit(ctx, opsrepo.AuditFilter{TargetID: operationID, Limit: 10})
	require.NoError(t, err)
	type row struct {
		action  string
		outcome opsrepo.AuditOutcome
		reason  string
	}
	var got []row
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		require.Equal(t, actor, entry.ActorID)
		require.Equal(t, AuditTargetType, entry.TargetType)
		got = append(got, row{entry.Action, entry.Outcome, entry.Reason})
	}
	require.Equal(t, []row{
		{AuditActionAcquire, opsrepo.AuditSucceeded, ""},
		{AuditActionAcquire, opsrepo.AuditRejected, string(apperror.InvalidArgument)},
		{AuditActionCommit, opsrepo.AuditSucceeded, ""},
		{AuditActionRelease, opsrepo.AuditRejected, string(apperror.InvalidTransition)},
	}, got, "Inspect is not audited")
}

func TestRoutesAgainstPostgres_C2(t *testing.T) {
	service, _, _ := migratedService(t)
	h := newHarness(t, service)
	operationID, supplierID := newUUID(t), newUUID(t)

	recorder := h.do(t, http.MethodPost, fencePath(operationID, "acquire"), h.service, `{"supplierId":"`+supplierID+`"}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"operationId":"`+operationID+`","supplierId":"`+supplierID+`","state":"open","hasActiveErrands":false}`, recorder.Body.String())

	recorder = h.do(t, http.MethodPost, fencePath(operationID, "acquire"), h.service, `{"supplierId":"`+newUUID(t)+`"}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code)

	recorder = h.do(t, http.MethodPost, fencePath(operationID, "release"), h.service, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"operationId":"`+operationID+`","supplierId":"`+supplierID+`","state":"released","hasActiveErrands":false}`, recorder.Body.String())

	recorder = h.do(t, http.MethodPost, fencePath(operationID, "commit"), h.service, "")
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Equal(t, apperror.InvalidTransition, decodeError(t, recorder).Code)

	recorder = h.do(t, http.MethodGet, fencePath(newUUID(t), ""), h.service, "")
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Equal(t, apperror.FenceNotFound, decodeError(t, recorder).Code)
}

// TestAcquireRacesWithCreationAndEdits checks the protocol in
// docs/contracts.md ("Why there is no gap"): every errand creation or
// supplier-changing edit running concurrently with Acquire either commits
// before the fence, and Acquire counts it, or is rejected by the fence check.
// Never both missed.
func TestAcquireRacesWithCreationAndEdits_F2_4_2(t *testing.T) {
	_, db, connect := migratedService(t)
	ctx := context.Background()
	const rounds = 40
	const writers = 8
	// One connection per concurrent transaction: a *pgx.Conn is not safe for
	// concurrent use.
	service := NewService(opsrepo.New(connect()), discardLogger())
	conns := make([]*pgx.Conn, writers)
	for i := range conns {
		conns[i] = connect()
	}

	for round := range rounds {
		supplierID, operationID := newUUID(t), newUUID(t)
		// Errands on other suppliers that the edit writers move onto supplierID.
		editable := make([]string, writers)
		for i := range editable {
			editable[i] = insertErrand(t, db, "OPEN", newUUID(t))
		}

		type outcome struct {
			kind      writeKind
			committed bool
			at        time.Time // when the write landed, if committed
			err       error
		}
		outcomes := make([]outcome, writers)
		var fence Fence
		var acquireErr error
		var wg sync.WaitGroup
		start := make(chan struct{})

		for i := range writers {
			kind := writeKind(i % 3)
			wg.Go(func() {
				<-start
				jitter(t, 3*time.Millisecond)
				at, committed, err := simulateWrite(ctx, conns[i], kind, supplierID, editable[i])
				outcomes[i] = outcome{kind: kind, committed: committed, at: at, err: err}
			})
		}
		wg.Go(func() {
			<-start
			jitter(t, 3*time.Millisecond)
			fence, acquireErr = service.Acquire(ctx, actor, operationID, supplierID)
		})
		close(start)
		wg.Wait()

		require.NoError(t, acquireErr, "round %d", round)
		var fenceCreated time.Time
		require.NoError(t, db.QueryRow(ctx,
			`SELECT created_at FROM supplier_fences WHERE operation_id = $1`, operationID,
		).Scan(&fenceCreated))

		committed := 0
		for i, o := range outcomes {
			require.NoError(t, o.err, "round %d writer %d (%s)", round, i, o.kind)
			if o.committed {
				committed++
				require.True(t, o.at.Before(fenceCreated),
					"round %d writer %d (%s) committed after the fence was created", round, i, o.kind)
			}
		}
		require.Equal(t, committed > 0, fence.HasActiveErrands,
			"round %d: %d writes committed but hasActiveErrands=%v", round, committed, fence.HasActiveErrands)

		// Once Acquire has returned, every later write is rejected.
		for kind := range writeKind(3) {
			_, ok, err := simulateWrite(ctx, db, kind, supplierID, insertErrand(t, db, "OPEN", newUUID(t)))
			require.NoError(t, err)
			require.False(t, ok, "round %d: %s after Acquire was not rejected", round, kind)
		}
	}
}

type writeKind int

const (
	writeCreate      writeKind = iota // Store.Create
	writeEdit                         // an edit applied at once (Changes.Details)
	writePendingEdit                  // a reward edit stored as pending_edit
)

func (k writeKind) String() string {
	return [...]string{"create", "edit", "pending edit"}[k]
}

// simulateWrite is the minimal SQL of Person A's Store.Create and
// supplier-changing Store.Apply (docs/contracts.md, "Errand creation" and
// "Supplier-changing edits"). It returns committed=false when the fence check
// rejected the write, and the database time at which the write landed.
func simulateWrite(ctx context.Context, db *pgx.Conn, kind writeKind, supplierID, errandID string) (at time.Time, committed bool, err error) {
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('supplier:' || $1::uuid::text, 0))`, supplierID); err != nil {
			return err
		}
		var fenced bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM supplier_fences WHERE supplier_id = $1 AND state IN ('OPEN', 'COMMITTED'))`,
			supplierID,
		).Scan(&fenced); err != nil {
			return err
		}
		if fenced {
			return errFenced
		}
		// Widen the window between the check and the write, where a missing
		// lock would let Acquire slip through.
		if _, err := tx.Exec(ctx, `SELECT pg_sleep(random() * 0.002)`); err != nil {
			return err
		}
		switch kind {
		case writeCreate:
			return tx.QueryRow(ctx, insertErrandSQL, "PENDING_RESERVATION", supplierID, nil).Scan(new(string), &at)
		case writeEdit:
			return tx.QueryRow(ctx, `
				UPDATE errands SET supplier_id = $2, version = version + 1, updated_at = clock_timestamp()
				WHERE id = $1 RETURNING updated_at`, errandID, supplierID).Scan(&at)
		default:
			return tx.QueryRow(ctx, `
				UPDATE errands
				SET pending_edit = jsonb_build_object('supplierId', $2::text),
				    pending_edit_key = gen_random_uuid()::text, pending_edit_at = clock_timestamp()
				WHERE id = $1 RETURNING pending_edit_at`, errandID, supplierID).Scan(&at)
		}
	})
	if errors.Is(err, errFenced) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return at, true, nil
}

var errFenced = errors.New("supplier is fenced")

const insertErrandSQL = `
	INSERT INTO errands (
		status, requester_id, courier_id, supplier_id, supplier_version_id,
		delivery_building_id, delivery_building_snapshot, delivery_detail, items,
		contact_name, contact_mobile, reward, acceptance_deadline, create_idempotency_key
	) VALUES (
		$1, 'requester-1', $3, $2, gen_random_uuid(),
		gen_random_uuid(), '{}', 'Room 1', '[{"name": "coffee"}]',
		'Contact', '91234567', 10, clock_timestamp() + interval '1 hour', gen_random_uuid()::text
	) RETURNING id::text, created_at`

// insertErrand writes an errand fixture directly; the fence never writes
// errands.
func insertErrand(t *testing.T, db *pgx.Conn, status, supplierID string) string {
	t.Helper()
	var id string
	require.NoError(t, db.QueryRow(context.Background(), insertErrandSQL, status, supplierID, "courier-1").Scan(&id, new(time.Time)))
	return id
}

// migratedService returns a Service on a new schema with every migration
// applied, a connection to the same schema for fixtures, and connect, which
// opens further connections to it (closed when the test ends).
func migratedService(t *testing.T) (*Service, *pgx.Conn, func() *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL fence integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := "order_fence_" + strings.ReplaceAll(newUUID(t), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = conn.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = conn.Close(cleanupCtx)
	})
	_, err = conn.Exec(ctx, "SET search_path TO "+quotedSchema)
	require.NoError(t, err)
	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = conn.Exec(ctx, up)
	require.NoError(t, err)

	config, err := pgx.ParseConfig(databaseURL)
	require.NoError(t, err)
	config.RuntimeParams["search_path"] = schema
	connect := func() *pgx.Conn {
		t.Helper()
		connectCtx, connectCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer connectCancel()
		extra, err := pgx.ConnectConfig(connectCtx, config.Copy())
		require.NoError(t, err)
		// Registered after the schema cleanup, so it runs first.
		t.Cleanup(func() { _ = extra.Close(context.Background()) })
		return extra
	}
	return NewService(opsrepo.New(connect()), discardLogger()), conn, connect
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func requireCode(t *testing.T, err error, code apperror.Code) {
	t.Helper()
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr), "expected an apperror, got %v", err)
	require.Equal(t, code, appErr.Code)
}

func countRows(t *testing.T, db *pgx.Conn, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(context.Background(), query, args...).Scan(&count))
	return count
}

func jitter(t *testing.T, limit time.Duration) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		t.Error(err)
		return
	}
	time.Sleep(time.Duration(n.Int64()))
}

func newUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	_, err := rand.Read(value)
	require.NoError(t, err)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32])
}
