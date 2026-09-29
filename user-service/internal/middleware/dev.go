package middleware

import "net/http"

// NewAuth0WithClient builds an Auth0 validator that fetches JWKS through the
// supplied HTTP client instead of the network default.
//
// DEV-ONLY: this exists so a local mock issuer (see internal/devauth) can serve
// signing keys in-process. Production code must use NewAuth0. Never wire this to
// anything reachable in a deployed environment.
func NewAuth0WithClient(domain, audience string, httpClient *http.Client) (*Auth0, error) {
	return newAuth0(domain, audience, httpClient)
}
