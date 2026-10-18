package errand_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

const (
	errandID    = "e-1"
	requesterID = "requester-1"
	courierID   = "courier-1"
	otherID     = "user-9"
	adminID     = "admin-1"
	baseVersion = 3
	baseReward  = 10
)

// t0 is "now" in most tests. The fixture deadlines are chosen so that t0,
// t0+5h and t0+11h each satisfy a different guard combination:
// verification t0+30m < pickup = delivery t0+2h < acceptance t0+10h.
var (
	t0                   = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	acceptanceDeadline   = t0.Add(10 * time.Hour)
	pickupDeadline       = t0.Add(2 * time.Hour)
	deliveryDeadline     = t0.Add(2 * time.Hour)
	verificationDeadline = t0.Add(30 * time.Minute)
)

func details(reward int, deadline time.Time) errand.Details {
	return errand.Details{
		SupplierID:               "supplier-1",
		SupplierVersionID:        "supplier-version-1",
		DeliveryBuildingID:       "building-1",
		DeliveryBuildingSnapshot: json.RawMessage(`{"name":"COM1"}`),
		DeliveryDetail:           "Level 2 lounge",
		Items:                    json.RawMessage(`[{"description":"iced latte"}]`),
		Notes:                    "less sugar",
		ContactName:              "Alex Tan",
		ContactMobile:            "+65 9123 4567",
		Reward:                   reward,
		AcceptanceDeadline:       deadline,
	}
}

func ptr[T any](v T) *T { return &v }

// errandIn returns a consistent errand in status s, as the store would hold
// it. StatusNone gives a new, unsaved errand for Submit.
func errandIn(s errand.Status) errand.Errand {
	e := errand.Errand{
		ID:                   errandID,
		Status:               s,
		Version:              baseVersion,
		RequesterID:          requesterID,
		Details:              details(baseReward, acceptanceDeadline),
		CreateIdempotencyKey: "create-1",
		CreatedAt:            t0.Add(-time.Hour),
		UpdatedAt:            t0.Add(-time.Hour),
	}
	switch s {
	case errand.StatusNone:
		e.Version = 0
		e.CreatedAt, e.UpdatedAt = time.Time{}, time.Time{}
	case errand.StatusAssigned, errand.StatusPickedUp, errand.StatusDelivered,
		errand.StatusReviewRequired, errand.StatusCompleted, errand.StatusRefunded:
		e.CourierID = courierID
		e.PickupDeadline = ptr(pickupDeadline)
		e.DeliveryDeadline = ptr(deliveryDeadline)
	}
	switch s {
	case errand.StatusDelivered, errand.StatusCompleted:
		e.DeliveredAt = ptr(t0.Add(-time.Minute))
		e.VerificationDeadline = ptr(verificationDeadline)
	case errand.StatusReviewRequired, errand.StatusRefunded:
		e.ReviewReason = errand.ReviewCourierIssue
	}
	return e
}

// withPendingEdit adds a pending reward edit requested at the errand's
// current version.
func withPendingEdit(e errand.Errand, reward int) errand.Errand {
	e.PendingEdit = &errand.PendingEdit{
		Details: details(reward, acceptanceDeadline),
		Key:     errand.IdempotencyKey(e.ID, "adjust", e.Version),
		At:      t0.Add(-time.Minute),
	}
	return e
}

var (
	asRequester = errand.Actor{Type: errand.ActorRequester, ID: requesterID}
	asCourier   = errand.Actor{Type: errand.ActorCourier, ID: courierID}
	asAdmin     = errand.Actor{Type: errand.ActorAdmin, ID: adminID}
	asSystem    = errand.SystemActor()
)

// actorFor returns an actor with the right relationship for ev.
func actorFor(t *testing.T, ev errand.Event) errand.Actor {
	t.Helper()
	switch relationshipOf(t, ev) {
	case errand.RelRequester:
		return asRequester
	case errand.RelAssignedCourier, errand.RelEligibleCourier:
		return asCourier
	case errand.RelAdmin:
		return asAdmin
	case errand.RelSystem:
		return asSystem
	}
	t.Fatalf("no actor for %s", ev)
	return errand.Actor{}
}

func relationshipOf(t *testing.T, ev errand.Event) errand.Relationship {
	t.Helper()
	for _, r := range errand.Rules() {
		if r.Event == ev {
			return r.Actor
		}
	}
	t.Fatalf("event %s is in no rule", ev)
	return ""
}

// validInput returns input that passes every event-input check at now.
func validInput(e errand.Errand, now time.Time) errand.EventInput {
	in := errand.EventInput{
		Edit:      ptr(details(e.Details.Reward, now.Add(time.Hour))),
		Reason:    "the reason",
		Deadlines: errand.DefaultDeadlineSettings(),
	}
	if e.PendingEdit != nil {
		in.EditKey = e.PendingEdit.Key
	}
	return in
}

// fixtureFor is errandIn plus a pending edit for the edit-outcome events,
// which need one.
func fixtureFor(s errand.Status, ev errand.Event) errand.Errand {
	e := errandIn(s)
	if ev == errand.EventEditConfirmed || ev == errand.EventEditRefused {
		e = withPendingEdit(e, baseReward+5)
	}
	return e
}

func codeOf(t *testing.T, err error) apperror.Code {
	t.Helper()
	require.Error(t, err)
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr), "want *apperror.Error, got %T: %v", err, err)
	return appErr.Code
}

func requireCode(t *testing.T, want apperror.Code, err error) {
	t.Helper()
	require.Equal(t, want, codeOf(t, err), "error: %v", err)
}

func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr))
	var names []string
	for _, f := range appErr.Fields {
		names = append(names, f.Field)
	}
	return names
}

type rowSummary struct {
	Kind string
	Key  string
}

func summarize(rows []errand.OutboxRow) []rowSummary {
	out := make([]rowSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowSummary{Kind: r.Kind, Key: r.IdempotencyKey})
	}
	return out
}

func creditPayload(t *testing.T, rows []errand.OutboxRow, kind string) errand.CreditCommand {
	t.Helper()
	for _, r := range rows {
		if r.Kind == kind {
			cmd, ok := r.Payload.(errand.CreditCommand)
			require.True(t, ok, "payload of %s is %T", kind, r.Payload)
			return cmd
		}
	}
	t.Fatalf("no %s row in %v", kind, summarize(rows))
	return errand.CreditCommand{}
}

func eventPayload(t *testing.T, rows []errand.OutboxRow, name errand.EventName) errand.EventData {
	t.Helper()
	for _, r := range rows {
		if r.Kind == name.Kind() {
			data, ok := r.Payload.(errand.EventData)
			require.True(t, ok, "payload of %s is %T", name, r.Payload)
			return data
		}
	}
	t.Fatalf("no %s row in %v", name, summarize(rows))
	return errand.EventData{}
}
