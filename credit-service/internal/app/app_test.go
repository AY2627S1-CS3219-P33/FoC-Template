package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

type checkerStub struct{ err error }

func (c checkerStub) Check(context.Context) error { return c.err }

type rejectingAuth struct{}

func (rejectingAuth) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, &auth.AuthenticationError{Kind: auth.InvalidCredential}
}

type noWallets struct{}

func (noWallets) CreateWallet(context.Context, string, string) (wallet.Wallet, bool, error) {
	panic("must not be reached without authentication")
}
func (noWallets) GetByOwner(context.Context, string) (wallet.Wallet, error) {
	panic("must not be reached without authentication")
}
func (noWallets) GetBySubject(context.Context, string) (wallet.Wallet, error) {
	panic("must not be reached without authentication")
}

func handlerWith(checker error) http.Handler {
	return Handler(Deps{Readiness: checkerStub{err: checker}, Auth: rejectingAuth{}, Wallets: noWallets{}})
}

func TestHandlerServesReadyzWithoutOpeningAPort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	response := httptest.NewRecorder()
	handlerWith(nil).ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)

	response = httptest.NewRecorder()
	handlerWith(errors.New("down")).ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestWalletRoutesRequireAuthentication(t *testing.T) {
	const owner = "00000000-0000-0000-0000-00000000a11c"
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/wallet/me"},
		{http.MethodGet, "/internal/wallets/" + owner},
		{http.MethodPut, "/internal/wallets/" + owner},
	} {
		for _, header := range []string{"", "Bearer not-a-valid-token"} {
			request := httptest.NewRequest(route.method, route.path, nil)
			if header != "" {
				request.Header.Set("Authorization", header)
			}
			response := httptest.NewRecorder()
			handlerWith(nil).ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code, route.method+" "+route.path)
		}
	}
}
