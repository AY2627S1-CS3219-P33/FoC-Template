// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
)

func TestTranslateMapsDatabaseErrorsToChatErrors(t *testing.T) {
	leaky := "Failing row contains (secret message text)"
	tests := []struct {
		name        string
		err         error
		want        error
		retryable   bool
		mustNotHave string
	}{
		{
			name: "message check",
			err:  &pgconn.PgError{Code: "23514", TableName: "chat_messages", ConstraintName: "chat_messages_body_length", Detail: leaky},
			want: chat.ErrInvalidMessage,
		},
		{
			name: "room check",
			err:  &pgconn.PgError{Code: "23514", TableName: "chat_rooms", ConstraintName: "chat_rooms_distinct_people", Detail: leaky},
			want: chat.ErrInvalidRoom,
		},
		{
			name: "missing room",
			err:  &pgconn.PgError{Code: "23503", TableName: "chat_messages", Detail: leaky},
			want: chat.ErrRoomNotFound,
		},
		{
			name: "other rejected data",
			err:  &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type uuid: "secret"`},
		},
		{
			name:      "server error",
			err:       &pgconn.PgError{Code: "57P01", Message: "terminating connection"},
			want:      chat.ErrUnavailable,
			retryable: true,
		},
		{
			name:        "connection error",
			err:         errors.New("failed to connect to `user=chat_service database=chat_service`: chat-db:5432"),
			want:        chat.ErrUnavailable,
			retryable:   true,
			mustNotHave: "chat-db",
		},
		{
			name:      "cancelled",
			err:       fmt.Errorf("query: %w", context.Canceled),
			want:      context.Canceled,
			retryable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := translate("op", tt.err)
			require.Error(t, err)
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want)
			}
			require.Equal(t, tt.retryable, errors.Is(err, chat.ErrUnavailable))
			var pgErr *pgconn.PgError
			require.False(t, errors.As(err, &pgErr), "the raw database error is not reachable")
			require.NotContains(t, err.Error(), "secret")
			if tt.mustNotHave != "" {
				require.NotContains(t, err.Error(), tt.mustNotHave)
			}
		})
	}
	require.NoError(t, translate("op", nil))
}
