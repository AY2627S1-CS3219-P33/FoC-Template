// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Tests for notification-service notification event messaging.
// Author review: I validated correctness, edited for style, and added boundary checks.

package notifmsg

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func envelope(t *testing.T, routingKey string, data any) Envelope {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return Envelope{
		MessageID:     "errand-1:" + routingKey,
		Type:          routingKey,
		OccurredAt:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		ErrandID:      "11111111-1111-1111-1111-111111111111",
		ErrandVersion: 1,
		Data:          raw,
	}
}

func TestClassifyPlacedProducesNothing(t *testing.T) {
	items, err := classify(envelope(t, EventPlaced, map[string]any{
		"errandId": "e", "errandVersion": 1, "event": "submit",
		"to": "PENDING_RESERVATION", "actorType": "REQUESTER", "requesterId": "req", "reward": 10, "at": "2026-01-02T03:04:05Z",
	}))
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestClassifyOpenedNotifiesRequester(t *testing.T) {
	items, err := classify(envelope(t, EventOpened, map[string]any{
		"errandId": "e", "errandVersion": 1, "event": "reservation_confirmed",
		"to": "OPEN", "actorType": "SYSTEM", "requesterId": "req", "reward": 10, "at": "2026-01-02T03:04:05Z",
	}))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "req", items[0].Recipient)
	assert.Equal(t, EventOpened, items[0].Type)
	assert.NotEmpty(t, items[0].Title)
}

func TestClassifyAcceptedExcludesActingCourier(t *testing.T) {
	items, err := classify(envelope(t, EventAccepted, map[string]any{
		"errandId": "e", "errandVersion": 1, "event": "accept",
		"to": "ASSIGNED", "actorType": "COURIER", "actorId": "cour",
		"requesterId": "req", "courierId": "cour", "reward": 10, "at": "2026-01-02T03:04:05Z",
	}))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "req", items[0].Recipient, "the courier who accepted should not be notified of their own action")
}

func TestClassifyCancelledNotifiesCourierNotActingRequester(t *testing.T) {
	items, err := classify(envelope(t, EventCancelled, map[string]any{
		"errandId": "e", "errandVersion": 1, "event": "cancel",
		"to": "CANCELLED", "actorType": "REQUESTER", "actorId": "req",
		"requesterId": "req", "courierId": "cour", "reward": 10, "at": "2026-01-02T03:04:05Z",
	}))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "cour", items[0].Recipient)
}

func TestClassifyCompletedNotifiesBothParties(t *testing.T) {
	items, err := classify(envelope(t, EventCompleted, map[string]any{
		"errandId": "e", "errandVersion": 1, "event": "admin_complete",
		"to": "COMPLETED", "actorType": "ADMIN", "actorId": "admin",
		"requesterId": "req", "courierId": "cour", "reward": 10, "at": "2026-01-02T03:04:05Z",
	}))
	require.NoError(t, err)
	require.Len(t, items, 2)
	got := []string{items[0].Recipient, items[1].Recipient}
	assert.ElementsMatch(t, []string{"req", "cour"}, got)
}

func TestClassifyRejectionNotifiesActor(t *testing.T) {
	items, err := classify(envelope(t, EventAcceptRejected, map[string]any{
		"errandId": "e", "errandVersion": 1, "status": "OPEN", "action": "accept",
		"code": "ERRAND_CHANGED", "actorType": "COURIER", "actorId": "cour", "at": "2026-01-02T03:04:05Z",
	}))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "cour", items[0].Recipient)
	assert.Contains(t, items[0].Title, "accept")
}

func TestClassifyRemindersTargetTheRightParty(t *testing.T) {
	verify, err := classify(envelope(t, EventVerificationReminder, map[string]any{
		"errandId": "e", "errandVersion": 1, "status": "DELIVERED",
		"requesterId": "req", "courierId": "cour", "deadline": "2026-01-02T04:04:05Z", "at": "2026-01-02T03:04:05Z",
	}))
	require.NoError(t, err)
	require.Len(t, verify, 1)
	assert.Equal(t, "req", verify[0].Recipient)

	pickup, err := classify(envelope(t, EventPickupDeadlineApproaching, map[string]any{
		"errandId": "e", "errandVersion": 1, "status": "ASSIGNED",
		"requesterId": "req", "courierId": "cour", "deadline": "2026-01-02T04:04:05Z", "at": "2026-01-02T03:04:05Z",
	}))
	require.NoError(t, err)
	require.Len(t, pickup, 1)
	assert.Equal(t, "cour", pickup[0].Recipient)
}

func TestClassifyUnknownKeyIsUndecodable(t *testing.T) {
	_, err := classify(envelope(t, "errand.teleported", map[string]any{
		"errandId": "e", "errandVersion": 1, "requesterId": "req",
	}))
	assert.ErrorIs(t, err, ErrUndecodable)
	assert.Equal(t, Reject, DispositionOf(err))
}
