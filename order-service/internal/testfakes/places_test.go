// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package testfakes_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/places"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	placeSupplier = "6f1c2b8e-4d3a-4c59-9e0b-2a7d5f8c1e43"
	placeBuilding = "0d9e8f7a-6b5c-4d3e-8f2a-1b0c9d8e7f6a"
)

func deliveryPlace(userID, label string) places.NewPlace {
	return places.NewPlace{
		UserID:             userID,
		Kind:               places.KindDelivery,
		Label:              label,
		DeliveryBuildingID: placeBuilding,
		DeliveryDetail:     "Level 2 lounge",
	}
}

func pickupPlace(userID, label, supplierID string) places.NewPlace {
	return places.NewPlace{UserID: userID, Kind: places.KindPickup, Label: label, SupplierID: supplierID}
}

func TestFakePlacesCRUD(t *testing.T) {
	ctx := context.Background()
	clock := testfakes.NewFakeClock(t0)
	s := testfakes.NewFakePlaces(clock)

	home, err := s.Create(ctx, deliveryPlace(requesterID, "Home"))
	require.NoError(t, err)
	assert.NotEmpty(t, home.ID)
	assert.Equal(t, places.KindDelivery, home.Kind)
	assert.Equal(t, "Level 2 lounge", home.DeliveryDetail)
	assert.Empty(t, home.SupplierID)
	assert.True(t, t0.Equal(home.CreatedAt))

	clock.Advance(time.Minute)
	cafe, err := s.Create(ctx, pickupPlace(requesterID, "Cafe", strings.ToUpper(placeSupplier)))
	require.NoError(t, err)
	assert.Equal(t, placeSupplier, cafe.SupplierID, "UUIDs are stored in lower case")
	assert.Empty(t, cafe.DeliveryBuildingID)
	assert.Empty(t, cafe.DeliveryDetail)

	all, err := s.List(ctx, requesterID, "")
	require.NoError(t, err)
	assert.Equal(t, []places.Place{cafe, home}, all, "newest first")
	pickups, err := s.List(ctx, requesterID, places.KindPickup)
	require.NoError(t, err)
	assert.Equal(t, []places.Place{cafe}, pickups)

	clock.Advance(time.Minute)
	label, detail := "Hall", "Room 101"
	updated, err := s.Update(ctx, requesterID, home.ID, places.PlacePatch{Label: &label, DeliveryDetail: &detail})
	require.NoError(t, err)
	assert.Equal(t, "Hall", updated.Label)
	assert.Equal(t, "Room 101", updated.DeliveryDetail)
	assert.True(t, home.CreatedAt.Equal(updated.CreatedAt))
	assert.True(t, clock.Now().Equal(updated.UpdatedAt))

	unchanged, err := s.Update(ctx, requesterID, home.ID, places.PlacePatch{})
	require.NoError(t, err)
	assert.Equal(t, updated, unchanged)

	require.NoError(t, s.Delete(ctx, requesterID, strings.ToUpper(home.ID)))
	requireCode(t, apperror.PlaceNotFound, s.Delete(ctx, requesterID, home.ID))
	all, err = s.List(ctx, requesterID, "")
	require.NoError(t, err)
	assert.Equal(t, []places.Place{cafe}, all)
}

func TestFakePlacesSameInstantListsNewestInsertFirst(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakePlaces(testfakes.NewFakeClock(t0))
	first, err := s.Create(ctx, deliveryPlace(requesterID, "First"))
	require.NoError(t, err)
	second, err := s.Create(ctx, deliveryPlace(requesterID, "Second"))
	require.NoError(t, err)

	all, err := s.List(ctx, requesterID, places.KindDelivery)
	require.NoError(t, err)
	assert.Equal(t, []places.Place{second, first}, all)
}

func TestFakePlacesOtherUsersPlaceIsNotFound(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakePlaces(testfakes.NewFakeClock(t0))
	home, err := s.Create(ctx, deliveryPlace(requesterID, "Home"))
	require.NoError(t, err)

	label := "Mine now"
	_, err = s.Update(ctx, "someone-else", home.ID, places.PlacePatch{Label: &label})
	requireCode(t, apperror.PlaceNotFound, err)
	requireCode(t, apperror.PlaceNotFound, s.Delete(ctx, "someone-else", home.ID))
	theirs, err := s.List(ctx, "someone-else", "")
	require.NoError(t, err)
	assert.Empty(t, theirs)

	_, err = s.Update(ctx, requesterID, "not-a-uuid", places.PlacePatch{Label: &label})
	requireCode(t, apperror.PlaceNotFound, err)
	requireCode(t, apperror.PlaceNotFound, s.Delete(ctx, requesterID, "not-a-uuid"))
}

func TestFakePlacesUniqueness(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakePlaces(testfakes.NewFakeClock(t0))
	home, err := s.Create(ctx, deliveryPlace(requesterID, "Home"))
	require.NoError(t, err)
	_, err = s.Create(ctx, pickupPlace(requesterID, "Cafe", placeSupplier))
	require.NoError(t, err)

	t.Run("a label is unique per user and kind, ignoring case", func(t *testing.T) {
		_, err := s.Create(ctx, deliveryPlace(requesterID, "HOME"))
		requireField(t, err, "label")
		_, err = s.Create(ctx, pickupPlace(requesterID, "home", "11111111-2222-4333-8444-555555555555"))
		require.NoError(t, err, "the other kind may reuse it")
		_, err = s.Create(ctx, deliveryPlace("someone-else", "Home"))
		require.NoError(t, err, "another user may reuse it")
	})

	t.Run("a renamed label is checked too", func(t *testing.T) {
		work, err := s.Create(ctx, deliveryPlace(requesterID, "Work"))
		require.NoError(t, err)
		label := "home"
		_, err = s.Update(ctx, requesterID, work.ID, places.PlacePatch{Label: &label})
		requireField(t, err, "label")
		label = "HOME"
		_, err = s.Update(ctx, requesterID, home.ID, places.PlacePatch{Label: &label})
		require.NoError(t, err, "a place may change the case of its own label")
	})

	t.Run("a supplier is saved once per user", func(t *testing.T) {
		_, err := s.Create(ctx, pickupPlace(requesterID, "Cafe again", strings.ToUpper(placeSupplier)))
		requireField(t, err, "supplierId")
		_, err = s.Create(ctx, pickupPlace("someone-else", "Cafe", placeSupplier))
		require.NoError(t, err)
	})
}

func TestFakePlacesLimitPerKind(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakePlaces(testfakes.NewFakeClock(t0))

	var wg sync.WaitGroup
	errs := make([]error, places.MaxPerKind+5)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = s.Create(ctx, deliveryPlace(requesterID, fmt.Sprintf("Place %d", i)))
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

	_, err := s.Create(ctx, pickupPlace(requesterID, "Cafe", placeSupplier))
	require.NoError(t, err, "the limit is per kind")
	_, err = s.Create(ctx, deliveryPlace("someone-else", "Home"))
	require.NoError(t, err, "the limit is per user")
}

func TestFakePlacesCheckConstraints(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakePlaces(testfakes.NewFakeClock(t0))
	requireCheck := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var appErr *apperror.Error
		assert.False(t, errors.As(err, &appErr), "a CHECK violation is a plain error, got %v", err)
	}

	for name, p := range map[string]places.NewPlace{
		"blank label":             deliveryPlace(requesterID, "   "),
		"long label":              deliveryPlace(requesterID, strings.Repeat("é", places.MaxLabelLength+1)),
		"unknown kind":            {UserID: requesterID, Kind: "HOME", Label: "x", DeliveryBuildingID: placeBuilding, DeliveryDetail: "x"},
		"delivery without detail": {UserID: requesterID, Kind: places.KindDelivery, Label: "x", DeliveryBuildingID: placeBuilding},
		"delivery with supplier": func() places.NewPlace {
			p := deliveryPlace(requesterID, "x")
			p.SupplierID = placeSupplier
			return p
		}(),
		"pickup without supplier": {UserID: requesterID, Kind: places.KindPickup, Label: "x"},
		"pickup with building": func() places.NewPlace {
			p := pickupPlace(requesterID, "x", placeSupplier)
			p.DeliveryBuildingID = placeBuilding
			return p
		}(),
		"pickup with detail": func() places.NewPlace {
			p := pickupPlace(requesterID, "x", placeSupplier)
			p.DeliveryDetail = "Level 1"
			return p
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Create(ctx, p)
			requireCheck(t, err)
		})
	}

	_, err := s.Create(ctx, deliveryPlace(requesterID, strings.Repeat("é", places.MaxLabelLength)))
	require.NoError(t, err, "the label limit counts characters, not bytes")

	t.Run("a malformed UUID is INVALID_ARGUMENT", func(t *testing.T) {
		_, err := s.Create(ctx, pickupPlace(requesterID, "x", "supplier-1"))
		requireCode(t, apperror.InvalidArgument, err)
	})

	t.Run("a pickup place has no delivery detail to patch", func(t *testing.T) {
		cafe, err := s.Create(ctx, pickupPlace(requesterID, "Cafe", placeSupplier))
		require.NoError(t, err)
		detail := "Level 1"
		_, err = s.Update(ctx, requesterID, cafe.ID, places.PlacePatch{DeliveryDetail: &detail})
		requireField(t, err, "deliveryDetail")
		blank := " "
		_, err = s.Update(ctx, requesterID, cafe.ID, places.PlacePatch{Label: &blank})
		requireCheck(t, err)
	})
}

func TestFakePlacesFaults(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakePlaces(testfakes.NewFakeClock(t0))
	boom := errors.New("boom")
	s.FailNext("Create", boom)
	_, err := s.Create(ctx, deliveryPlace(requesterID, "Home"))
	require.ErrorIs(t, err, boom)
	_, err = s.Create(ctx, deliveryPlace(requesterID, "Home"))
	require.NoError(t, err)
}

func requireField(t *testing.T, err error, field string) {
	t.Helper()
	requireCode(t, apperror.ValidationFailed, err)
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr))
	require.Len(t, appErr.Fields, 1)
	require.Equal(t, field, appErr.Fields[0].Field)
}
