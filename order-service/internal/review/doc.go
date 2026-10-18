// Package review serves Person B's admin routes: the review queue (F6.1),
// settings (F6.3.5), the errand event log (F6.2) and suspicious-activity
// flags (F6.3). Failed outbox rows are Person C's (internal/outboxadmin).
//
// Routes (all need a user token with errands:review, through
// auth.Middleware.RequireReview; F1.6.2, NFR3.3):
//
//   - GET  /admin/reviews?reason=&limit=&cursor=
//   - POST /admin/reviews/{errandId}/resolve {outcome, reason, expectedVersion}
//   - GET  /admin/settings
//   - PUT  /admin/settings
//   - GET  /admin/errand-events?userId=&errandId=&type=&from=&to=&limit=&cursor=
//   - GET  /admin/flags
//
// # Reviews
//
// Both review routes return AdminErrandView bodies (openapi/common.yaml):
// the errand with requester and courier IDs, its pickup location from the
// stored supplier version, and its review. The contact person is never
// returned (NFR3.6.3).
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
// # Settings
//
// Settings are durations in whole seconds and counts. PUT replaces every
// value; errand.SystemSettings.Validate decides what is valid, and the
// settings rows and their audit row are written in one transaction.
//
// # Event log
//
// The event log reads errand_transitions newest first by (at, id). userId
// matches transitions of errands the user requested, and those the user
// caused or was the errand's courier at. Each filter has an indexed query in
// opsrepo; the cursor is bound to every filter.
//
// # Flags
//
// Flags are evaluated now against the current settings: a courier with at
// least the withdrawal-restriction count of withdrawals, voluntary or forced,
// within its window (F6.3.1, F3.6.4), and a requester with more than the
// cancellation-flag count of cancellations within its window (F6.3.2,
// F3.5.3), each with a summary such as "High withdrawal rate, 4 in 15 min"
// (F6.3.4).
//
// # Audit
//
// Every resolution and settings change writes an admin_audit row whose actor
// is the admin's sub (F6.5). A resolution is SUCCEEDED with the admin's
// reason, written after Apply commits; the transition row already records
// actor and reason, so a failed audit insert is logged and does not undo or
// fail the resolution. A settings change is SUCCEEDED with the names of the
// changed settings as reason. A refused request that reached the service is
// REJECTED with the error code as reason, as for the supplier fence.
package review
