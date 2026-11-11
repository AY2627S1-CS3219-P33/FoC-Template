// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Package documentation for notification-service Auth0 authentication and authorization.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package auth verifies Auth0 access tokens and enforces explicit notification
// permissions. Adapted from credit-service/internal/auth.
//
// Two kinds of caller reach the service over HTTP: a human user (login flow
// token) and another service (machine-to-machine token, gty=client-credentials).
// Permissions are read from the token's permissions claim only. Whether a
// token is a user or machine token is also checked, so a user token can never
// call a service route even if it somehow carried a service permission.
package auth
