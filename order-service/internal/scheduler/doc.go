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
// When Config.Reminders and Config.Settings are set, two reminder jobs (B4b)
// run alongside, every 5 s by default. They change no errand state: they
// select candidates with a read-only query and insert one outbox row per
// errand version through opsrepo, which ignores duplicates, so C's
// dispatcher publishes them like any other event row (F3.10.3, NFR5.2):
//
//	job                    event                               key
//	verification_reminder  errand.verification_reminder        {errandId}:verify-reminder:{version}
//	pickup_reminder        errand.pickup_deadline_approaching  {errandId}:pickup-reminder:{version}
//
// The verification reminder is due once the verification deadline is
// VerificationAfter − VerificationReminderAfter away (Settings; 5 min after
// delivery by default). The pickup reminder is due once the pickup deadline
// is Config.PickupReminderLead away (10 min by default). A reminder is
// written only while the errand still has the status and version it was
// selected with, so an errand that has moved on gets none, and a re-accepted
// errand (a new version) is reminded again.
//
// The credit reconcilers are Person C's (M5), not part of this package.
package scheduler
