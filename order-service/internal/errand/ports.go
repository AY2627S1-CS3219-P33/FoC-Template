package errand

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
)

// This file declares the ports: the interfaces errand logic uses to reach
// storage, other services, the broker and the cache. Adapters implement them
// (A: Store; B: SupplierGate, UserDirectory, OpenErrandCache, Settings; C:
// Publisher) and internal/testfakes has an in-memory fake of each.
//
// Errors: unless a method says otherwise, a failure the caller should show
// to a client is an *apperror.Error with one of the codes named in the
// method's comment; anything else (a lost connection, a bug) is a plain error
// that callers treat as INTERNAL or retry.

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store persists errands. It is the only writer of errands and
// errand_transitions, it inserts the outbox rows that transitions and
// rejections emit, and it opens and resolves the errand_reviews row of a
// transition into or out of REVIEW_REQUIRED. Every write method runs in a
// single database transaction: the errand row, its transition record, its
// outbox rows and its review row commit together or not at all (NFR7.4).
// Store writes no other table; audit rows and reminder rows are written by
// their owners.
//
// Every errand_transitions row records, besides the actor, the errand's
// courier: CourierID before the transition, or after it when there was none
// before (an accept). A forced withdrawal's actor is SYSTEM, so this column
// is what attributes it to the courier (F3.6.3, F3.6.4).
type Store interface {
	// Create inserts a new errand in PENDING_RESERVATION. e is the errand
	// passed to Next (Status StatusNone, Version 0) and t is Next's Submit
	// transition for it; the stored row is t.ApplyTo(e).
	//
	// In one transaction Create takes the shared advisory lock on the
	// supplier key (docs/contracts.md), refuses if supplier_fences has an
	// OPEN or COMMITTED fence for e.Details.SupplierID, inserts the errand,
	// records the transition and inserts t.Outbox (the credit.reserve and
	// errand.placed rows).
	//
	// Create is idempotent per (RequesterID, CreateIdempotencyKey): if that
	// pair already exists it changes nothing and returns the stored errand
	// with created = false, whatever the new request contained (NFR7.3).
	//
	// Errors: SUPPLIER_DELETION_IN_PROGRESS when the supplier is fenced;
	// INVALID_ARGUMENT when t is not a Submit transition for e.
	Create(ctx context.Context, e Errand, t Transition) (stored Errand, created bool, err error)

	// Apply persists a transition from Next for an existing errand and
	// returns the errand as stored afterwards.
	//
	// The update is a compare-and-swap on (status = t.From, version =
	// t.ExpectedVersion, pending_edit_key = t.ExpectedPendingEditKey, ""
	// meaning NULL). On a match it writes t.ApplyTo(current), appends one
	// errand_transitions row, writes the review row (below) and inserts
	// t.Outbox, all in one transaction. Apply never retries and never re-runs Next: on a mismatch the caller
	// re-reads the errand and decides again (or reports the conflict).
	//
	// An EventEdit transition that moves the errand to another supplier
	// (t.Changes.Details, or the pending edit's Details, has a SupplierID
	// different from the stored one) is fenced like Create: in the same
	// transaction Apply takes the shared advisory lock on the new supplier's
	// key and refuses if it has an OPEN or COMMITTED fence
	// (docs/contracts.md). EventEditConfirmed is not checked: Fence Acquire
	// counts pending edits towards their target supplier, so a confirmed
	// edit can never land on a fenced supplier.
	//
	// Review rows (F6.1):
	//   - t.To is REVIEW_REQUIRED (ReportIssue, Dispute,
	//     DeliveryDeadlinePassed): insert an open errand_reviews row with
	//     reason = the new ReviewReason, reporter_id = t.Actor.ID (NULL for
	//     SYSTEM), note = t.Reason (NULL when blank) and created_at = t.At.
	//   - t.From is REVIEW_REQUIRED (AdminComplete, AdminRefund): set the
	//     errand's open review's resolution = t.To, resolver_id = t.Actor.ID,
	//     resolution_reason = t.Reason and resolved_at = t.At. No open
	//     review is a plain error: it signals a bug.
	//
	// Errors:
	//   - ERRAND_NOT_FOUND: no errand has t.ErrandID.
	//   - ERRAND_UNAVAILABLE: the errand's status is no longer t.From (it
	//     was accepted, cancelled, expired… in the meantime, F3.3.7).
	//   - ERRAND_CHANGED: the status matches but the version or the pending
	//     edit does not (the caller's view is stale).
	//   - SUPPLIER_DELETION_IN_PROGRESS: an edit's new supplier is fenced.
	//   - INVALID_ARGUMENT: t is a Submit transition (use Create).
	// A row whose outbox idempotency key already exists aborts the whole
	// transaction with a plain error; Next never produces one, so it signals
	// a bug.
	Apply(ctx context.Context, t Transition) (Errand, error)

	// Get reads one errand. Errors: ERRAND_NOT_FOUND.
	Get(ctx context.Context, id string) (Errand, error)

	// RecordRejection inserts one event.errand.<action>_rejected outbox row
	// for a request that was refused with a 409 by a state check (A4, A5,
	// A6), so the person who made it is notified. It changes no errand and
	// records no transition.
	//
	// The row's idempotency key is RejectionKey(r.ErrandID, r.Action,
	// r.RequestID); a second call with the same key is a no-op (ON CONFLICT
	// DO NOTHING), so a retried HTTP request notifies once. The payload is a
	// RejectionData carrying the errand's current status and version, read
	// in the same transaction.
	//
	// Errors: ERRAND_NOT_FOUND; INVALID_ARGUMENT for an unknown action or an
	// empty request ID.
	RecordRejection(ctx context.Context, r Rejection) error

	// ListOpen lists OPEN errands whose acceptance deadline is after q.Now
	// (an errand past its deadline is hidden before the expiry job runs,
	// A3), ordered by (acceptance_deadline, id) ascending, using the
	// errands_open_by_supplier_idx / errands_acceptance_due_idx indexes.
	// Errors: INVALID_ARGUMENT for a limit outside 1..MaxListLimit.
	ListOpen(ctx context.Context, q OpenQuery) (Page[OpenCursor], error)

	// ListRequested lists the active (non-terminal) errands q.UserID
	// requested, newest first by (created_at, id) (F3.11).
	// Errors: INVALID_ARGUMENT for a limit outside 1..MaxListLimit.
	ListRequested(ctx context.Context, q UserListQuery) (Page[UserListCursor], error)

	// ListDelivering lists the active errands assigned to q.UserID as
	// courier, most recently updated first by (updated_at, id) (F3.12).
	// Errors: INVALID_ARGUMENT for a limit outside 1..MaxListLimit.
	ListDelivering(ctx context.Context, q UserListQuery) (Page[UserListCursor], error)

	// ListHistory lists the terminal errands in which q.UserID is the
	// requester or the courier, most recently updated first by (updated_at,
	// id) (F3.13). A courier who withdrew is no longer the errand's courier,
	// so that errand is not in their history.
	// Errors: INVALID_ARGUMENT for a limit outside 1..MaxListLimit.
	ListHistory(ctx context.Context, q UserListQuery) (Page[UserListCursor], error)

	// CourierHistory reads the withdrawal history Accept needs for
	// eligibility (F3.3.6): whether courierID ever withdrew from errandID,
	// and how many withdrawals courierID has at or after since (F3.6.4).
	// Both voluntary (EventWithdraw) and forced (EventPickupDeadlinePassed)
	// withdrawals count; a row is courierID's when its courier_id is
	// courierID, whoever the actor was. It reads errand_transitions only
	// (errand_transitions_withdrawals_idx).
	CourierHistory(ctx context.Context, errandID, courierID string, since time.Time) (CourierHistory, error)

	// ClaimDue runs one batch of a deadline or reconciler job.
	//
	// In one transaction it selects up to limit errands that are due for
	// kind at now (see DueKind for each WHERE clause), ordered by the kind's
	// column then id, with FOR UPDATE SKIP LOCKED, so concurrent callers
	// (several replicas, or a job and a user request) never get the same
	// errand. For each errand it calls decide while holding the row lock:
	//
	//   - (t, true, nil): t is applied exactly as Apply would apply it,
	//     including its review row.
	//   - (_, false, nil): the errand is left unchanged (for example its
	//     deadline was extended, or the reserve is still unsent).
	//   - (_, _, err): the whole batch is rolled back and err is returned
	//     with applied = 0.
	//
	// decide should therefore return skip for any refusal it expects for a
	// single errand (including apperror codes from Next) and an error only
	// when the whole batch cannot proceed (for example settings could not be
	// read). It must not call other Store methods for the same errand: the
	// row is locked by this transaction.
	//
	// applied is the number of transitions committed. Callers that loop
	// while a batch comes back full should compare the number of errands
	// decide saw with limit, not applied, because skipped errands count
	// towards the limit.
	ClaimDue(ctx context.Context, kind DueKind, now time.Time, limit int, decide DecideFunc) (applied int, err error)
}

// DecideFunc chooses the transition ClaimDue applies to one locked errand.
type DecideFunc func(Errand) (t Transition, apply bool, err error)

// MaxListLimit is the largest page any Store list method returns.
const MaxListLimit = 500

// Page is one page of a Store list. Next is nil on the last page; otherwise
// it is passed back as the query's After to read the following page.
type Page[C any] struct {
	Items []Errand
	Next  *C
}

// OpenQuery selects open errands (A3).
type OpenQuery struct {
	// Now hides errands whose acceptance deadline is at or before it.
	Now time.Time
	// SupplierID, when non-empty, keeps only errands picked up from that
	// supplier (F3.2.2).
	SupplierID string
	After      *OpenCursor
	Limit      int
}

// OpenCursor is the position after the last errand of an open-list page.
type OpenCursor struct {
	AcceptanceDeadline time.Time
	ID                 string
}

// UserListQuery selects one user's errands (A7).
type UserListQuery struct {
	UserID string
	After  *UserListCursor
	Limit  int
}

// UserListCursor is the position after the last errand of a per-user page.
// At is created_at for ListRequested and updated_at for ListDelivering and
// ListHistory.
type UserListCursor struct {
	At time.Time
	ID string
}

// CourierHistory is what CourierHistory reports for one courier.
type CourierHistory struct {
	// WithdrewFromErrand feeds EventInput.PreviouslyWithdrew (F3.6.3).
	WithdrewFromErrand bool
	// WithdrawalsSince is compared with the restriction threshold from
	// Settings to set EventInput.Restricted (F3.6.4).
	WithdrawalsSince int
}

// ---------------------------------------------------------------------------
// Rejections
// ---------------------------------------------------------------------------

// RejectedAction is a request whose 409 refusal is announced as an event.
type RejectedAction string

const (
	RejectedAccept RejectedAction = "accept"
	RejectedCancel RejectedAction = "cancel"
	RejectedPickup RejectedAction = "pickup"
)

// RejectedActions lists every action RecordRejection accepts.
func RejectedActions() []RejectedAction {
	return []RejectedAction{RejectedAccept, RejectedCancel, RejectedPickup}
}

// Valid reports whether a is one of RejectedActions.
func (a RejectedAction) Valid() bool {
	for _, known := range RejectedActions() {
		if a == known {
			return true
		}
	}
	return false
}

// EventName is the rejection event's routing key, e.g.
// errand.accept_rejected.
func (a RejectedAction) EventName() EventName {
	return EventName("errand." + string(a) + "_rejected")
}

// RejectionKey formats {errandId}:{action}-rejected:{requestId}, the
// idempotency key (and messageId) of a rejection event.
func RejectionKey(errandID string, action RejectedAction, requestID string) string {
	return errandID + ":" + string(action) + "-rejected:" + requestID
}

// Rejection is a refused request to announce.
type Rejection struct {
	ErrandID string
	Action   RejectedAction
	// Actor is who made the refused request and is to be notified.
	Actor Actor
	// Code is the refusal's apperror code, e.g. ERRAND_UNAVAILABLE.
	Code apperror.Code
	// RequestID identifies the HTTP request, so a retry of the same request
	// is announced once.
	RequestID string
	At        time.Time
}

// RejectionData is the payload of an event.errand.*_rejected row.
type RejectionData struct {
	ErrandID string `json:"errandId"`
	// ErrandVersion and Status are the errand's state when the request was
	// refused.
	ErrandVersion int            `json:"errandVersion"`
	Status        Status         `json:"status"`
	Action        RejectedAction `json:"action"`
	Code          apperror.Code  `json:"code"`
	ActorType     ActorType      `json:"actorType"`
	ActorID       string         `json:"actorId"`
	At            time.Time      `json:"at"`
}

// ---------------------------------------------------------------------------
// Due kinds
// ---------------------------------------------------------------------------

// DueKind names one ClaimDue job. Each kind's WHERE clause is exactly the
// predicate of its partial index in migrations/00001_create_order_schema.sql
// plus "column <= $now", so the selection is an index range scan.
//
// For the four deadline kinds now is the current time. For StuckReservation
// and StuckEdit now is the cutoff: the caller passes the current time minus
// the timeout from Settings, so "created more than 5 minutes ago" becomes
// "created_at <= now".
type DueKind string

const (
	// AcceptanceExpiry: status = 'OPEN' AND acceptance_deadline <= $now
	// (errands_acceptance_due_idx). Job: OPEN → EXPIRED (B4a).
	AcceptanceExpiry DueKind = "acceptance_expiry"
	// PickupDeadline: status = 'ASSIGNED' AND pickup_deadline <= $now
	// (errands_pickup_due_idx). Job: forced withdrawal (B4a).
	PickupDeadline DueKind = "pickup_deadline"
	// DeliveryDeadline: status = 'PICKED_UP' AND delivery_deadline <= $now
	// (errands_delivery_due_idx). Job: → REVIEW_REQUIRED (B4a).
	DeliveryDeadline DueKind = "delivery_deadline"
	// VerificationLapse: status = 'DELIVERED' AND verification_deadline <=
	// $now (errands_verification_due_idx). Job: → COMPLETED (B4a).
	VerificationLapse DueKind = "verification_lapse"
	// StuckReservation: status = 'PENDING_RESERVATION' AND created_at <=
	// $now (errands_stuck_reservation_idx). Job: → REJECTED (M5).
	StuckReservation DueKind = "stuck_reservation"
	// StuckEdit: pending_edit IS NOT NULL AND pending_edit_at <= $now
	// (errands_stuck_edit_idx). Job: EditRefused (M5).
	StuckEdit DueKind = "stuck_edit"
)

// DueKinds lists every kind.
func DueKinds() []DueKind {
	return []DueKind{
		AcceptanceExpiry, PickupDeadline, DeliveryDeadline,
		VerificationLapse, StuckReservation, StuckEdit,
	}
}

// DueAt returns the value of k's column for e, and false when e does not
// match k's index predicate (wrong status, or a NULL column).
func (k DueKind) DueAt(e Errand) (time.Time, bool) {
	at := func(status Status, t *time.Time) (time.Time, bool) {
		if e.Status != status || t == nil {
			return time.Time{}, false
		}
		return *t, true
	}
	switch k {
	case AcceptanceExpiry:
		return at(StatusOpen, &e.Details.AcceptanceDeadline)
	case PickupDeadline:
		return at(StatusAssigned, e.PickupDeadline)
	case DeliveryDeadline:
		return at(StatusPickedUp, e.DeliveryDeadline)
	case VerificationLapse:
		return at(StatusDelivered, e.VerificationDeadline)
	case StuckReservation:
		return at(StatusPendingReservation, &e.CreatedAt)
	case StuckEdit:
		if e.PendingEdit == nil {
			return time.Time{}, false
		}
		return e.PendingEdit.At, true
	default:
		return time.Time{}, false
	}
}

// Due reports whether ClaimDue(k, now) selects e: e matches k's predicate
// and its column is at or before now.
func (k DueKind) Due(e Errand, now time.Time) bool {
	at, ok := k.DueAt(e)
	return ok && !at.After(now)
}

// ---------------------------------------------------------------------------
// SupplierGate
// ---------------------------------------------------------------------------

// SupplierGate reads supplier-service (B2c). Fences are not its concern:
// Store.Create and Store.Apply check them inside their transactions.
type SupplierGate interface {
	// ResolveForCreate returns the current version of supplierID if the
	// supplier can take a new errand at now: it exists, is not deleted, and
	// now (in Asia/Singapore) is within its opening hours, including hours
	// that cross midnight (F3.1.1, F2.5.2). It is never served from a cache.
	// Errors: SUPPLIER_UNAVAILABLE (missing, deleted or closed);
	// DEPENDENCY_UNAVAILABLE (supplier-service unreachable or failing).
	ResolveForCreate(ctx context.Context, supplierID string, now time.Time) (SupplierVersion, error)

	// GetVersion returns one immutable supplier version, also for deleted
	// suppliers (F2.4.3, F2.5.1). Results may be cached indefinitely.
	// Errors: SUPPLIER_UNAVAILABLE (no such version); DEPENDENCY_UNAVAILABLE.
	GetVersion(ctx context.Context, versionID string) (SupplierVersion, error)

	// ListBuildings returns supplier-service's building list, including
	// retired buildings so old snapshots still resolve. A2 stores the chosen
	// building as the errand's delivery snapshot.
	// Errors: DEPENDENCY_UNAVAILABLE.
	ListBuildings(ctx context.Context) ([]Building, error)
}

// SupplierVersion is supplier-service's SupplierVersion schema.
type SupplierVersion struct {
	SupplierID          string  `json:"supplierId"`
	VersionID           string  `json:"versionId"`
	Name                string  `json:"name"`
	Type                string  `json:"type"`
	TypeID              string  `json:"typeId"`
	Building            string  `json:"building"`
	BuildingID          string  `json:"buildingId"`
	Floor               string  `json:"floor"`
	LocationDescription string  `json:"locationDescription"`
	Latitude            float64 `json:"latitude"`
	Longitude           float64 `json:"longitude"`
	// OpeningTime and ClosingTime are "HH:MM" in Asia/Singapore. A closing
	// time at or before the opening time means the hours cross midnight.
	OpeningTime string `json:"openingTime"`
	ClosingTime string `json:"closingTime"`
	ImageURL    string `json:"imageUrl,omitempty"`
	// Available is true only for the current version of a non-deleted
	// supplier.
	Available bool      `json:"available"`
	CreatedAt time.Time `json:"createdAt"`
}

// Building is one row of supplier-service's building lookup.
type Building struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Retired bool   `json:"retired"`
}

// ---------------------------------------------------------------------------
// UserDirectory
// ---------------------------------------------------------------------------

// UserDirectory reads contact details from user-service (B2c, gate C5).
// Callers show them only as NFR3.6.4 allows: to the other party of an
// active, assigned errand. Implementations never log the mobile number.
type UserDirectory interface {
	// GetContact returns userID's display name and mobile number. found is
	// false, with a nil error, when user-service has no such user (for
	// example a deleted account); callers then omit the contact.
	// Errors: DEPENDENCY_UNAVAILABLE.
	GetContact(ctx context.Context, userID string) (contact UserContact, found bool, err error)
}

// UserContact is what one party of an errand may see of the other.
type UserContact struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Mobile      string `json:"mobile"`
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// Settings reads the admin-configurable settings (B1, B5b). Admin updates
// go through B's own repository, not this port.
type Settings interface {
	// Get returns the current settings. A key that has never been stored
	// has its DefaultSystemSettings value. Get fails only when the settings
	// cannot be read at all; callers then refuse the operation rather than
	// guess.
	Get(ctx context.Context) (SystemSettings, error)
}

// SystemSettings are the values in the settings table.
type SystemSettings struct {
	Deadlines DeadlineSettings
	// WithdrawalRestriction is F3.6.4 and F6.3.1: Count withdrawals within
	// Window restrict the courier for Duration.
	WithdrawalRestriction WithdrawalRestriction
	// CancellationFlag is F3.5.3 and F6.3.2: more than Count cancellations
	// within Window flag the requester.
	CancellationFlag CancellationFlag
	// ReservationTimeout and EditTimeout are how long a reserve or adjust may
	// wait for a credit result before the reconcilers act (M5).
	ReservationTimeout time.Duration
	EditTimeout        time.Duration
	// OutboxConfirmTimeout is how long a SENT credit row may wait for its
	// result before the dispatcher re-sends it (M3).
	OutboxConfirmTimeout time.Duration
}

// WithdrawalRestriction is the courier restriction rule (F3.6.4).
type WithdrawalRestriction struct {
	Count    int
	Window   time.Duration
	Duration time.Duration
}

// CancellationFlag is the requester flag rule (F3.5.3).
type CancellationFlag struct {
	Count  int
	Window time.Duration
}

// DefaultSystemSettings are the defaults in order-requirements.md and
// commit-plan-b.md B1.
func DefaultSystemSettings() SystemSettings {
	return SystemSettings{
		Deadlines: DefaultDeadlineSettings(),
		WithdrawalRestriction: WithdrawalRestriction{
			Count:    3,
			Window:   15 * time.Minute,
			Duration: 15 * time.Minute,
		},
		CancellationFlag: CancellationFlag{
			Count:  20,
			Window: time.Hour,
		},
		ReservationTimeout:   5 * time.Minute,
		EditTimeout:          5 * time.Minute,
		OutboxConfirmTimeout: 10 * time.Minute,
	}
}

// Validate rejects non-positive durations and counts, and invalid deadline
// settings, with VALIDATION_FAILED and one field per problem.
func (s SystemSettings) Validate() error {
	var fields []apperror.Field
	if err := s.Deadlines.Validate(); err != nil {
		var appErr *apperror.Error
		if errors.As(err, &appErr) {
			fields = append(fields, appErr.Fields...)
		}
	}
	positive := func(name string, ok bool) {
		if !ok {
			fields = append(fields, apperror.Field{Field: name, Message: "must be positive"})
		}
	}
	positive("withdrawalRestriction.count", s.WithdrawalRestriction.Count > 0)
	positive("withdrawalRestriction.window", s.WithdrawalRestriction.Window > 0)
	positive("withdrawalRestriction.duration", s.WithdrawalRestriction.Duration > 0)
	positive("cancellationFlag.count", s.CancellationFlag.Count > 0)
	positive("cancellationFlag.window", s.CancellationFlag.Window > 0)
	positive("reservationTimeout", s.ReservationTimeout > 0)
	positive("editTimeout", s.EditTimeout > 0)
	positive("outboxConfirmTimeout", s.OutboxConfirmTimeout > 0)
	if len(fields) > 0 {
		return &apperror.Error{Code: apperror.ValidationFailed, Message: "settings are invalid", Fields: fields}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Publisher and the message contract
// ---------------------------------------------------------------------------

// Publisher sends one message to RabbitMQ (M2). It is used by the outbox
// dispatcher only (M3); everything else writes outbox rows.
type Publisher interface {
	// Publish sends m as a persistent message with MessageId =
	// m.Envelope.MessageID and Type = m.RoutingKey, and returns nil only
	// after the broker has confirmed it.
	//
	// Any error means the message may or may not have reached the broker;
	// the caller retries it later with the same MessageID, and consumers
	// deduplicate by it. errors.Is(err, ErrUnpublishable) marks a message
	// that can never be sent (for example its envelope cannot be encoded);
	// the caller marks it failed instead of retrying.
	Publish(ctx context.Context, m Message) error
}

// ErrUnpublishable wraps Publish errors that retrying cannot fix.
var ErrUnpublishable = errors.New("message cannot be published")

// Message is one message for an exchange.
type Message struct {
	Exchange   string
	RoutingKey string
	Envelope   Envelope
}

// Envelope is the JSON body of every message order-service sends or
// receives: credit commands, credit results and errand events.
type Envelope struct {
	// MessageID is the outbox row's idempotency key for messages
	// order-service sends; consumers deduplicate by it.
	MessageID string `json:"messageId"`
	// CorrelationID is set on credit results only: the MessageID of the
	// command answered.
	CorrelationID string `json:"correlationId,omitempty"`
	// Type is the routing key.
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurredAt"`
	ErrandID   string    `json:"errandId"`
	// ErrandVersion lets consumers ignore an event older than one they have
	// applied, because redelivery can reorder messages.
	ErrandVersion int `json:"errandVersion"`
	// Data is a CreditCommand, a CreditResult, an EventData or a
	// RejectionData, depending on Type.
	Data json.RawMessage `json:"data"`
}

// Exchanges, queues and bindings (docs/contracts.md).
const (
	// ExchangeEvents is the durable topic exchange for errand events and
	// credit results.
	ExchangeEvents = "foc.events"
	// ExchangeCommands is the durable direct exchange for credit commands.
	ExchangeCommands = "foc.commands"

	// QueueCreditCommands is owned and declared by credit-service; order-
	// service never declares it.
	QueueCreditCommands = "credit-service.commands"
	// QueueCreditResults is order-service's queue for credit results, bound
	// to ExchangeEvents with BindingCreditResults.
	QueueCreditResults    = "order-service.credit-results"
	QueueCreditResultsDLQ = "order-service.credit-results.dlq"
	BindingCreditResults  = "credit.#"
	// QueueEventsTap is the optional demo queue bound with BindingErrandEvents.
	QueueEventsTap      = "order-events.tap"
	BindingErrandEvents = "errand.#"
)

// Credit command routing keys on ExchangeCommands. They equal the outbox row
// kinds.
const (
	CommandCreditReserve  = KindCreditReserve
	CommandCreditAdjust   = KindCreditAdjust
	CommandCreditRelease  = KindCreditRelease
	CommandCreditTransfer = KindCreditTransfer
)

// CreditCommands lists every command routing key.
func CreditCommands() []string {
	return []string{CommandCreditReserve, CommandCreditAdjust, CommandCreditRelease, CommandCreditTransfer}
}

// Credit result routing keys on ExchangeEvents.
const (
	ResultCreditReserved           = "credit.reserved"
	ResultCreditReservationRefused = "credit.reservation_refused"
	ResultCreditAdjusted           = "credit.adjusted"
	ResultCreditAdjustRefused      = "credit.adjust_refused"
	ResultCreditReleased           = "credit.released"
	ResultCreditTransferred        = "credit.transferred"
	// ResultCreditOperationFailed is a contract bug or an impossible state;
	// the command's row is marked FAILED and never retried.
	ResultCreditOperationFailed = "credit.operation_failed"
)

// CreditResults lists every result routing key.
func CreditResults() []string {
	return []string{
		ResultCreditReserved, ResultCreditReservationRefused,
		ResultCreditAdjusted, ResultCreditAdjustRefused,
		ResultCreditReleased, ResultCreditTransferred,
		ResultCreditOperationFailed,
	}
}

// Codes in CreditResult.Code.
const (
	CreditCodeInsufficientCredits           = "INSUFFICIENT_CREDITS"
	CreditCodeAccountNotFound               = "ACCOUNT_NOT_FOUND"
	CreditCodeReservationReleased           = "RESERVATION_RELEASED"
	CreditCodeReservationNotActive          = "RESERVATION_NOT_ACTIVE"
	CreditCodeReservationAlreadyTransferred = "RESERVATION_ALREADY_TRANSFERRED"
	CreditCodeReservationAlreadyReleased    = "RESERVATION_ALREADY_RELEASED"
)

// CreditResult is the data of a credit.* result. Amount is the reservation's
// amount after the command (credit.reserved, credit.adjusted); Seq echoes
// the adjust's seq (credit.adjusted, credit.adjust_refused); Code is set on
// refusals and credit.operation_failed.
type CreditResult struct {
	Amount int    `json:"amount,omitempty"`
	Seq    int    `json:"seq,omitempty"`
	Code   string `json:"code,omitempty"`
}

// Reminder events. They are not transitions: B's scheduler inserts their
// outbox rows directly (B4b).
const (
	ErrandVerificationReminder      EventName = "errand.verification_reminder"
	ErrandPickupDeadlineApproaching EventName = "errand.pickup_deadline_approaching"
)

// EventNames lists every routing key order-service publishes on
// ExchangeEvents: transition, rejection and reminder events.
func EventNames() []EventName {
	names := TransitionEventNames()
	for _, a := range RejectedActions() {
		names = append(names, a.EventName())
	}
	return append(names, ErrandVerificationReminder, ErrandPickupDeadlineApproaching)
}

// ---------------------------------------------------------------------------
// OpenErrandCache
// ---------------------------------------------------------------------------

// OpenErrandCache holds the whole open-errand set for a few seconds (A3,
// B2b). It is a display optimisation only: nothing that is checked before a
// write reads from it, and Postgres stays the source of truth.
type OpenErrandCache interface {
	// Get returns the cached set and true, or nil and false on a miss. An
	// error (Redis down, timeout, open breaker, bad data) is reported as a
	// miss, never returned.
	Get(ctx context.Context) ([]OpenErrandView, bool)
	// Set replaces the cached set for ttl. Failures are swallowed.
	Set(ctx context.Context, views []OpenErrandView, ttl time.Duration)
}

// OpenErrandView is the redacted open-list projection: what any courier may
// see of an open errand. It has no requester identity and no requester notes
// (NFR3.6.4, NFR3.6.3), so it is the only errand shape that may be cached.
// Courier coordinates are never part of it; A3 orders by distance per
// request.
type OpenErrandView struct {
	ErrandID string `json:"errandId"`
	Version  int    `json:"version"`

	SupplierID        string          `json:"supplierId"`
	SupplierVersionID string          `json:"supplierVersionId"`
	Pickup            OpenPickupView  `json:"pickup"`
	DeliveryBuilding  json.RawMessage `json:"deliveryBuilding"`
	DeliveryDetail    string          `json:"deliveryDetail"`
	Items             json.RawMessage `json:"items"`

	Reward             int       `json:"reward"`
	CreatedAt          time.Time `json:"createdAt"`
	AcceptanceDeadline time.Time `json:"acceptanceDeadline"`
}

// OpenPickupView is the pickup location from the errand's supplier version.
type OpenPickupView struct {
	Name                string  `json:"name"`
	Building            string  `json:"building"`
	Floor               string  `json:"floor"`
	LocationDescription string  `json:"locationDescription"`
	Latitude            float64 `json:"latitude"`
	Longitude           float64 `json:"longitude"`
}
