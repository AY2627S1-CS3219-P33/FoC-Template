// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
)

// TestChatOverHTTPEndToEnd runs the phase 3 check with every real part except
// Auth0 itself: a migrated PostgreSQL, a room inserted by hand (as in manual
// testing, until errand events create rooms), and RS256 tokens signed by a
// local key server that the real verifier fetches keys from.
func TestChatOverHTTPEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := testdb.Migrated(t)
	keys := authtest.New(t)

	cfg := testConfig(db.URL)
	cfg.Auth.Issuer = keys.Server.URL + "/"
	application, err := New(ctx, cfg, discardLogger(), auth.WithHTTPClient(keys.Server.Client()))
	require.NoError(t, err)
	t.Cleanup(application.Close)
	server := httptest.NewServer(application.Handler())
	t.Cleanup(server.Close)

	token := func(subject string, permissions ...string) string {
		claims := keys.Claims(auth.ChatAudience, permissions)
		claims["sub"] = subject
		return keys.Token(t, claims, "one")
	}
	requesterToken := token("auth0|requester", "chats:use")
	courierToken := token("auth0|courier", "chats:use")
	outsiderToken := token("auth0|outsider", "chats:use")

	var roomID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO chat_rooms (errand_id, requester_id, courier_id, opened_version)
		VALUES (gen_random_uuid(), 'auth0|requester', 'auth0|courier', 1)
		RETURNING id`).Scan(&roomID))
	messages := server.URL + "/chats/" + roomID.String() + "/messages"

	call := func(method, url, bearer, body string) (int, string) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		require.NoError(t, err)
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, string(raw)
	}

	status, body := call(http.MethodPost, messages, requesterToken, `{"clientMsgId":"c1","body":"I'm at the lobby"}`)
	require.Equal(t, http.StatusCreated, status, body)
	status, body = call(http.MethodPost, messages, requesterToken, `{"clientMsgId":"c1","body":"I'm at the lobby"}`)
	require.Equal(t, http.StatusOK, status, "the retry stores nothing new: "+body)

	status, body = call(http.MethodGet, messages, courierToken, "")
	require.Equal(t, http.StatusOK, status, body)
	var page struct {
		Items []struct {
			Body       string `json:"body"`
			SenderRole string `json:"senderRole"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, "I'm at the lobby", page.Items[0].Body)
	require.Equal(t, "REQUESTER", page.Items[0].SenderRole)

	status, body = call(http.MethodGet, server.URL+"/me/chats", courierToken, "")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, roomID.String())

	status, _ = call(http.MethodGet, messages, outsiderToken, "")
	require.Equal(t, http.StatusNotFound, status, "outsiders cannot tell the room exists")
	status, _ = call(http.MethodGet, messages, "", "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, _ = call(http.MethodGet, messages, token("auth0|requester"), "")
	require.Equal(t, http.StatusForbidden, status, "a token without chats:use")

	wrongAudience := keys.Claims("https://api.foc.local/order-service", []string{"chats:use"})
	wrongAudience["sub"] = "auth0|requester"
	status, _ = call(http.MethodGet, messages, keys.Token(t, wrongAudience, "one"), "")
	require.Equal(t, http.StatusUnauthorized, status, "a token for another service is refused")
}
