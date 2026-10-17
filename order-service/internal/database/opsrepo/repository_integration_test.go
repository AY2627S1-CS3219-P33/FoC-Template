package opsrepo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
)

func TestInsertReminderDuplicateIsNoOp(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	errandID := insertErrand(t, conn, errandRow{status: "DELIVERED", courier: "courier-1"})
	key := errand.IdempotencyKey(errandID, "verify-reminder", 6)
	row := errand.OutboxRow{
		Kind:           errand.ErrandVerificationReminder.Kind(),
		IdempotencyKey: key,
		ErrandID:       errandID,
		Payload:        map[string]any{"errandId": errandID, "errandVersion": 6},
	}

	inserted, err := repo.InsertReminder(ctx, row)
	require.NoError(t, err)
	require.True(t, inserted)

	replay := row
	replay.Payload = map[string]any{"errandId": errandID, "errandVersion": 99}
	inserted, err = repo.InsertReminder(ctx, replay)
	require.NoError(t, err)
	require.False(t, inserted)

	var count int
	var payload []byte
	var status string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) OVER (), payload, status FROM outbox WHERE idempotency_key = $1`, key,
	).Scan(&count, &payload, &status))
	require.Equal(t, 1, count)
	require.Equal(t, "PENDING", status)
	require.JSONEq(t, `{"errandId": "`+errandID+`", "errandVersion": 6}`, string(payload))

	pickup := row
	pickup.Kind = errand.ErrandPickupDeadlineApproaching.Kind()
	pickup.IdempotencyKey = errand.IdempotencyKey(errandID, "pickup-reminder", 4)
	inserted, err = repo.InsertReminder(ctx, pickup)
	require.NoError(t, err)
	require.True(t, inserted)

	notReminder := row
	notReminder.Kind = errand.ErrandCompleted.Kind()
	notReminder.IdempotencyKey = errandID + ":completed:7"
	_, err = repo.InsertReminder(ctx, notReminder)
	require.ErrorIs(t, err, ErrInvalidArgument)

	unknownErrand := row
	unknownErrand.ErrandID = newUUID(t)
	unknownErrand.IdempotencyKey = unknownErrand.ErrandID + ":verify-reminder:1"
	_, err = repo.InsertReminder(ctx, unknownErrand)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFenceStateChanges(t *testing.T) {
	repo, _ := migratedRepository(t)
	ctx := context.Background()
	supplierID := newUUID(t)
	committed := newUUID(t)

	fence, created, err := repo.CreateFence(ctx, committed, supplierID)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, Fence{OperationID: committed, SupplierID: supplierID, State: FenceOpen,
		CreatedAt: fence.CreatedAt, UpdatedAt: fence.UpdatedAt}, fence)

	replayed, created, err := repo.CreateFence(ctx, committed, supplierID)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, fence, replayed)

	_, _, err = repo.CreateFence(ctx, committed, newUUID(t))
	require.ErrorIs(t, err, ErrFenceSupplierMismatch)

	fence, err = repo.CloseFence(ctx, committed, FenceCommitted)
	require.NoError(t, err)
	require.Equal(t, FenceCommitted, fence.State)
	fence, err = repo.CloseFence(ctx, committed, FenceCommitted)
	require.NoError(t, err, "committing twice is idempotent")
	require.Equal(t, FenceCommitted, fence.State)
	fence, err = repo.CloseFence(ctx, committed, FenceReleased)
	require.ErrorIs(t, err, ErrFenceStateConflict, "a committed fence is never released")
	require.Equal(t, FenceCommitted, fence.State)

	released := newUUID(t)
	_, _, err = repo.CreateFence(ctx, released, supplierID)
	require.NoError(t, err)
	fence, err = repo.CloseFence(ctx, released, FenceReleased)
	require.NoError(t, err)
	require.Equal(t, FenceReleased, fence.State)
	_, err = repo.CloseFence(ctx, released, FenceReleased)
	require.NoError(t, err, "releasing twice is idempotent")
	_, err = repo.CloseFence(ctx, released, FenceCommitted)
	require.ErrorIs(t, err, ErrFenceStateConflict)

	got, err := repo.GetFence(ctx, released)
	require.NoError(t, err)
	require.Equal(t, FenceReleased, got.State)

	missing := newUUID(t)
	_, err = repo.GetFence(ctx, missing)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = repo.CloseFence(ctx, missing, FenceCommitted)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = repo.CloseFence(ctx, committed, FenceOpen)
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = repo.GetFence(ctx, "not-a-uuid")
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestCountActiveErrandsForSupplier(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	supplierID, otherSupplier := newUUID(t), newUUID(t)

	count, err := repo.CountActiveErrandsForSupplier(ctx, supplierID)
	require.NoError(t, err)
	require.Zero(t, count)

	// Every active state counts; terminal states do not.
	for _, status := range errand.Statuses() {
		insertErrand(t, conn, errandRow{status: string(status), supplier: supplierID, courier: "courier-1"})
	}
	// Another supplier's errand counts only through a pending edit onto this
	// supplier.
	insertErrand(t, conn, errandRow{status: "OPEN", supplier: otherSupplier, pendingEditSupplier: supplierID})
	insertErrand(t, conn, errandRow{status: "OPEN", supplier: otherSupplier})
	insertErrand(t, conn, errandRow{status: "CANCELLED", supplier: otherSupplier, pendingEditSupplier: supplierID})

	count, err = repo.CountActiveErrandsForSupplier(ctx, supplierID)
	require.NoError(t, err)
	require.EqualValues(t, 6+1, count, "six active states plus one pending edit")

	count, err = repo.CountActiveErrandsForSupplier(ctx, otherSupplier)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)

	// The fence count runs after the exclusive lock in one transaction.
	err = repo.InTx(ctx, func(tx *Repository) error {
		if err := tx.LockSupplier(ctx, supplierID); err != nil {
			return err
		}
		if _, _, err := tx.CreateFence(ctx, newUUID(t), supplierID); err != nil {
			return err
		}
		count, err = tx.CountActiveErrandsForSupplier(ctx, supplierID)
		return err
	})
	require.NoError(t, err)
	require.EqualValues(t, 7, count)
}

func TestSeedDefaultsTwiceLeavesOneRowPerSetting(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()

	inserted, err := repo.SeedDefaults(ctx)
	require.NoError(t, err)
	require.Equal(t, len(settingFields), inserted)

	inserted, err = repo.SeedDefaults(ctx)
	require.NoError(t, err)
	require.Zero(t, inserted)

	var rows, keys int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT key) FROM settings`).Scan(&rows, &keys))
	require.Equal(t, len(settingFields), rows)
	require.Equal(t, len(settingFields), keys)

	settings, err := repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, errand.DefaultSystemSettings(), settings)

	var pickup string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT value #>> '{}' FROM settings WHERE key = $1`, KeyPickupAfter).Scan(&pickup))
	require.Equal(t, "2h0m0s", pickup)
}

func TestSettingsUpdate(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()

	// Nothing stored yet: every key has its default.
	settings, err := repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, errand.DefaultSystemSettings(), settings)

	_, err = repo.SeedDefaults(ctx)
	require.NoError(t, err)

	changed := errand.DefaultSystemSettings()
	changed.Deadlines.PickupAfter = 90 * time.Minute
	changed.WithdrawalRestriction.Count = 5
	require.NoError(t, repo.Update(ctx, changed, "auth0|admin1"))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, changed, got)

	rows, err := conn.Query(ctx, `SELECT key FROM settings WHERE updated_by = 'auth0|admin1' ORDER BY key`)
	require.NoError(t, err)
	updatedBy, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.Equal(t, []string{KeyPickupAfter, KeyWithdrawalRestrictionCount}, updatedBy,
		"unchanged keys keep their metadata")

	// A later seed never overwrites an admin's value.
	_, err = repo.SeedDefaults(ctx)
	require.NoError(t, err)
	got, err = repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, changed, got)

	bad := changed
	bad.CancellationFlag.Count = 0
	bad.EditTimeout = -time.Minute
	err = repo.Update(ctx, bad, "auth0|admin1")
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, apperror.ValidationFailed, appErr.Code)
	var fields []string
	for _, f := range appErr.Fields {
		fields = append(fields, f.Field)
	}
	require.ElementsMatch(t, []string{KeyCancellationFlagCount, KeyEditTimeout}, fields)
	got, err = repo.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, changed, got, "a rejected update stores nothing")

	require.ErrorIs(t, repo.Update(ctx, changed, ""), ErrInvalidArgument)

	_, err = conn.Exec(ctx, `UPDATE settings SET value = '"soon"' WHERE key = $1`, KeyEditTimeout)
	require.NoError(t, err)
	_, err = repo.Get(ctx)
	require.ErrorContains(t, err, KeyEditTimeout)
}

func TestAdminAudit(t *testing.T) {
	repo, _ := migratedRepository(t)
	ctx := context.Background()

	first, err := repo.InsertAudit(ctx, AuditRecord{
		ActorID: "auth0|admin1", Action: "review.resolve", TargetType: "errand", TargetID: "E1",
		Reason: "courier delivered", Outcome: AuditSucceeded,
	})
	require.NoError(t, err)
	require.NotZero(t, first.ID)
	require.False(t, first.At.IsZero())
	_, err = repo.InsertAudit(ctx, AuditRecord{
		ActorID: "svc|supplier", Action: "fence.acquire", TargetType: "supplier_fence", TargetID: "F1",
		Outcome: AuditSucceeded,
	})
	require.NoError(t, err)
	last, err := repo.InsertAudit(ctx, AuditRecord{
		ActorID: "auth0|admin1", Action: "review.resolve", TargetType: "errand", TargetID: "E2",
		Reason: "not an issue", Outcome: AuditRejected,
	})
	require.NoError(t, err)

	all, err := repo.ListAudit(ctx, AuditFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, last.ID, all[0].ID, "newest first")

	byActor, err := repo.ListAudit(ctx, AuditFilter{ActorID: "auth0|admin1", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []AuditEntry{last, first}, byActor)

	rejected, err := repo.ListAudit(ctx, AuditFilter{Outcome: AuditRejected, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []AuditEntry{last}, rejected)

	page, err := repo.ListAudit(ctx, AuditFilter{BeforeID: last.ID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "fence.acquire", page[0].Action)

	future, err := repo.ListAudit(ctx, AuditFilter{Since: last.At.Add(time.Hour), Limit: 10})
	require.NoError(t, err)
	require.Empty(t, future)

	_, err = repo.InsertAudit(ctx, AuditRecord{ActorID: "a", Action: "b", TargetType: "c", TargetID: "d", Outcome: "MAYBE"})
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = repo.ListAudit(ctx, AuditFilter{})
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestListOpenReviews(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	supplierID := newUUID(t)

	reviewed := insertErrand(t, conn, errandRow{status: "REVIEW_REQUIRED", supplier: supplierID, courier: "courier-1"})
	overdue := insertErrand(t, conn, errandRow{status: "REVIEW_REQUIRED", supplier: supplierID, courier: "courier-2"})
	resolved := insertErrand(t, conn, errandRow{status: "COMPLETED", supplier: supplierID, courier: "courier-3"})
	_, err := conn.Exec(ctx, `
		INSERT INTO errand_reviews (errand_id, reason, reporter_id, note)
		VALUES ($1, 'REQUESTER_DISPUTE', 'requester-1', 'never arrived')`, reviewed)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO errand_reviews (errand_id, reason) VALUES ($1, 'DELIVERY_OVERDUE')`, overdue)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `
		INSERT INTO errand_reviews (errand_id, reason, reporter_id, resolution, resolver_id, resolution_reason, resolved_at)
		VALUES ($1, 'COURIER_ISSUE', 'courier-3', 'COMPLETED', 'auth0|admin1', 'fine', clock_timestamp())`, resolved)
	require.NoError(t, err)

	reviews, err := repo.ListOpenReviews(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, reviews, 2)
	require.Equal(t, reviewed, reviews[0].ErrandID)
	require.Equal(t, errand.ReviewRequesterDispute, reviews[0].Reason)
	require.Equal(t, "requester-1", reviews[0].ReporterID)
	require.Equal(t, "never arrived", reviews[0].Note)
	require.Equal(t, errand.StatusReviewRequired, reviews[0].Errand.Status)
	require.Equal(t, "courier-1", reviews[0].Errand.CourierID)
	require.Equal(t, supplierID, reviews[0].Errand.SupplierID)
	require.Equal(t, 10, reviews[0].Errand.Reward)
	require.Equal(t, overdue, reviews[1].ErrandID)
	require.Empty(t, reviews[1].ReporterID, "system-raised review")

	next, err := repo.ListOpenReviews(ctx, reviews[0].ID, 10)
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.Equal(t, overdue, next[0].ErrandID)
}

type errandRow struct {
	status              string
	supplier            string
	courier             string
	pendingEditSupplier string
}

// insertErrand writes an errand fixture directly; opsrepo itself never
// writes errands.
func insertErrand(t *testing.T, conn *pgx.Conn, row errandRow) string {
	t.Helper()
	if row.supplier == "" {
		row.supplier = newUUID(t)
	}
	var courier *string
	if row.courier != "" {
		courier = &row.courier
	}
	var pendingEdit []byte
	var pendingKey *string
	var pendingAt *time.Time
	if row.pendingEditSupplier != "" {
		var err error
		pendingEdit, err = json.Marshal(errand.Details{SupplierID: row.pendingEditSupplier})
		require.NoError(t, err)
		key, at := "pending-"+newUUID(t), time.Now()
		pendingKey, pendingAt = &key, &at
	}
	var id string
	err := conn.QueryRow(context.Background(), `
		INSERT INTO errands (
			status, requester_id, courier_id, supplier_id, supplier_version_id,
			delivery_building_id, delivery_building_snapshot, delivery_detail, items,
			contact_name, contact_mobile, reward, acceptance_deadline,
			create_idempotency_key, pending_edit, pending_edit_key, pending_edit_at
		) VALUES (
			$1, 'requester-1', $2, $3, gen_random_uuid(),
			gen_random_uuid(), '{}', 'Room 1', '[{"name": "coffee"}]',
			'Contact', '91234567', 10, clock_timestamp() + interval '1 hour',
			gen_random_uuid()::text, $4, $5, $6
		) RETURNING id::text`,
		row.status, courier, row.supplier, pendingEdit, pendingKey, pendingAt,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

// migratedRepository returns a repository on a new schema with every
// migration applied, and a connection to the same schema for fixtures.
func migratedRepository(t *testing.T) (*Repository, *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL ops repository integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := "order_opsrepo_" + strings.ReplaceAll(newUUID(t), "-", "")
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
	return New(conn), conn
}

func newUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	_, err := rand.Read(value)
	require.NoError(t, err)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
