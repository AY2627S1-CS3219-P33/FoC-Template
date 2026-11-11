// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package auth verifies Auth0 access tokens and enforces explicit chat permissions. It is
// copied from order-service's auth package; keep the two in step.
// The one addition is Principal.ExpiresAt, for closing WebSockets on expiry.
package auth
