package outboxadmin

import (
	"context"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type authPort bool

func (p authPort) Authenticate(context.Context, string) (auth.Principal, error) {
	perms := []auth.Permission{auth.UseErrands}
	if p {
		perms = append(perms, auth.ReviewErrands)
	}
	return auth.Principal{Subject: "admin", Kind: auth.UserToken, Permissions: perms}, nil
}

type fakeStore struct {
	rows          []msgrepo.Record
	err           error
	actor, reason string
	id            int64
	after         int64
}

func (s *fakeStore) ListFailed(_ context.Context, limit int, after int64) ([]msgrepo.Record, error) {
	s.after = after
	rows := []msgrepo.Record{}
	for _, r := range s.rows {
		if r.ID > after {
			rows = append(rows, r)
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, s.err
}
func (s *fakeStore) Requeue(_ context.Context, actor string, id int64, reason string) (msgrepo.Record, error) {
	s.actor = actor
	s.reason = reason
	s.id = id
	return msgrepo.Record{ID: id, Status: msgrepo.StatusPending}, s.err
}
func request(t *testing.T, s Store, admin bool, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(s, auth.NewMiddleware(authPort(admin)), slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func TestList(t *testing.T) {
	s := &fakeStore{rows: []msgrepo.Record{{ID: 1, Kind: "credit.reserve", Status: "FAILED", IdempotencyKey: "secret"}, {ID: 2}, {ID: 3}}}
	w := request(t, s, true, "GET", "/admin/outbox/failed?limit=2", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"nextCursor":"2"`)
	require.NotContains(t, w.Body.String(), "secret")
	require.NotContains(t, w.Body.String(), "Payload")
	require.NotContains(t, w.Body.String(), "confirmedAt")
	w = request(t, s, true, "GET", "/admin/outbox/failed?limit=2&cursor=2", "")
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "nextCursor")
	require.EqualValues(t, 2, s.after)
	for _, q := range []string{"limit=0", "limit=101", "cursor=oops", "cursor=-1"} {
		require.Equal(t, 400, request(t, s, true, "GET", "/admin/outbox/failed?"+q, "").Code)
	}
	require.Equal(t, 403, request(t, s, false, "GET", "/admin/outbox/failed", "").Code)
}
func TestRequeue(t *testing.T) {
	for _, body := range []string{`{}`, `{"reason":" "}`, `{"reason":12}`, `null`, `{"reason":"ok","extra":1}`, `{"reason":"ok"} {}`, `{"reason":"` + strings.Repeat("a", 501) + `"}`} {
		s := &fakeStore{}
		w := request(t, s, true, "POST", "/admin/outbox/3/requeue", body)
		require.Equal(t, 400, w.Code, body)
		require.Contains(t, w.Body.String(), "VALIDATION_FAILED")
		require.Empty(t, s.actor)
	}
	s := &fakeStore{}
	w := request(t, s, true, "POST", "/admin/outbox/3/requeue", `{"reason":"recover"}`)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "admin", s.actor)
	require.Equal(t, "recover", s.reason)
	require.EqualValues(t, 3, s.id)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{msgrepo.ErrNotFailed, 409, "INVALID_TRANSITION"}, {msgrepo.ErrNotFound, 404, "OUTBOX_ROW_NOT_FOUND"}} {
		s.err = tc.err
		w = request(t, s, true, "POST", "/admin/outbox/3/requeue", `{"reason":"recover"}`)
		require.Equal(t, tc.status, w.Code)
		require.Contains(t, w.Body.String(), tc.code)
	}
	require.Equal(t, 403, request(t, s, false, "POST", "/admin/outbox/3/requeue", `{"reason":"recover"}`).Code)
}
