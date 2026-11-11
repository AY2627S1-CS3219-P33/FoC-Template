// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package migrations_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/migrations"
)

var relations = []string{
	"chat_rooms",
	"chat_messages",
	"errand_endings",
	"chat_rooms_by_requester",
	"chat_rooms_by_courier",
	"chat_messages_room_id_idx",
}

func TestLatestVersionReadsEmbeddedFiles(t *testing.T) {
	version, err := migrations.LatestVersion()
	require.NoError(t, err)
	require.GreaterOrEqual(t, version, int64(1))
}

// goose up, down and up again on an empty schema: the Down section really
// undoes the Up section, so a bad deploy can be rolled back.
func TestMigrationsGoUpDownAndUpAgain(t *testing.T) {
	ctx := context.Background()
	db := testdb.Empty(t)
	goose := testdb.Goose(t, db)
	latest, err := migrations.LatestVersion()
	require.NoError(t, err)

	_, err = goose.Up(ctx)
	require.NoError(t, err)
	requireRelations(t, db.Pool, true)
	version, err := goose.GetDBVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, latest, version)

	_, err = goose.DownTo(ctx, 0)
	require.NoError(t, err)
	requireRelations(t, db.Pool, false)

	_, err = goose.Up(ctx)
	require.NoError(t, err)
	requireRelations(t, db.Pool, true)
}

func TestSchemaConstraints(t *testing.T) {
	ctx := context.Background()

	t.Run("one room per errand and courier", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		errand := uuid.New()
		require.NoError(t, insertRoom(pool, errand, "auth0|req", "auth0|courier-a"))
		requireCode(t, insertRoom(pool, errand, "auth0|req", "auth0|courier-a"), "23505")
		require.NoError(t, insertRoom(pool, errand, "auth0|req", "auth0|courier-b"), "a second courier gets a second room")
	})

	t.Run("requester and courier are different people", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		requireCode(t, insertRoom(pool, uuid.New(), "auth0|same", "auth0|same"), "23514")
	})

	t.Run("end fields are all set or all empty", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		id := roomID(t, pool)
		for _, set := range []string{
			"ended_at = now()",
			"end_reason = 'COMPLETED'",
			"closes_at = now()",
			"ended_at = now(), end_reason = 'COMPLETED'",
			"ended_at = now(), closes_at = now()",
		} {
			_, err := pool.Exec(ctx, "UPDATE chat_rooms SET "+set+" WHERE id = $1", id)
			requireCode(t, err, "23514")
		}
		_, err := pool.Exec(ctx, `UPDATE chat_rooms
			SET ended_at = now(), end_reason = 'COMPLETED', closes_at = now() + interval '24 hours'
			WHERE id = $1`, id)
		require.NoError(t, err)
	})

	t.Run("a room cannot close before it ends", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		_, err := pool.Exec(ctx, `UPDATE chat_rooms
			SET ended_at = now(), end_reason = 'WITHDRAWN', closes_at = now() - interval '1 second'
			WHERE id = $1`, roomID(t, pool))
		requireCode(t, err, "23514")
	})

	t.Run("end reason is one of the known ones", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		_, err := pool.Exec(ctx, `UPDATE chat_rooms
			SET ended_at = now(), end_reason = 'CANCELLED', closes_at = now()
			WHERE id = $1`, roomID(t, pool))
		requireCode(t, err, "23514")
	})

	t.Run("message body is 1 to 2000 characters", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		room := roomID(t, pool)
		requireCode(t, insertMessage(pool, room, "c-empty", ""), "23514")
		requireCode(t, insertMessage(pool, room, "c-long", strings.Repeat("a", 2001)), "23514")
		require.NoError(t, insertMessage(pool, room, "c-max", strings.Repeat("é", 2000)), "characters, not bytes")
	})

	t.Run("client message ID is 1 to 64 characters", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		room := roomID(t, pool)
		requireCode(t, insertMessage(pool, room, "", "hi"), "23514")
		requireCode(t, insertMessage(pool, room, strings.Repeat("x", 65), "hi"), "23514")
		require.NoError(t, insertMessage(pool, room, strings.Repeat("x", 64), "hi"))
	})

	t.Run("a client message ID is used once per room", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		roomA, roomB := roomID(t, pool), roomID(t, pool)
		require.NoError(t, insertMessage(pool, roomA, "same", "hi"))
		requireCode(t, insertMessage(pool, roomA, "same", "hi again"), "23505")
		require.NoError(t, insertMessage(pool, roomB, "same", "hi"), "another room may reuse it")
	})

	t.Run("messages need an existing room", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		requireCode(t, insertMessage(pool, uuid.New(), "c1", "hi"), "23503")
	})

	t.Run("rooms with messages cannot be deleted", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		room := roomID(t, pool)
		require.NoError(t, insertMessage(pool, room, "c1", "hi"))
		_, err := pool.Exec(ctx, `DELETE FROM chat_rooms WHERE id = $1`, room)
		requireCode(t, err, "23503")
	})

	t.Run("message IDs increase", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		room := roomID(t, pool)
		var first, second int64
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO chat_messages (room_id, sender_id, body, client_msg_id)
			VALUES ($1, 'auth0|req', 'a', 'c1') RETURNING id`, room).Scan(&first))
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO chat_messages (room_id, sender_id, body, client_msg_id)
			VALUES ($1, 'auth0|req', 'b', 'c2') RETURNING id`, room).Scan(&second))
		require.Greater(t, second, first)
	})

	t.Run("errand ending reasons are checked", func(t *testing.T) {
		pool := testdb.Migrated(t).Pool
		_, err := pool.Exec(ctx, `INSERT INTO errand_endings (errand_id, reason, ended_at)
			VALUES ($1, 'WITHDRAWN', now())`, uuid.New())
		requireCode(t, err, "23514", "a withdrawal ends one room, not the errand")
	})
}

func requireRelations(t *testing.T, pool *pgxpool.Pool, exist bool) {
	t.Helper()
	for _, relation := range relations {
		var found *string
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, relation).Scan(&found))
		if exist {
			require.NotNilf(t, found, "%s was not created", relation)
		} else {
			require.Nilf(t, found, "%s remains after rollback", relation)
		}
	}
}

func insertRoom(pool *pgxpool.Pool, errandID uuid.UUID, requester, courier string) error {
	_, err := pool.Exec(context.Background(), `
		INSERT INTO chat_rooms (errand_id, requester_id, courier_id, opened_version)
		VALUES ($1, $2, $3, 2)`, errandID, requester, courier)
	return err
}

func roomID(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO chat_rooms (errand_id, requester_id, courier_id, opened_version)
		VALUES ($1, 'auth0|req', 'auth0|courier', 2) RETURNING id`, uuid.New()).Scan(&id))
	return id
}

func insertMessage(pool *pgxpool.Pool, roomID uuid.UUID, clientMsgID, body string) error {
	_, err := pool.Exec(context.Background(), `
		INSERT INTO chat_messages (room_id, sender_id, body, client_msg_id)
		VALUES ($1, 'auth0|req', $2, $3)`, roomID, body, clientMsgID)
	return err
}

func requireCode(t *testing.T, err error, code string, msgAndArgs ...any) {
	t.Helper()
	require.Error(t, err, msgAndArgs...)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a PostgreSQL error, got %T: %v", err, err)
	require.Equal(t, code, pgErr.Code, msgAndArgs...)
}
