// Package review serves the admin review queue (F6.1): every errand in
// REVIEW_REQUIRED, whatever the reason, and its resolution to COMPLETED or
// REFUNDED (F6.1.2, F6.1.3).
//
// Routes (all need a user token with errands:review, through
// auth.Middleware.RequireReview; F1.6.2, NFR3.3):
//
//   - GET  /admin/reviews?reason=&limit=&cursor=
//   - POST /admin/reviews/{errandId}/resolve {outcome, reason, expectedVersion}
//
// Both return AdminErrandView bodies (openapi/common.yaml): the errand with
// requester and courier IDs, its pickup location from the stored supplier
// version, and its review. The contact person is never returned (NFR3.6.3).
//
// The list reads open errand_reviews rows through opsrepo, oldest first, and
// each errand through errand.Store. The cursor is bound to the reason filter.
//
// A resolution is one transition through errand.Next and Store.Apply with
// the ADMIN actor: Apply resolves the review row (resolver, outcome, reason)
// and inserts the credit.transfer or credit.release row in the same
// transaction. The reason is required (F6.1.4). A stale expectedVersion is
// ERRAND_CHANGED; an errand that has already left REVIEW_REQUIRED is
// ERRAND_UNAVAILABLE and nothing changes, so resolving twice is safe.
//
// Every resolution request that reaches the errand writes an admin_audit row
// whose actor is the admin's sub and whose action names the outcome
// (errand_review.complete or errand_review.refund) (F6.5). A resolution is
// SUCCEEDED with the admin's reason, written after Apply commits; the
// transition row already records actor and reason, so a failed audit insert
// is logged and does not undo or fail the resolution. A refused one is
// REJECTED with the error code as reason, as for the supplier fence.
package review
