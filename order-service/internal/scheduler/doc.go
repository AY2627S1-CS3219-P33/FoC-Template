// Package scheduler runs order-service's deadline jobs (B4a): the periodic
// batches that move errands whose deadline has passed (F3.7, F3.8.4, F3.8.5,
// F3.10.3).
//
// Each job polls Postgres through errand.Store.ClaimDue, which selects due
// errands with FOR UPDATE SKIP LOCKED, so several replicas, or a job and a
// user request, never act on the same errand. Deadlines are polled rather
// than scheduled as broker delayed messages because requesters can edit them
// (F3.1.3). Every transition is built by errand.Next with the SYSTEM actor
// and applied by the Store, so the state machine and its outbox rows (the
// credit release on expiry, the transfer on auto-complete) stay in one place:
//
//	job                 DueKind            transition
//	acceptance_expiry   AcceptanceExpiry   OPEN → EXPIRED, release (F3.7, F4.5)
//	pickup_deadline     PickupDeadline     ASSIGNED → OPEN, or → EXPIRED with
//	                                       release once the acceptance deadline
//	                                       has passed; a forced withdrawal that
//	                                       counts towards F3.6.4 (F3.8.4)
//	delivery_deadline   DeliveryDeadline   PICKED_UP → REVIEW_REQUIRED with
//	                                       reason DELIVERY_OVERDUE; ClaimDue
//	                                       writes the errand_reviews row (F3.8.5)
//	verification_lapse  VerificationLapse  DELIVERED → COMPLETED, transfer
//	                                       (F3.10.3, F4.2.2)
//
// decide re-checks the deadline against the batch's now and leaves the
// errand alone when it no longer applies, for example because the deadline
// was extended after the errand was selected.
//
// Each job has its own goroutine and timer: acceptance expiry and
// verification lapse every 5 s, pickup and delivery every 15 s by default,
// each with ±20% jitter so replicas drift apart. A job runs once at start,
// then on every tick, and runs again at once while its batches come back
// full. A panic in a batch is recovered, logged and counted, and never stops
// the other jobs or the next tick.
//
// Reminder jobs (B4b) and the credit reconcilers (M5) are not part of this
// package's deadline jobs.
package scheduler
