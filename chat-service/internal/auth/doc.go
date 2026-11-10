// Package auth verifies Auth0 access tokens and enforces explicit chat permissions. It is
// copied from order-service's auth package; keep the two in step.
// The one addition is Principal.ExpiresAt, for closing WebSockets on expiry.
package auth
