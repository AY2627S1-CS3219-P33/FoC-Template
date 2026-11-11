// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package requesterrepo_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/requesterrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/places"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
)

const userID = "auth0|requester-1"

func TestPlacesCRUD(t *testing.T) {
	r, _ := newRepository(t)
	ctx := context.Background()
	building := newUUID(t)
	supplier := newUUID(t)

	home, err := r.Create(ctx, deliveryPlace(userID, "Home", building))
	require.NoError(t, err)
	assert.NotEmpty(t, home.ID)
	assert.Equal(t, userID, home.UserID)
	assert.Equal(t, places.KindDelivery, home.Kind)
	assert.Equal(t, building, home.DeliveryBuildingID)
	assert.Equal(t, "Level 2 lounge", home.DeliveryDetail)
	assert.Empty(t, home.SupplierID)
	assert.False(t, home.CreatedAt.IsZero())
	assert.True(t, home.CreatedAt.Equal(home.UpdatedAt))

	cafe, err := r.Create(ctx, pickupPlace(userID, "Cafe", strings.ToUpper(supplier)))
	require.NoError(t, err)
	assert.Equal(t, supplier, cafe.SupplierID)
	assert.Empty(t, cafe.DeliveryBuildingID)
	assert.Empty(t, cafe.DeliveryDetail)

	all, err := r.List(ctx, userID, "")
	require.NoError(t, err)
	assert.Equal(t, []places.Place{cafe, home}, all, "newest first")
	deliveries, err := r.List(ctx, userID, places.KindDelivery)
	require.NoError(t, err)
	assert.Equal(t, []places.Place{home}, deliveries)

	label, detail := "Hall", "Room 101"
	updated, err := r.Update(ctx, userID, home.ID, places.PlacePatch{Label: &label, DeliveryDetail: &detail})
	require.NoError(t, err)
	assert.Equal(t, "Hall", updated.Label)
	assert.Equal(t, "Room 101", updated.DeliveryDetail)
	assert.Equal(t, building, updated.DeliveryBuildingID)
	assert.True(t, home.CreatedAt.Equal(updated.CreatedAt))
	assert.True(t, updated.UpdatedAt.After(home.UpdatedAt), "updated_at moves")

	label = "Hostel"
	renamed, err := r.Update(ctx, userID, home.ID, places.PlacePatch{Label: &label})
	require.NoError(t, err)
	assert.Equal(t, "Room 101", renamed.DeliveryDetail, "a nil field keeps the stored value")

	unchanged, err := r.Update(ctx, userID, home.ID, places.PlacePatch{})
	require.NoError(t, err)
	assert.Equal(t, renamed, unchanged)

	require.NoError(t, r.Delete(ctx, userID, home.ID))
	requireCode(t, apperror.PlaceNotFound, r.Delete(ctx, userID, home.ID))
	_, err = r.Update(ctx, userID, home.ID, places.PlacePatch{Label: &label})
	requireCode(t, apperror.PlaceNotFound, err)
	all, err = r.List(ctx, userID, "")
	require.NoError(t, err)
	assert.Equal(t, []places.Place{cafe}, all)
}

func TestPlacesOtherUsersPlaceIsNotFound(t *testing.T) {
	r, _ := newRepository(t)
	ctx := context.Background()
	home, err := r.Create(ctx, deliveryPlace(userID, "Home", newUUID(t)))
	require.NoError(t, err)

	label := "Mine now"
	_, err = r.Update(ctx, "auth0|someone-else", home.ID, places.PlacePatch{Label: &label})
	requireCode(t, apperror.PlaceNotFound, err)
	requireCode(t, apperror.PlaceNotFound, r.Delete(ctx, "auth0|someone-else", home.ID))
	theirs, err := r.List(ctx, "auth0|someone-else", "")
	require.NoError(t, err)
	assert.Empty(t, theirs)

	mine, err := r.List(ctx, userID, "")
	require.NoError(t, err)
	assert.Equal(t, []places.Place{home}, mine, "the owner's place is untouched")

	_, err = r.Update(ctx, userID, "not-a-uuid", places.PlacePatch{Label: &label})
	requireCode(t, apperror.PlaceNotFound, err)
	requireCode(t, apperror.PlaceNotFound, r.Delete(ctx, userID, "not-a-uuid"))
}

func TestPlacesUniqueness(t *testing.T) {
	r, _ := newRepository(t)
	ctx := context.Background()
	supplier := newUUID(t)
	home, err := r.Create(ctx, deliveryPlace(userID, "Home", newUUID(t)))
	require.NoError(t, err)
	_, err = r.Create(ctx, pickupPlace(userID, "Cafe", supplier))
	require.NoError(t, err)

	t.Run("a label is unique per user and kind, ignoring case", func(t *testing.T) {
		_, err := r.Create(ctx, deliveryPlace(userID, "HOME", newUUID(t)))
		requireField(t, err, "label")
		_, err = r.Create(ctx, pickupPlace(userID, "home", newUUID(t)))
		require.NoError(t, err, "the other kind may reuse it")
		_, err = r.Create(ctx, deliveryPlace("auth0|someone-else", "Home", newUUID(t)))
		require.NoError(t, err, "another user may reuse it")
	})

	t.Run("a renamed label is checked too", func(t *testing.T) {
		work, err := r.Create(ctx, deliveryPlace(userID, "Work", newUUID(t)))
		require.NoError(t, err)
		label := "home"
		_, err = r.Update(ctx, userID, work.ID, places.PlacePatch{Label: &label})
		requireField(t, err, "label")
		label = "HOME"
		_, err = r.Update(ctx, userID, home.ID, places.PlacePatch{Label: &label})
		require.NoError(t, err, "a place may change the case of its own label")
	})

	t.Run("a supplier is saved once per user", func(t *testing.T) {
		_, err := r.Create(ctx, pickupPlace(userID, "Cafe again", strings.ToUpper(supplier)))
		requireField(t, err, "supplierId")
		_, err = r.Create(ctx, pickupPlace("auth0|someone-else", "Cafe", supplier))
		require.NoError(t, err)
	})
}

func TestPlacesLimitUnderConcurrentCreates(t *testing.T) {
	r, pool := newRepository(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, places.MaxPerKind+5)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = r.Create(ctx, deliveryPlace(userID, fmt.Sprintf("Place %d", i), newUUID(t)))
		})
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		if err == nil {
			created++
			continue
		}
		requireCode(t, apperror.PlaceLimitReached, err)
	}
	assert.Equal(t, places.MaxPerKind, created)
	var stored int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM saved_places WHERE user_id = $1 AND kind = 'DELIVERY'`, userID,
	).Scan(&stored))
	assert.Equal(t, places.MaxPerKind, stored)

	_, err := r.Create(ctx, pickupPlace(userID, "Cafe", newUUID(t)))
	require.NoError(t, err, "the limit is per kind")
	_, err = r.Create(ctx, deliveryPlace("auth0|someone-else", "Home", newUUID(t)))
	require.NoError(t, err, "the limit is per user")

	all, err := r.List(ctx, userID, places.KindDelivery)
	require.NoError(t, err)
	require.NoError(t, r.Delete(ctx, userID, all[0].ID))
	_, err = r.Create(ctx, deliveryPlace(userID, "One more", newUUID(t)))
	require.NoError(t, err, "a deleted place frees its slot")
}

func TestPlacesCheckConstraints(t *testing.T) {
	r, pool := newRepository(t)
	ctx := context.Background()

	t.Run("the table rejects rows that break a CHECK", func(t *testing.T) {
		insert := `
			INSERT INTO saved_places (user_id, kind, label, supplier_id, delivery_building_id, delivery_detail)
			VALUES ($1, $2, $3, $4, $5, $6)
		`
		supplier, building := newUUID(t), newUUID(t)
		for name, args := range map[string][]any{
			"unknown kind":            {"HOME", "x", nil, building, "Level 1"},
			"blank label":             {"DELIVERY", "  ", nil, building, "Level 1"},
			"long label":              {"DELIVERY", strings.Repeat("é", places.MaxLabelLength+1), nil, building, "Level 1"},
			"delivery with supplier":  {"DELIVERY", "x", supplier, building, "Level 1"},
			"delivery without detail": {"DELIVERY", "x", nil, building, nil},
			"pickup without supplier": {"PICKUP", "x", nil, nil, nil},
			"pickup with building":    {"PICKUP", "x", supplier, building, nil},
			"pickup with detail":      {"PICKUP", "x", supplier, nil, "Level 1"},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := pool.Exec(ctx, insert, append([]any{userID}, args...)...)
				requirePostgresCode(t, err, "23514")
			})
		}
		_, err := pool.Exec(ctx, insert, userID, "DELIVERY", strings.Repeat("é", places.MaxLabelLength), nil, building, "Level 1")
		require.NoError(t, err, "the label limit counts characters, not bytes")
	})

	t.Run("an unvalidated place is a plain error", func(t *testing.T) {
		_, err := r.Create(ctx, places.NewPlace{UserID: userID, Kind: places.KindDelivery, Label: "x", DeliveryBuildingID: newUUID(t)})
		require.Error(t, err)
		var appErr *apperror.Error
		assert.False(t, errors.As(err, &appErr), "got %v", err)
	})

	t.Run("a malformed UUID is INVALID_ARGUMENT", func(t *testing.T) {
		_, err := r.Create(ctx, pickupPlace(userID, "x", "supplier-1"))
		requireCode(t, apperror.InvalidArgument, err)
	})

	t.Run("a pickup place has no delivery detail to patch", func(t *testing.T) {
		cafe, err := r.Create(ctx, pickupPlace(userID, "Cafe", newUUID(t)))
		require.NoError(t, err)
		detail := "Level 1"
		_, err = r.Update(ctx, userID, cafe.ID, places.PlacePatch{DeliveryDetail: &detail})
		requireField(t, err, "deliveryDetail")
	})
}

func deliveryPlace(userID, label, buildingID string) places.NewPlace {
	return places.NewPlace{
		UserID:             userID,
		Kind:               places.KindDelivery,
		Label:              label,
		DeliveryBuildingID: buildingID,
		DeliveryDetail:     "Level 2 lounge",
	}
}

func pickupPlace(userID, label, supplierID string) places.NewPlace {
	return places.NewPlace{UserID: userID, Kind: places.KindPickup, Label: label, SupplierID: supplierID}
}

func requireCode(t *testing.T, want apperror.Code, err error) {
	t.Helper()
	require.Error(t, err)
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr), "want *apperror.Error, got %T: %v", err, err)
	require.Equal(t, want, appErr.Code, "error: %v", err)
}

func requireField(t *testing.T, err error, field string) {
	t.Helper()
	requireCode(t, apperror.ValidationFailed, err)
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr))
	require.Len(t, appErr.Fields, 1)
	require.Equal(t, field, appErr.Fields[0].Field)
}

func requirePostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var postgresError *pgconn.PgError
	require.True(t, errors.As(err, &postgresError), "expected PostgreSQL error, got %T: %v", err, err)
	require.Equal(t, code, postgresError.Code)
}

// newRepository returns a repository on a pool whose connections use a new
// schema with every migration applied. The pool is also used for fixtures
// and checks.
func newRepository(t *testing.T) (*requesterrepo.Repository, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL requester repository integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := "order_requesterrepo_" + strings.ReplaceAll(newUUID(t), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)

	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close(cleanupCtx)
	})

	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = pool.Exec(ctx, up)
	require.NoError(t, err)
	return requesterrepo.New(pool), pool
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
