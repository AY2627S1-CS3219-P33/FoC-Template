// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Tests for notification-service PostgreSQL connection management.
// Author review: I validated correctness, edited for style, and added boundary checks.

package notifrepo_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/database/notifrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notification"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/testdb"
)

const errandID = "11111111-1111-1111-1111-111111111111"

func pending(recipient string, at time.Time) notification.Pending {
	return notification.Pending{
		Recipient: recipient,
		ErrandID:  errandID,
		Type:      "errand.delivered",
		Title:     "Your errand was delivered",
		Body:      "Confirm it to release the reward.",
		CreatedAt: at,
	}
}

func TestInsertIsIdempotentPerRecipient(t *testing.T) {
	store := notifrepo.New(testdb.Pool(t))
	ctx := context.Background()
	now := time.Now().UTC()

	n, err := store.Insert(ctx, "m1", []notification.Pending{pending("req", now)})
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// The same (messageId, recipient) adds nothing: at-least-once redelivery.
	n, err = store.Insert(ctx, "m1", []notification.Pending{pending("req", now)})
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// A new recipient under the same messageId is still inserted.
	n, err = store.Insert(ctx, "m1", []notification.Pending{pending("req", now), pending("cour", now)})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestInsertFansOutToEachRecipient(t *testing.T) {
	store := notifrepo.New(testdb.Pool(t))
	ctx := context.Background()
	now := time.Now().UTC()

	n, err := store.Insert(ctx, "m2", []notification.Pending{pending("req", now), pending("cour", now)})
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	for _, who := range []string{"req", "cour"} {
		items, _, err := store.List(ctx, who, notification.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, who, items[0].Recipient)
	}
}

func TestListIsNewestFirstAndCursorPaginates(t *testing.T) {
	store := notifrepo.New(testdb.Pool(t))
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	for i, key := range []string{"a", "b", "c"} {
		_, err := store.Insert(ctx, key, []notification.Pending{pending("req", base.Add(time.Duration(i)*time.Minute))})
		require.NoError(t, err)
	}

	first, cursor, err := store.List(ctx, "req", notification.ListOptions{Limit: 2})
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.NotEmpty(t, cursor, "a third row remains, so a cursor is returned")
	assert.True(t, !first[0].CreatedAt.Before(first[1].CreatedAt), "newest first")

	second, cursor, err := store.List(ctx, "req", notification.ListOptions{Limit: 2, Cursor: cursor})
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Empty(t, cursor, "no more rows")
	assert.True(t, second[0].CreatedAt.Before(first[1].CreatedAt), "the page resumes after the first one")
}

func TestUnreadCountAndMarkRead(t *testing.T) {
	store := notifrepo.New(testdb.Pool(t))
	ctx := context.Background()
	now := time.Now().UTC()
	for _, key := range []string{"a", "b", "c"} {
		_, err := store.Insert(ctx, key, []notification.Pending{pending("req", now)})
		require.NoError(t, err)
	}

	count, err := store.UnreadCount(ctx, "req")
	require.NoError(t, err)
	assert.EqualValues(t, 3, count)

	items, _, err := store.List(ctx, "req", notification.ListOptions{Limit: 10})
	require.NoError(t, err)
	id := items[0].ID

	found, err := store.MarkRead(ctx, "req", id)
	require.NoError(t, err)
	assert.True(t, found)

	count, err = store.UnreadCount(ctx, "req")
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	// Marking an already-read notification is idempotent and still "found".
	found, err = store.MarkRead(ctx, "req", id)
	require.NoError(t, err)
	assert.True(t, found)

	// Another user's id, an unknown id and a malformed id are all not-found,
	// never an error that leaks across the recipient boundary.
	found, err = store.MarkRead(ctx, "someone-else", id)
	require.NoError(t, err)
	assert.False(t, found)
	found, err = store.MarkRead(ctx, "req", "99999999-9999-9999-9999-999999999999")
	require.NoError(t, err)
	assert.False(t, found)
	found, err = store.MarkRead(ctx, "req", "not-a-uuid")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestMarkAllReadAndUnreadOnlyFilter(t *testing.T) {
	store := notifrepo.New(testdb.Pool(t))
	ctx := context.Background()
	now := time.Now().UTC()
	for _, key := range []string{"a", "b", "c"} {
		_, err := store.Insert(ctx, key, []notification.Pending{pending("req", now)})
		require.NoError(t, err)
	}

	// Read one so the unread filter has something to exclude.
	items, _, err := store.List(ctx, "req", notification.ListOptions{Limit: 10})
	require.NoError(t, err)
	_, err = store.MarkRead(ctx, "req", items[0].ID)
	require.NoError(t, err)

	unread, _, err := store.List(ctx, "req", notification.ListOptions{Limit: 10, UnreadOnly: true})
	require.NoError(t, err)
	assert.Len(t, unread, 2)

	updated, err := store.MarkAllRead(ctx, "req")
	require.NoError(t, err)
	assert.EqualValues(t, 2, updated, "only the still-unread rows are changed")

	count, err := store.UnreadCount(ctx, "req")
	require.NoError(t, err)
	assert.Zero(t, count)

	updated, err = store.MarkAllRead(ctx, "req")
	require.NoError(t, err)
	assert.Zero(t, updated, "nothing left to mark")
}
