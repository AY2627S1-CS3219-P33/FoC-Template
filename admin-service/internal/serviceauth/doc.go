// Package serviceauth authenticates service-to-service calls (NFR3.3, NFR3.5.2,
// NFR3.5.3).
//
// Outgoing calls use ClientCredentials, a TokenSource that obtains an Auth0
// client-credentials token for one audience, caches it until shortly before
// it expires and shares one fetch between concurrent callers. NewTransport
// adds the token to every request as a bearer Authorization header.
//
// Incoming internal routes (supplier fence, internal API) use
// Middleware.RequireService. It verifies the bearer token with the
// internal/auth verifier and then requires a service token carrying the
// given permission. User tokens are rejected even when they are valid and
// carry that permission.
//
// The client secret comes only from Config. Neither the secret nor any token
// is logged or included in an error message.
package serviceauth
