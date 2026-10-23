package orderclient

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/serviceauth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
	deletefeature "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/delete"
)

const (
	testOperationID = supplier.DeletionOperationID("a1b2c3d4-e5f6-4a1b-8c2d-3e4f5a6b7c8d")
	testSupplierID  = supplier.SupplierID("29e9aa8b-5651-4981-8828-3fbb1b21fcb1")
	otherSupplierID = "44444444-4444-4444-8444-444444444444"
	testToken       = "m2m-token-value"
)

type staticToken struct{}

func (staticToken) Token(context.Context) (string, error) { return testToken, nil }

// stubOrderService records requests and answers each with handle.
type stubOrderService struct {
	server *httptest.Server
	hits   atomic.Int64
}

func newStub(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, hit int64)) *stubOrderService {
	t.Helper()
	stub := &stubOrderService{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, stub.hits.Add(1))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func newTestClient(t *testing.T, baseURL string, maxAttempts int) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: baseURL, Timeout: 500 * time.Millisecond, MaxAttempts: maxAttempts},
		serviceauth.NewTransport(nil, staticToken{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return client
}

func writeFence(w http.ResponseWriter, supplierID, state string, active bool) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"operationId": string(testOperationID), "supplierId": supplierID,
		"state": state, "hasActiveErrands": active,
	})
}

func writeProblem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": "x"})
}

func TestAcquireSendsAuthenticatedRequestAndParsesFence(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/internal/supplier-fences/"+string(testOperationID)+"/acquire", r.URL.Path)
		require.Equal(t, "Bearer "+testToken, r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, map[string]string{"supplierId": string(testSupplierID)}, body)
		writeFence(w, string(testSupplierID), "open", true)
	})

	fence, err := newTestClient(t, stub.server.URL, 2).Acquire(context.Background(), testOperationID, testSupplierID)
	require.NoError(t, err)
	require.Equal(t, deletefeature.Fence{
		OperationID: testOperationID, SupplierID: testSupplierID,
		State: deletefeature.FenceOpen, HasActiveErrands: true,
	}, fence)
}

func TestInspectCommitReleaseUseContractRoutes(t *testing.T) {
	var seen []string
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/internal/supplier-fences/" + string(testOperationID) + "/commit":
			writeFence(w, string(testSupplierID), "committed", false)
		case "/internal/supplier-fences/" + string(testOperationID) + "/release":
			writeFence(w, string(testSupplierID), "released", false)
		default:
			writeFence(w, string(testSupplierID), "open", false)
		}
	})
	client := newTestClient(t, stub.server.URL, 1)

	_, err := client.Inspect(context.Background(), testOperationID)
	require.NoError(t, err)
	require.NoError(t, client.Commit(context.Background(), testOperationID))
	require.NoError(t, client.Release(context.Background(), testOperationID))
	prefix := "/internal/supplier-fences/" + string(testOperationID)
	require.Equal(t, []string{"GET " + prefix, "POST " + prefix + "/commit", "POST " + prefix + "/release"}, seen)
}

func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, hit int64) {
		if hit == 1 {
			writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
			return
		}
		writeFence(w, string(testSupplierID), "committed", false)
	})
	require.NoError(t, newTestClient(t, stub.server.URL, 2).Commit(context.Background(), testOperationID))
	require.EqualValues(t, 2, stub.hits.Load())
}

func TestPersistentServerErrorIsUnavailableAfterMaxAttempts(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		writeProblem(w, http.StatusInternalServerError, "INTERNAL")
	})
	_, err := newTestClient(t, stub.server.URL, 3).Acquire(context.Background(), testOperationID, testSupplierID)
	require.ErrorIs(t, err, ErrUnavailable)
	require.Contains(t, err.Error(), "INTERNAL")
	require.NotContains(t, err.Error(), testToken)
	require.EqualValues(t, 3, stub.hits.Load())
}

func TestDefiniteClientErrorsAreNotRetried(t *testing.T) {
	for _, tt := range []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusNotFound, "FENCE_NOT_FOUND", ErrFenceNotFound},
		{http.StatusConflict, "INVALID_TRANSITION", ErrInvalidTransition},
		{http.StatusBadRequest, "INVALID_ARGUMENT", ErrRejected},
		{http.StatusForbidden, "FORBIDDEN", ErrRejected},
	} {
		t.Run(tt.code, func(t *testing.T) {
			stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
				writeProblem(w, tt.status, tt.code)
			})
			err := newTestClient(t, stub.server.URL, 3).Commit(context.Background(), testOperationID)
			require.ErrorIs(t, err, tt.want)
			require.EqualValues(t, 1, stub.hits.Load())
		})
	}
}

func TestTimeoutIsUnavailable(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	_, err := newTestClient(t, stub.server.URL, 1).Inspect(context.Background(), testOperationID)
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestRejectsFencesThatDoNotMatchTheRequest(t *testing.T) {
	t.Run("acquire for another supplier", func(t *testing.T) {
		stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
			writeFence(w, otherSupplierID, "open", false)
		})
		_, err := newTestClient(t, stub.server.URL, 1).Acquire(context.Background(), testOperationID, testSupplierID)
		require.ErrorIs(t, err, ErrInvalidResponse)
	})
	t.Run("another operation", func(t *testing.T) {
		stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "11111111-1111-4111-8111-111111111111", "supplierId": string(testSupplierID), "state": "open",
			})
		})
		_, err := newTestClient(t, stub.server.URL, 1).Inspect(context.Background(), testOperationID)
		require.ErrorIs(t, err, ErrInvalidResponse)
	})
	t.Run("commit not confirmed", func(t *testing.T) {
		stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
			writeFence(w, string(testSupplierID), "open", false)
		})
		require.ErrorIs(t, newTestClient(t, stub.server.URL, 1).Commit(context.Background(), testOperationID), ErrInvalidResponse)
	})
	t.Run("unknown state", func(t *testing.T) {
		stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
			writeFence(w, string(testSupplierID), "OPEN", false)
		})
		_, err := newTestClient(t, stub.server.URL, 1).Inspect(context.Background(), testOperationID)
		require.ErrorIs(t, err, ErrInvalidResponse)
	})
	t.Run("malformed body", func(t *testing.T) {
		stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
			_, _ = w.Write([]byte("not json"))
		})
		_, err := newTestClient(t, stub.server.URL, 1).Inspect(context.Background(), testOperationID)
		require.ErrorIs(t, err, ErrInvalidResponse)
	})
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Store(true)
		writeFence(w, string(testSupplierID), "open", false)
	}))
	t.Cleanup(target.Close)
	stub := newStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	_, err := newTestClient(t, stub.server.URL, 1).Inspect(context.Background(), testOperationID)
	require.Error(t, err)
	require.False(t, redirected.Load(), "the bearer token must only reach the configured order-service")
}

func TestNewValidatesConfiguration(t *testing.T) {
	transport := serviceauth.NewTransport(nil, staticToken{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	valid := Config{BaseURL: "http://order-service:3003", Timeout: time.Second, MaxAttempts: 1}
	_, err := New(valid, transport, logger)
	require.NoError(t, err)

	for name, cfg := range map[string]Config{
		"trailing slash": {BaseURL: "http://order-service:3003/", Timeout: time.Second, MaxAttempts: 1},
		"scheme":         {BaseURL: "ftp://order-service", Timeout: time.Second, MaxAttempts: 1},
		"credentials":    {BaseURL: "http://user:pass@order-service", Timeout: time.Second, MaxAttempts: 1},
		"timeout":        {BaseURL: valid.BaseURL, MaxAttempts: 1},
		"attempts":       {BaseURL: valid.BaseURL, Timeout: time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(cfg, transport, logger)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "pass")
		})
	}
	_, err = New(valid, nil, logger)
	require.Error(t, err)
	_, err = New(valid, transport, nil)
	require.Error(t, err)
}

func TestInvalidOperationIDIsRejectedWithoutCalling(t *testing.T) {
	stub := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		writeFence(w, string(testSupplierID), "open", false)
	})
	_, err := newTestClient(t, stub.server.URL, 1).Inspect(context.Background(), "../../admin")
	require.ErrorIs(t, err, ErrRejected)
	require.EqualValues(t, 0, stub.hits.Load())
}
