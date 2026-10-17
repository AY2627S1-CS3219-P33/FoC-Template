package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testauth"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testdb"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

// Runs the real token verifier (against a local JWKS server), the real routes
// and a real database, with no network port opened for the service itself.
func TestWalletFlowEndToEnd(t *testing.T) {
	const owner = "00000000-0000-0000-0000-00000000a11c"
	pool := testdb.Pool(t)
	f := testauth.New(t)

	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	verifier, err := auth.NewAuth0(cfg, logger, auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	handler := Handler(Deps{
		Readiness: readiness.NewPostgresChecker(pool, 3),
		Auth:      verifier,
		Wallets:   wallet.NewService(pool, 100),
		Logger:    logger,
	})

	token := func(subject string, machine bool, permissions ...string) string {
		claims := f.Claims(auth.CreditAudience, permissions)
		claims["sub"] = subject
		if machine {
			claims["gty"] = "client-credentials"
		}
		return f.Token(t, claims, "one")
	}
	userService := token("userservice@clients", true, "credits:wallets:create", "credits:read")
	alice := token("auth0|alice", false, "credits:read:own")
	bob := token("auth0|bob", false, "credits:read:own")

	do := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	balance := func(response *httptest.ResponseRecorder) string {
		var body map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		return body["available"].(string)
	}

	// Before the wallet exists, a user gets 404.
	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/v1/wallet/me", alice, "").Code)

	// user-service creates the wallet; the configured allocation is applied.
	created := do(http.MethodPut, "/internal/wallets/"+owner, userService, `{"auth0Subject":"auth0|alice"}`)
	require.Equal(t, http.StatusCreated, created.Code)
	require.Equal(t, "100", balance(created))

	// Repeating it is harmless and does not allocate again.
	again := do(http.MethodPut, "/internal/wallets/"+owner, userService, `{"auth0Subject":"auth0|alice"}`)
	require.Equal(t, http.StatusOK, again.Code)
	require.Equal(t, "100", balance(again))

	// Alice sees her own wallet; Bob, who has none, sees nothing of hers.
	mine := do(http.MethodGet, "/v1/wallet/me", alice, "")
	require.Equal(t, http.StatusOK, mine.Code)
	require.Equal(t, "100", balance(mine))
	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/v1/wallet/me", bob, "").Code)

	// user-service can read it by owner ID.
	require.Equal(t, http.StatusOK, do(http.MethodGet, "/internal/wallets/"+owner, userService, "").Code)

	// Users can neither create wallets nor read by owner ID.
	require.Equal(t, http.StatusForbidden,
		do(http.MethodPut, "/internal/wallets/"+owner, alice, `{"auth0Subject":"auth0|alice"}`).Code)
	require.Equal(t, http.StatusForbidden, do(http.MethodGet, "/internal/wallets/"+owner, bob, "").Code)

	// The subject cannot be reassigned.
	require.Equal(t, http.StatusConflict,
		do(http.MethodPut, "/internal/wallets/"+owner, userService, `{"auth0Subject":"auth0|bob"}`).Code)
}
