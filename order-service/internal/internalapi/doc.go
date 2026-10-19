// Package internalapi serves read-only routes that other services call
// (docs/contracts.md, "Internal routes"). The supplier deletion fence has its
// own package, internal/fence.
//
// Routes (each needs a service token with orders:internal, through
// serviceauth.RequireService):
//
//   - GET /internal/users/{userId}/active-errands
//
// It returns {userId, asRequester, asCourier} for user-service's
// account-deletion check: the user's errands in any non-terminal state as
// requester, each holding a credit reservation (F1.2.2), and as the
// currently assigned courier (F1.2.3). userId is the user's Auth0 subject;
// an unknown user has zero of both.
package internalapi
