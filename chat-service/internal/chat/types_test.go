// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package chat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRoomStatusFollowsClosesAt(t *testing.T) {
	ended := time.Date(2026, 11, 9, 14, 40, 0, 0, time.UTC)
	closes := ended.Add(24 * time.Hour)

	open := Room{}
	require.Equal(t, StatusOpen, open.Status(ended.Add(1000*time.Hour)), "a room without an ending never closes")

	room := Room{Ending: &Ending{Reason: EndCompleted, EndedAt: ended, ClosesAt: closes}}
	require.Equal(t, StatusClosing, room.Status(ended))
	require.Equal(t, StatusClosing, room.Status(closes.Add(-time.Nanosecond)))
	require.Equal(t, StatusClosed, room.Status(closes), "closed exactly at closesAt")
	require.Equal(t, StatusClosed, room.Status(closes.Add(time.Second)))
}

func TestRoleOf(t *testing.T) {
	room := Room{RequesterID: "auth0|requester", CourierID: "auth0|courier"}

	role, ok := room.RoleOf("auth0|requester")
	require.True(t, ok)
	require.Equal(t, RoleRequester, role)

	role, ok = room.RoleOf("auth0|courier")
	require.True(t, ok)
	require.Equal(t, RoleCourier, role)

	_, ok = room.RoleOf("auth0|someone-else")
	require.False(t, ok)
	_, ok = room.RoleOf("")
	require.False(t, ok)
}
