package testfakes

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// FakeStore is an in-memory errand.Store with the same contract as the
// Postgres one: every change is a transition from errand.Next, applied with
// the version compare-and-swap, and records the transition and outbox rows a
// real transaction would have written.
//
// ClaimDue models FOR UPDATE SKIP LOCKED: while decide runs, the selected
// errands are locked, other ClaimDue calls skip them, and Apply on one of
// them waits until the batch commits or rolls back (and then usually fails
// its compare-and-swap), as a blocked UPDATE would.
type FakeStore struct {
	Faults

	mu          sync.Mutex
	unlocked    *sync.Cond
	errands     map[string]errand.Errand
	createKeys  map[[2]string]string
	fenced      map[string]bool
	locked      map[string]bool
	transitions []TransitionRecord
	reviews     []ReviewRecord
	outbox      []OutboxRecord
	outboxKeys  map[string]bool
}

var _ errand.Store = (*FakeStore)(nil)

// TransitionRecord is one errand_transitions row.
type TransitionRecord struct {
	errand.Transition
	// CourierID is the courier_id column: the errand's courier before the
	// transition, or after it when there was none before (an accept).
	// CourierHistory reads it to attribute forced withdrawals.
	CourierID string
}

// ReviewRecord is one errand_reviews row. Empty strings and a nil
// ResolvedAt stand for NULL columns; Resolution is empty while it is open.
type ReviewRecord struct {
	ErrandID         string
	Reason           errand.ReviewReason
	ReporterID       string
	Note             string
	CreatedAt        time.Time
	Resolution       errand.Status
	ResolverID       string
	ResolutionReason string
	ResolvedAt       *time.Time
}

// OutboxRecord is one outbox row as inserted, in insertion order.
type OutboxRecord struct {
	errand.OutboxRow
	// ErrandVersion is the errand's version after the write.
	ErrandVersion int
	At            time.Time
}

// NewFakeStore returns an empty store.
func NewFakeStore() *FakeStore {
	s := &FakeStore{
		errands:    map[string]errand.Errand{},
		createKeys: map[[2]string]string{},
		fenced:     map[string]bool{},
		locked:     map[string]bool{},
		outboxKeys: map[string]bool{},
	}
	s.unlocked = sync.NewCond(&s.mu)
	return s
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// Put stores e as is, replacing any errand with the same ID, without a
// transition or outbox rows. Use it to start a test from any state. An
// errand put in REVIEW_REQUIRED without an open review gets one, with its
// ReviewReason and no reporter, so it can be resolved.
func (s *FakeStore) Put(e errand.Errand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errands[e.ID] = cloneErrand(e)
	if e.CreateIdempotencyKey != "" {
		s.createKeys[[2]string{e.RequesterID, e.CreateIdempotencyKey}] = e.ID
	}
	if e.Status == errand.StatusReviewRequired && s.openReview(e.ID) < 0 {
		s.reviews = append(s.reviews, ReviewRecord{ErrandID: e.ID, Reason: e.ReviewReason, CreatedAt: e.UpdatedAt})
	}
}

// Errand returns the stored errand, if any.
func (s *FakeStore) Errand(id string) (errand.Errand, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.errands[id]
	return cloneErrand(e), ok
}

// Fence makes Create refuse new errands for supplierID, and Apply refuse an
// edit that moves an errand to it, as an OPEN or COMMITTED supplier fence
// would. Unfence lifts it.
func (s *FakeStore) Fence(supplierID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fenced[supplierID] = true
}

func (s *FakeStore) Unfence(supplierID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fenced, supplierID)
}

// Transitions returns every recorded transition in order.
func (s *FakeStore) Transitions() []TransitionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.transitions)
}

// Reviews returns every errand_reviews row in insertion order.
func (s *FakeStore) Reviews() []ReviewRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.reviews)
	for i, r := range out {
		if r.ResolvedAt != nil {
			at := *r.ResolvedAt
			out[i].ResolvedAt = &at
		}
	}
	return out
}

// Outbox returns every outbox row in insertion order.
func (s *FakeStore) Outbox() []OutboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.outbox)
}

// OutboxKinds returns the outbox rows of one kind, in insertion order.
func (s *FakeStore) OutboxKinds(kind string) []OutboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []OutboxRecord
	for _, r := range s.outbox {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// Submit runs errand.Next(e, Submit) as e's requester and Create.
func (s *FakeStore) Submit(ctx context.Context, e errand.Errand, now time.Time) (errand.Errand, error) {
	actor := errand.Actor{Type: errand.ActorRequester, ID: e.RequesterID}
	t, err := errand.Next(e, errand.EventSubmit, actor, errand.EventInput{}, now)
	if err != nil {
		return errand.Errand{}, err
	}
	stored, _, err := s.Create(ctx, e, t)
	return stored, err
}

// Fire reads errand id, runs errand.Next and applies the result, returning
// the stored errand and the transition.
func (s *FakeStore) Fire(ctx context.Context, id string, ev errand.Event, actor errand.Actor, input errand.EventInput, now time.Time) (errand.Errand, errand.Transition, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return errand.Errand{}, errand.Transition{}, err
	}
	t, err := errand.Next(e, ev, actor, input, now)
	if err != nil {
		return errand.Errand{}, errand.Transition{}, err
	}
	stored, err := s.Apply(ctx, t)
	return stored, t, err
}

// ---------------------------------------------------------------------------
// errand.Store
// ---------------------------------------------------------------------------

func (s *FakeStore) Create(_ context.Context, e errand.Errand, t errand.Transition) (errand.Errand, bool, error) {
	if err := s.fault("Create"); err != nil {
		return errand.Errand{}, false, err
	}
	switch {
	case e.Status != errand.StatusNone, t.Event != errand.EventSubmit,
		t.From != errand.StatusNone, t.ErrandID != e.ID:
		return errand.Errand{}, false, appErr(apperror.InvalidArgument, "not a Submit transition for errand %q", e.ID)
	case e.CreateIdempotencyKey == "":
		return errand.Errand{}, false, appErr(apperror.InvalidArgument, "create idempotency key is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]string{e.RequesterID, e.CreateIdempotencyKey}
	if id, ok := s.createKeys[key]; ok {
		return cloneErrand(s.errands[id]), false, nil
	}
	if s.fenced[e.Details.SupplierID] {
		return errand.Errand{}, false, appErr(apperror.SupplierDeletionInProgress, "supplier %q is being deleted", e.Details.SupplierID)
	}
	if _, exists := s.errands[e.ID]; exists {
		return errand.Errand{}, false, fmt.Errorf("testfakes: errand %q already exists", e.ID)
	}
	if err := s.checkOutboxKeys(t.Outbox, nil); err != nil {
		return errand.Errand{}, false, err
	}
	stored := s.commit(e, t)
	s.createKeys[key] = e.ID
	return cloneErrand(stored), true, nil
}

func (s *FakeStore) Apply(_ context.Context, t errand.Transition) (errand.Errand, error) {
	if err := s.fault("Apply"); err != nil {
		return errand.Errand{}, err
	}
	if t.From == errand.StatusNone {
		return errand.Errand{}, appErr(apperror.InvalidArgument, "use Create for a Submit transition")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for s.locked[t.ErrandID] {
		s.unlocked.Wait()
	}
	cur, ok := s.errands[t.ErrandID]
	if !ok {
		return errand.Errand{}, appErr(apperror.ErrandNotFound, "errand %q not found", t.ErrandID)
	}
	if err := compareAndSwap(cur, t); err != nil {
		return errand.Errand{}, err
	}
	if to, ok := editTargetSupplier(cur, t); ok && s.fenced[to] {
		return errand.Errand{}, appErr(apperror.SupplierDeletionInProgress, "supplier %q is being deleted", to)
	}
	if t.From == errand.StatusReviewRequired && s.openReview(t.ErrandID) < 0 {
		return errand.Errand{}, fmt.Errorf("testfakes: errand %q has no open review to resolve", t.ErrandID)
	}
	if err := s.checkOutboxKeys(t.Outbox, nil); err != nil {
		return errand.Errand{}, err
	}
	return cloneErrand(s.commit(cur, t)), nil
}

// editTargetSupplier returns the supplier an EventEdit transition moves cur
// to, applied at once or stored as a pending edit, and false when t is not
// an edit or keeps the supplier.
func editTargetSupplier(cur errand.Errand, t errand.Transition) (string, bool) {
	if t.Event != errand.EventEdit {
		return "", false
	}
	var to string
	switch c := t.Changes; {
	case c.Details != nil:
		to = c.Details.SupplierID
	case c.PendingEdit.Value != nil:
		to = c.PendingEdit.Value.Details.SupplierID
	}
	return to, to != "" && to != cur.Details.SupplierID
}

func (s *FakeStore) Get(_ context.Context, id string) (errand.Errand, error) {
	if err := s.fault("Get"); err != nil {
		return errand.Errand{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.errands[id]
	if !ok {
		return errand.Errand{}, appErr(apperror.ErrandNotFound, "errand %q not found", id)
	}
	return cloneErrand(e), nil
}

func (s *FakeStore) RecordRejection(_ context.Context, r errand.Rejection) error {
	if err := s.fault("RecordRejection"); err != nil {
		return err
	}
	if !r.Action.Valid() {
		return appErr(apperror.InvalidArgument, "unknown rejected action %q", r.Action)
	}
	if r.RequestID == "" {
		return appErr(apperror.InvalidArgument, "request ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.errands[r.ErrandID]
	if !ok {
		return appErr(apperror.ErrandNotFound, "errand %q not found", r.ErrandID)
	}
	key := errand.RejectionKey(r.ErrandID, r.Action, r.RequestID)
	if s.outboxKeys[key] {
		return nil
	}
	s.outboxKeys[key] = true
	s.outbox = append(s.outbox, OutboxRecord{
		OutboxRow: errand.OutboxRow{
			Kind:           r.Action.EventName().Kind(),
			IdempotencyKey: key,
			ErrandID:       r.ErrandID,
			Payload: errand.RejectionData{
				ErrandID:      r.ErrandID,
				ErrandVersion: cur.Version,
				Status:        cur.Status,
				Action:        r.Action,
				Code:          r.Code,
				ActorType:     r.Actor.Type,
				ActorID:       r.Actor.ID,
				At:            r.At,
			},
		},
		ErrandVersion: cur.Version,
		At:            r.At,
	})
	return nil
}

func (s *FakeStore) ListOpen(_ context.Context, q errand.OpenQuery) (errand.Page[errand.OpenCursor], error) {
	if err := s.fault("ListOpen"); err != nil {
		return errand.Page[errand.OpenCursor]{}, err
	}
	if err := checkLimit(q.Limit); err != nil {
		return errand.Page[errand.OpenCursor]{}, err
	}
	items := s.filter(func(e errand.Errand) bool {
		return e.Status == errand.StatusOpen &&
			e.Details.AcceptanceDeadline.After(q.Now) &&
			(q.SupplierID == "" || e.Details.SupplierID == q.SupplierID)
	})
	key := func(e errand.Errand) errand.OpenCursor {
		return errand.OpenCursor{AcceptanceDeadline: e.Details.AcceptanceDeadline, ID: e.ID}
	}
	compare := func(a, b errand.OpenCursor) int {
		return cmp.Or(a.AcceptanceDeadline.Compare(b.AcceptanceDeadline), cmp.Compare(a.ID, b.ID))
	}
	return paginate(items, key, compare, q.After, q.Limit), nil
}

func (s *FakeStore) ListRequested(_ context.Context, q errand.UserListQuery) (errand.Page[errand.UserListCursor], error) {
	return s.listUser("ListRequested", q, func(e errand.Errand) bool {
		return e.RequesterID == q.UserID && e.Status.Active()
	}, func(e errand.Errand) time.Time { return e.CreatedAt })
}

func (s *FakeStore) ListDelivering(_ context.Context, q errand.UserListQuery) (errand.Page[errand.UserListCursor], error) {
	return s.listUser("ListDelivering", q, func(e errand.Errand) bool {
		return e.CourierID == q.UserID && e.Status.Active()
	}, func(e errand.Errand) time.Time { return e.UpdatedAt })
}

func (s *FakeStore) ListHistory(_ context.Context, q errand.UserListQuery) (errand.Page[errand.UserListCursor], error) {
	requested := q.Role == errand.HistoryAny || q.Role == errand.HistoryRequester
	delivered := q.Role == errand.HistoryAny || q.Role == errand.HistoryCourier
	if !requested && !delivered {
		return errand.Page[errand.UserListCursor]{}, appErr(apperror.InvalidArgument, "unknown history role")
	}
	return s.listUser("ListHistory", q, func(e errand.Errand) bool {
		return ((requested && e.RequesterID == q.UserID) || (delivered && e.CourierID == q.UserID)) && e.Status.Terminal()
	}, func(e errand.Errand) time.Time { return e.UpdatedAt })
}

func (s *FakeStore) CourierHistory(_ context.Context, errandID, courierID string, since time.Time) (errand.CourierHistory, error) {
	if err := s.fault("CourierHistory"); err != nil {
		return errand.CourierHistory{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var h errand.CourierHistory
	for _, r := range s.transitions {
		if r.CourierID != courierID ||
			(r.Event != errand.EventWithdraw && r.Event != errand.EventPickupDeadlinePassed) {
			continue
		}
		if r.ErrandID == errandID {
			h.WithdrewFromErrand = true
		}
		if !r.At.Before(since) {
			h.WithdrawalsSince++
		}
	}
	return h, nil
}

func (s *FakeStore) ClaimDue(_ context.Context, kind errand.DueKind, now time.Time, limit int, decide errand.DecideFunc) (int, error) {
	if err := s.fault("ClaimDue"); err != nil {
		return 0, err
	}
	if !slices.Contains(errand.DueKinds(), kind) {
		return 0, appErr(apperror.InvalidArgument, "unknown due kind %q", kind)
	}
	if limit < 1 {
		return 0, appErr(apperror.InvalidArgument, "limit must be positive")
	}

	// SELECT … WHERE <kind> ORDER BY <column>, id LIMIT n FOR UPDATE SKIP LOCKED
	s.mu.Lock()
	var batch []errand.Errand
	for _, e := range s.errands {
		if !s.locked[e.ID] && kind.Due(e, now) {
			batch = append(batch, e)
		}
	}
	slices.SortFunc(batch, func(a, b errand.Errand) int {
		at, _ := kind.DueAt(a)
		bt, _ := kind.DueAt(b)
		return cmp.Or(at.Compare(bt), cmp.Compare(a.ID, b.ID))
	})
	if len(batch) > limit {
		batch = batch[:limit]
	}
	inBatch := map[string]errand.Errand{}
	for _, e := range batch {
		s.locked[e.ID] = true
		inBatch[e.ID] = e
	}
	s.mu.Unlock()

	// decide runs without the store mutex, so other goroutines can claim,
	// read and (for unlocked errands) apply meanwhile.
	var decided []errand.Transition
	var decideErr error
	for _, e := range batch {
		t, apply, err := decide(cloneErrand(e))
		if err != nil {
			decideErr = err
			break
		}
		if apply {
			decided = append(decided, t)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		for id := range inBatch {
			delete(s.locked, id)
		}
		s.unlocked.Broadcast()
	}()
	if decideErr != nil {
		return 0, decideErr
	}

	// The batch is one transaction: check everything before writing.
	batchKeys := map[string]bool{}
	for _, t := range decided {
		cur, ok := inBatch[t.ErrandID]
		if !ok {
			return 0, fmt.Errorf("testfakes: decide returned a transition for errand %q, which is not in the batch", t.ErrandID)
		}
		if t.From == errand.StatusNone {
			return 0, appErr(apperror.InvalidArgument, "ClaimDue cannot apply a Submit transition")
		}
		if err := compareAndSwap(cur, t); err != nil {
			return 0, fmt.Errorf("testfakes: decided transition for locked errand %q: %w", t.ErrandID, err)
		}
		if err := s.checkOutboxKeys(t.Outbox, batchKeys); err != nil {
			return 0, err
		}
		inBatch[t.ErrandID] = t.ApplyTo(cur) // a second transition for the same errand must follow this one
	}
	for _, t := range decided {
		s.commit(s.errands[t.ErrandID], t)
	}
	return len(decided), nil
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// compareAndSwap is Apply's WHERE clause.
func compareAndSwap(cur errand.Errand, t errand.Transition) error {
	if cur.Status != t.From {
		return appErr(apperror.ErrandUnavailable, "errand %q is %s, not %s", cur.ID, cur.Status, t.From)
	}
	curKey := ""
	if cur.PendingEdit != nil {
		curKey = cur.PendingEdit.Key
	}
	if cur.Version != t.ExpectedVersion || curKey != t.ExpectedPendingEditKey {
		return appErr(apperror.ErrandChanged, "errand %q has changed", cur.ID)
	}
	return nil
}

// checkOutboxKeys enforces the unique idempotency_key constraint. pending
// holds keys staged earlier in the same transaction; rows are added to it.
func (s *FakeStore) checkOutboxKeys(rows []errand.OutboxRow, pending map[string]bool) error {
	if pending == nil {
		pending = map[string]bool{}
	}
	for _, r := range rows {
		if s.outboxKeys[r.IdempotencyKey] || pending[r.IdempotencyKey] {
			return fmt.Errorf("testfakes: duplicate outbox idempotency key %q", r.IdempotencyKey)
		}
		pending[r.IdempotencyKey] = true
	}
	return nil
}

// commit writes t's errand, transition and outbox rows. s.mu must be held.
func (s *FakeStore) commit(before errand.Errand, t errand.Transition) errand.Errand {
	after := cloneErrand(t.ApplyTo(before))
	s.errands[after.ID] = after
	courier := cmp.Or(before.CourierID, after.CourierID)
	s.transitions = append(s.transitions, TransitionRecord{Transition: t, CourierID: courier})
	s.writeReview(after, t)
	for _, row := range t.Outbox {
		s.outboxKeys[row.IdempotencyKey] = true
		s.outbox = append(s.outbox, OutboxRecord{OutboxRow: row, ErrandVersion: after.Version, At: t.At})
	}
	return after
}

// writeReview opens or resolves the errand's review as Store.Apply does.
// Callers have checked that a transition out of REVIEW_REQUIRED has an open
// review.
func (s *FakeStore) writeReview(after errand.Errand, t errand.Transition) {
	switch {
	case t.To == errand.StatusReviewRequired:
		r := ReviewRecord{ErrandID: t.ErrandID, Reason: after.ReviewReason, CreatedAt: t.At}
		if t.Actor.Type != errand.ActorSystem {
			r.ReporterID = t.Actor.ID
		}
		if strings.TrimSpace(t.Reason) != "" {
			r.Note = t.Reason
		}
		s.reviews = append(s.reviews, r)
	case t.From == errand.StatusReviewRequired:
		i := s.openReview(t.ErrandID)
		at := t.At
		s.reviews[i].Resolution = t.To
		s.reviews[i].ResolverID = t.Actor.ID
		s.reviews[i].ResolutionReason = t.Reason
		s.reviews[i].ResolvedAt = &at
	}
}

// openReview returns the index of the errand's open review, or -1.
func (s *FakeStore) openReview(errandID string) int {
	return slices.IndexFunc(s.reviews, func(r ReviewRecord) bool {
		return r.ErrandID == errandID && r.Resolution == ""
	})
}

func (s *FakeStore) filter(keep func(errand.Errand) bool) []errand.Errand {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []errand.Errand
	for _, e := range s.errands {
		if keep(e) {
			out = append(out, cloneErrand(e))
		}
	}
	return out
}

func (s *FakeStore) listUser(method string, q errand.UserListQuery, keep func(errand.Errand) bool, at func(errand.Errand) time.Time) (errand.Page[errand.UserListCursor], error) {
	if err := s.fault(method); err != nil {
		return errand.Page[errand.UserListCursor]{}, err
	}
	if err := checkLimit(q.Limit); err != nil {
		return errand.Page[errand.UserListCursor]{}, err
	}
	key := func(e errand.Errand) errand.UserListCursor {
		return errand.UserListCursor{At: at(e), ID: e.ID}
	}
	// Newest first: descending on both keys.
	compare := func(a, b errand.UserListCursor) int {
		return cmp.Or(b.At.Compare(a.At), cmp.Compare(b.ID, a.ID))
	}
	return paginate(s.filter(keep), key, compare, q.After, q.Limit), nil
}

// paginate sorts items by compare on their keys, drops those at or before
// after, and returns one page.
func paginate[C any](items []errand.Errand, key func(errand.Errand) C, compare func(a, b C) int, after *C, limit int) errand.Page[C] {
	slices.SortFunc(items, func(a, b errand.Errand) int { return compare(key(a), key(b)) })
	if after != nil {
		items = slices.DeleteFunc(items, func(e errand.Errand) bool { return compare(key(e), *after) <= 0 })
	}
	page := errand.Page[C]{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		next := key(items[limit-1])
		page.Next = &next
	}
	return page
}

func checkLimit(limit int) error {
	if limit < 1 || limit > errand.MaxListLimit {
		return appErr(apperror.InvalidArgument, "limit must be between 1 and %d", errand.MaxListLimit)
	}
	return nil
}

func appErr(code apperror.Code, format string, args ...any) error {
	return &apperror.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// cloneErrand copies e deeply enough that callers cannot change stored
// state through pointers or byte slices.
func cloneErrand(e errand.Errand) errand.Errand {
	e.Details = cloneDetails(e.Details)
	e.PickupDeadline = clonePtr(e.PickupDeadline)
	e.DeliveryDeadline = clonePtr(e.DeliveryDeadline)
	e.DeliveredAt = clonePtr(e.DeliveredAt)
	e.VerificationDeadline = clonePtr(e.VerificationDeadline)
	if e.PendingEdit != nil {
		pending := *e.PendingEdit
		pending.Details = cloneDetails(pending.Details)
		e.PendingEdit = &pending
	}
	return e
}

func cloneDetails(d errand.Details) errand.Details {
	d.DeliveryBuildingSnapshot = slices.Clone(d.DeliveryBuildingSnapshot)
	d.Items = slices.Clone(d.Items)
	return d
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// cloneRaw copies a JSON value; nil stays nil.
func cloneRaw(r json.RawMessage) json.RawMessage { return slices.Clone(r) }
