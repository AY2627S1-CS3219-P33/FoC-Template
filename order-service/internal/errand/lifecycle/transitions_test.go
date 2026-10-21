package lifecycle_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// userAction is one errand route a user can call to change an errand.
type userAction struct {
	name  string
	event errand.Event
	// owner is who the route itself admits before errand.Next runs: the
	// requester for requester routes, the assigned courier for courier
	// routes, anyone for accept.
	owner errand.Relationship
	send  func(t *testing.T, w *world, token, id string, version int) *httptest.ResponseRecorder
}

func versioned(action string) func(*testing.T, *world, string, string, int) *httptest.ResponseRecorder {
	return func(t *testing.T, w *world, token, id string, version int) *httptest.ResponseRecorder {
		return w.act(t, token, id, action, version)
	}
}

func withReason(action string) func(*testing.T, *world, string, string, int) *httptest.ResponseRecorder {
	return func(t *testing.T, w *world, token, id string, version int) *httptest.ResponseRecorder {
		return w.reasoned(t, token, id, action, version, "something went wrong")
	}
}

var userActions = []userAction{
	{"edit", errand.EventEdit, errand.RelRequester,
		func(t *testing.T, w *world, token, id string, version int) *httptest.ResponseRecorder {
			return w.edit(t, token, id, version, map[string]any{"notes": "no sugar"})
		}},
	{"cancel", errand.EventCancel, errand.RelRequester, versioned("cancel")},
	{"confirm", errand.EventConfirm, errand.RelRequester, versioned("confirm")},
	{"dispute", errand.EventDispute, errand.RelRequester, withReason("dispute")},
	{"accept", errand.EventAccept, errand.RelEligibleCourier, versioned("accept")},
	{"withdraw", errand.EventWithdraw, errand.RelAssignedCourier, versioned("withdraw")},
	{"pickup", errand.EventPickUp, errand.RelAssignedCourier, versioned("pickup")},
	{"deliver", errand.EventDeliver, errand.RelAssignedCourier, versioned("deliver")},
	{"report issue", errand.EventReportIssue, errand.RelAssignedCourier, withReason("report-issue")},
}

type caller struct {
	name  string
	id    string
	token func(*world) string
}

var callers = []caller{
	{"requester", requesterID, func(w *world) string { return w.requester }},
	{"assigned courier", courierID, func(w *world) string { return w.courier }},
	{"other courier", rivalID, func(w *world) string { return w.rival }},
}

// TestEveryTransition calls every user action on an errand in every stored
// state, as the requester, the errand's courier and another courier. An
// action succeeds exactly when Diagram 1 has a rule for (state, event) and
// the caller has the rule's relationship; every other call is refused with
// nothing written (NFR6.1.1). The one exception is a confirm of a COMPLETED
// errand by its requester, answered 200 without writing (F3.10.2).
func TestEveryTransition(t *testing.T) {
	statuses := errand.Statuses()
	require.Len(t, statuses, 11)
	// One world, and a fresh errand for every call: records are per errand.
	w, store := newFakeWorld(t)
	tried := 0
	for _, status := range statuses {
		for _, a := range userActions {
			for _, c := range callers {
				tried++
				n := tried
				t.Run(fmt.Sprintf("%s/%s/%s", status, a.name, c.name), func(t *testing.T) {
					before := erranding(n, status)
					store.Put(before)
					recorder := a.send(t, w, c.token(w), before.ID, before.Version)
					checkTransition(t, w, before, a, c, recorder)
				})
			}
		}
	}
	assert.Equal(t, 11*len(userActions)*len(callers), tried)
}

func checkTransition(t *testing.T, w *world, before errand.Errand, a userAction, c caller, recorder *httptest.ResponseRecorder) {
	t.Helper()
	after := w.errand(t, before.ID)
	events := w.records.events(t, before.ID)
	outbox := w.records.outbox(t, before.ID)

	if allowed(before, a, c.id) {
		require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, recorder.Code, recorder.Body.String())
		assert.Equal(t, before.Version+1, after.Version)
		assert.Contains(t, ruleTargets(before.Status, a.event), after.Status)
		assert.Equal(t, []string{string(a.event)}, events)
		return
	}

	if a.event == errand.EventConfirm && before.Status == errand.StatusCompleted && c.id == before.RequesterID {
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	} else {
		require.Contains(t, []int{http.StatusForbidden, http.StatusConflict}, recorder.Code, recorder.Body.String())
		assert.Equal(t, refusal(before, a, c.id), errorCode(t, recorder), recorder.Body.String())
	}
	assert.Equal(t, before, after, "a refused call changes nothing")
	assert.Empty(t, events)
	for _, kind := range outbox {
		assert.True(t, strings.HasSuffix(kind, "_rejected"), "only a rejection event may be written, got %s", kind)
	}
	if recorder.Code == http.StatusForbidden {
		assert.Empty(t, outbox, "403 refusals are never announced")
	}
}

// allowed is the expectation from Diagram 1 alone: a rule exists, the
// caller has its relationship, and its guards hold (every deadline of
// erranding is in the future).
func allowed(e errand.Errand, a userAction, callerID string) bool {
	rules := errand.RulesFor(e.Status, a.event)
	if len(rules) == 0 {
		return false
	}
	switch rules[0].Actor {
	case errand.RelRequester:
		return callerID == e.RequesterID
	case errand.RelAssignedCourier:
		return e.CourierID != "" && callerID == e.CourierID
	case errand.RelEligibleCourier:
		return callerID != e.RequesterID
	default:
		return false
	}
}

// refusal is the error code a refused call gets. A route that admits only
// the requester or the assigned courier answers anyone else FORBIDDEN
// before looking at the state; otherwise the code is errand.Next's.
func refusal(e errand.Errand, a userAction, callerID string) apperror.Code {
	switch a.owner {
	case errand.RelRequester:
		if callerID != e.RequesterID {
			return apperror.Forbidden
		}
	case errand.RelAssignedCourier:
		if e.CourierID == "" || callerID != e.CourierID {
			return apperror.Forbidden
		}
	}
	actor := errand.Actor{Type: errand.ActorCourier, ID: callerID}
	if a.owner == errand.RelRequester {
		actor.Type = errand.ActorRequester
	}
	_, err := errand.Next(e, a.event, actor, errand.EventInput{Reason: "something went wrong"}, t0)
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		return ""
	}
	return appErr.Code
}

func ruleTargets(from errand.Status, ev errand.Event) []errand.Status {
	var out []errand.Status
	for _, r := range errand.RulesFor(from, ev) {
		if !slices.Contains(out, r.To) {
			out = append(out, r.To)
		}
	}
	return out
}

// erranding is errand n in status at version 5, requested by requesterID,
// with courierID as courier in the states that have one and every deadline
// in the future.
func erranding(n int, status errand.Status) errand.Errand {
	e := errand.Errand{
		ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", n),
		Status:      status,
		Version:     5,
		RequesterID: requesterID,
		Details: errand.Details{
			SupplierID:               supplierID,
			SupplierVersionID:        versionID,
			DeliveryBuildingID:       buildingID,
			DeliveryBuildingSnapshot: json.RawMessage(`{"id":"` + buildingID + `","name":"COM1","retired":false}`),
			DeliveryDetail:           "Level 2 lounge",
			Items:                    json.RawMessage(`[{"description":"iced latte","quantity":2}]`),
			Notes:                    "less ice",
			ContactName:              contactName,
			ContactMobile:            contactMobile,
			Reward:                   10,
			AcceptanceDeadline:       t0.Add(time.Hour),
		},
		CreatedAt: t0.Add(-20 * time.Minute),
		UpdatedAt: t0.Add(-10 * time.Minute),
	}
	switch status {
	case errand.StatusAssigned, errand.StatusPickedUp, errand.StatusDelivered,
		errand.StatusReviewRequired, errand.StatusCompleted, errand.StatusRefunded:
		e.CourierID = courierID
		e.PickupDeadline = ptr(t0.Add(2 * time.Hour))
		e.DeliveryDeadline = ptr(t0.Add(4 * time.Hour))
	}
	switch status {
	case errand.StatusDelivered, errand.StatusReviewRequired, errand.StatusCompleted, errand.StatusRefunded:
		e.DeliveredAt = ptr(t0.Add(-5 * time.Minute))
		e.VerificationDeadline = ptr(t0.Add(25 * time.Minute))
	}
	if status == errand.StatusReviewRequired {
		e.ReviewReason = errand.ReviewCourierIssue
	}
	return e
}

func ptr[T any](v T) *T { return &v }

// TestEveryRouteNeedsAUserTokenWithErrandsUse checks the permission and
// token kind on every A route before anything else (NFR3.3, NFR3.5.3).
func TestEveryRouteNeedsAUserTokenWithErrandsUse(t *testing.T) {
	w, store := newFakeWorld(t)
	e := erranding(1, errand.StatusOpen)
	store.Put(e)
	admin := tokenFor(t, w.fixture, requesterID, auth.ReviewErrands)
	service := w.fixture.Token(t, w.fixture.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")

	body := `{"expectedVersion":5}`
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/errands", `{}`},
		{http.MethodGet, "/errands/open", ""},
		{http.MethodGet, "/errands/" + e.ID, ""},
		{http.MethodPatch, "/errands/" + e.ID, body},
		{http.MethodGet, "/me/errands/requested", ""},
		{http.MethodGet, "/me/errands/delivering", ""},
		{http.MethodGet, "/me/errands/history", ""},
	}
	for _, action := range []string{"cancel", "confirm", "dispute", "accept", "withdraw", "pickup", "deliver", "report-issue"} {
		routes = append(routes, struct{ method, path, body string }{http.MethodPost, "/errands/" + e.ID + "/" + action, body})
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			requireError(t, w.do(t, r.method, r.path, "", r.body), http.StatusUnauthorized, apperror.Unauthenticated)
			requireError(t, w.do(t, r.method, r.path, admin, r.body), http.StatusForbidden, apperror.Forbidden)
			requireError(t, w.do(t, r.method, r.path, service, r.body), http.StatusForbidden, apperror.Forbidden)
		})
	}
	assert.Equal(t, e, w.errand(t, e.ID))
	assert.Empty(t, store.Outbox())
}
