// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package opsrepo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

func insertEventRow(t *testing.T, conn *pgx.Conn, kind, key, errandID string, data errand.EventData) int64 {
	t.Helper()
	payload, err := json.Marshal(data)
	require.NoError(t, err)
	var id int64
	require.NoError(t, conn.QueryRow(context.Background(),
		`INSERT INTO outbox (kind, idempotency_key, errand_id, payload) VALUES ($1, $2, $3, $4) RETURNING id`,
		kind, key, errandID, payload).Scan(&id))
	return id
}

func TestExportTransitionsPagesTheEventHistory(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	id := insertErrandFor(t, conn, "requester-1", "ASSIGNED")
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	placed := insertEventRow(t, conn, "event.errand.placed", id+":placed:1", id, errand.EventData{
		ErrandID: id, ErrandVersion: 1, Event: errand.EventSubmit, To: errand.StatusPendingReservation,
		RequesterID: "requester-1", Reason: "free text that must not leave", Reward: 10, At: at,
	})
	// Not transition events: a credit command, a reminder and a rejection.
	insertEventRow(t, conn, "credit.reserve", id+":reserve", id, errand.EventData{ErrandID: id})
	insertEventRow(t, conn, "event.errand.verification_reminder", id+":verify-reminder:6", id, errand.EventData{ErrandID: id})
	insertEventRow(t, conn, "event.errand.accept_rejected", id+":accept-rejected:req-1", id, errand.EventData{ErrandID: id})
	accepted := insertEventRow(t, conn, "event.errand.accepted", id+":accepted:3", id, errand.EventData{
		ErrandID: id, ErrandVersion: 3, Event: errand.EventAccept, From: errand.StatusOpen, To: errand.StatusAssigned,
		ActorType: errand.ActorCourier, ActorID: "courier-1", RequesterID: "requester-1", CourierID: "courier-1",
		Reward: 10, At: at.Add(time.Minute),
	})

	first, err := repo.ExportTransitions(ctx, 0, 1)
	require.NoError(t, err)
	require.Equal(t, []ExportedTransition{{
		ID: placed, MessageID: id + ":placed:1", Type: "errand.placed", ErrandID: id, ErrandVersion: 1,
		Event: errand.EventSubmit, To: errand.StatusPendingReservation, Reward: 10, OccurredAt: at,
	}}, first)

	second, err := repo.ExportTransitions(ctx, placed, 10)
	require.NoError(t, err)
	require.Equal(t, []ExportedTransition{{
		ID: accepted, MessageID: id + ":accepted:3", Type: "errand.accepted", ErrandID: id, ErrandVersion: 3,
		Event: errand.EventAccept, From: errand.StatusOpen, To: errand.StatusAssigned, CourierID: "courier-1",
		Reward: 10, OccurredAt: at.Add(time.Minute),
	}}, second, "credit commands, reminders and rejections are not transition events")

	none, err := repo.ExportTransitions(ctx, accepted, 10)
	require.NoError(t, err)
	require.Empty(t, none)

	_, err = repo.ExportTransitions(ctx, -1, 10)
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = repo.ExportTransitions(ctx, 0, MaxExportPageSize+2)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestExportErrandsPagesAndExcludesContactDetails(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	supplier, version, building := newUUID(t), newUUID(t), newUUID(t)
	var named, unnamed string
	require.NoError(t, conn.QueryRow(ctx, `
		INSERT INTO errands (status, requester_id, courier_id, supplier_id, supplier_version_id,
			delivery_building_id, delivery_building_snapshot, delivery_detail, items, notes,
			contact_name, contact_mobile, reward, acceptance_deadline, create_idempotency_key)
		VALUES ('ASSIGNED', 'requester-1', 'courier-1', $1, $2, $3, '{"id": "x", "name": "COM1"}',
			'Room 1', '[{"name": "coffee"}]', 'no sugar', 'Contact', '91234567', 12,
			clock_timestamp() + interval '1 hour', gen_random_uuid()::text)
		RETURNING id::text`, supplier, version, building).Scan(&named))
	unnamed = insertErrandFor(t, conn, "requester-2", "OPEN") // snapshot '{}': no building name

	page, err := repo.ExportErrands(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, page, 2)
	byID := map[string]ExportedErrand{}
	for _, e := range page {
		byID[e.ID] = e
	}
	got := byID[named]
	require.Equal(t, errand.StatusAssigned, got.Status)
	require.Equal(t, 1, got.Version)
	require.Equal(t, "courier-1", got.CourierID)
	require.Equal(t, 12, got.Reward)
	require.Equal(t, supplier, got.SupplierID)
	require.Equal(t, version, got.SupplierVersionID)
	require.Equal(t, building, got.DeliveryBuildingID)
	require.Equal(t, "COM1", got.DeliveryBuildingName)
	require.False(t, got.CreatedAt.IsZero())
	require.Equal(t, "", byID[unnamed].DeliveryBuildingName, "a snapshot without a name gives an empty name")
	require.Equal(t, "", byID[unnamed].CourierID)
	require.Less(t, page[0].ID, page[1].ID, "ordered by ID")

	next, err := repo.ExportErrands(ctx, page[0].ID, 10)
	require.NoError(t, err)
	require.Equal(t, []string{page[1].ID}, []string{next[0].ID})
	require.Len(t, next, 1)

	_, err = repo.ExportErrands(ctx, "not-a-uuid", 10)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestExportErrandsByIDLeavesOutUnknownErrands(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	a := insertErrandFor(t, conn, "requester-1", "OPEN")
	insertErrandFor(t, conn, "requester-1", "OPEN") // not asked for

	rows, err := repo.ExportErrandsByID(ctx, []string{a, newUUID(t)})

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, a, rows[0].ID)

	_, err = repo.ExportErrandsByID(ctx, nil)
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = repo.ExportErrandsByID(ctx, []string{"nope"})
	require.ErrorIs(t, err, ErrInvalidArgument)
}
