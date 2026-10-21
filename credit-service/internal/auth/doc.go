// Package auth verifies Auth0 access tokens and enforces explicit credit
// permissions. Adapted from credit-service/internal/auth.
//
// Two kinds of caller reach the service over HTTP: a human user (login flow
// token) and another service (machine-to-machine token, gty=client-credentials).
// Permissions are read from the token's permissions claim only. Whether a
// token is a user or machine token is also checked, so a user token can never
// call a service route even if it somehow carried a service permission.
package auth
