package errand_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

type pair struct {
	From  errand.Status
	Event errand.Event
	To    errand.Status
}

// diagram1 is the transition table in order-requirements.md, written out by
// hand so the data table in state.go is checked against it.
var diagram1 = []pair{
	{errand.StatusNone, errand.EventSubmit, errand.StatusPendingReservation},
	{errand.StatusPendingReservation, errand.EventReservationConfirmed, errand.StatusOpen},
	{errand.StatusPendingReservation, errand.EventReservationRefused, errand.StatusRejected},
	{errand.StatusPendingReservation, errand.EventReservationFailed, errand.StatusRejected},
	{errand.StatusOpen, errand.EventEdit, errand.StatusOpen},
	{errand.StatusOpen, errand.EventEditConfirmed, errand.StatusOpen},
	{errand.StatusOpen, errand.EventEditRefused, errand.StatusOpen},
	{errand.StatusOpen, errand.EventAccept, errand.StatusAssigned},
	{errand.StatusOpen, errand.EventCancel, errand.StatusCancelled},
	{errand.StatusOpen, errand.EventAcceptanceDeadlinePassed, errand.StatusExpired},
	{errand.StatusAssigned, errand.EventWithdraw, errand.StatusOpen},
	{errand.StatusAssigned, errand.EventWithdraw, errand.StatusExpired},
	{errand.StatusAssigned, errand.EventPickupDeadlinePassed, errand.StatusOpen},
	{errand.StatusAssigned, errand.EventPickupDeadlinePassed, errand.StatusExpired},
	{errand.StatusAssigned, errand.EventPickUp, errand.StatusPickedUp},
	{errand.StatusPickedUp, errand.EventDeliver, errand.StatusDelivered},
	{errand.StatusPickedUp, errand.EventReportIssue, errand.StatusReviewRequired},
	{errand.StatusPickedUp, errand.EventDeliveryDeadlinePassed, errand.StatusReviewRequired},
	{errand.StatusDelivered, errand.EventConfirm, errand.StatusCompleted},
	{errand.StatusDelivered, errand.EventVerificationLapsed, errand.StatusCompleted},
	{errand.StatusDelivered, errand.EventDispute, errand.StatusReviewRequired},
	{errand.StatusReviewRequired, errand.EventAdminComplete, errand.StatusCompleted},
	{errand.StatusReviewRequired, errand.EventAdminRefund, errand.StatusRefunded},
}

func TestTransitionTableMatchesDiagram1(t *testing.T) {
	var got []pair
	for _, r := range errand.Rules() {
		got = append(got, pair{r.From, r.Event, r.To})
	}
	require.ElementsMatch(t, diagram1, got)
}

func TestTransitionTableIsConsistent(t *testing.T) {
	relationship := map[errand.Event]errand.Relationship{}
	for _, r := range errand.Rules() {
		require.False(t, r.From.Terminal(), "%s leaves terminal %s", r.Event, r.From)
		require.NotEmpty(t, r.Emits, "%s from %s emits no event", r.Event, r.From)
		if rel, seen := relationship[r.Event]; seen {
			require.Equal(t, rel, r.Actor, "every rule for %s needs the same actor", r.Event)
		}
		relationship[r.Event] = r.Actor
		require.Equal(t, r.To == errand.StatusReviewRequired, r.Review != "", "review reason on %s", r.Event)
		switch r.To {
		case errand.StatusCancelled, errand.StatusExpired, errand.StatusRefunded:
			require.Equal(t, errand.CreditRelease, r.Credit, "F4.5: %s → %s releases", r.From, r.To)
		case errand.StatusCompleted:
			require.Equal(t, errand.CreditTransfer, r.Credit, "F4.2.2: %s → %s transfers", r.From, r.To)
		}
	}
	for _, ev := range errand.Events() {
		require.Contains(t, relationship, ev, "event %s is in no rule", ev)
	}
}

// guardHoldsInTest restates the guards against the fixture deadlines.
func guardHoldsInTest(g errand.Guard, now time.Time) bool {
	switch g {
	case errand.GuardBeforeAcceptanceDeadline:
		return now.Before(acceptanceDeadline)
	case errand.GuardAfterAcceptanceDeadline:
		return !now.Before(acceptanceDeadline)
	case errand.GuardAfterPickupDeadline:
		return !now.Before(pickupDeadline)
	case errand.GuardAfterDeliveryDeadline:
		return !now.Before(deliveryDeadline)
	case errand.GuardAfterVerificationDeadline:
		return !now.Before(verificationDeadline)
	}
	panic("unknown guard " + string(g))
}

// nowFor picks a time at which all of r's guards hold for the fixtures.
func nowFor(t *testing.T, r errand.Rule) time.Time {
	t.Helper()
	for _, now := range []time.Time{t0, t0.Add(5 * time.Hour), t0.Add(11 * time.Hour)} {
		ok := true
		for _, g := range r.Guards {
			ok = ok && guardHoldsInTest(g, now)
		}
		if ok {
			return now
		}
	}
	t.Fatalf("no fixture time satisfies %v", r.Guards)
	return time.Time{}
}

// TestEveryStateEventPair_NFR6_1_1 tries every event in every state,
// including the start pseudo-state. Pairs in the table succeed once for each
// guarded alternative; every other pair is rejected.
func TestEveryStateEventPair_NFR6_1_1(t *testing.T) {
	statuses := append([]errand.Status{errand.StatusNone}, errand.Statuses()...)
	allowed, rejected := 0, 0
	for _, s := range statuses {
		for _, ev := range errand.Events() {
			rules := errand.RulesFor(s, ev)
			name := fmt.Sprintf("%s/%s", displayName(s), ev)
			if len(rules) == 0 {
				rejected++
				t.Run(name+"/rejected", func(t *testing.T) {
					e := fixtureFor(s, ev)
					_, err := errand.Next(e, ev, actorFor(t, ev), validInput(e, t0), t0)
					code := codeOf(t, err)
					if s.Terminal() {
						require.Equal(t, apperror.ErrandUnavailable, code)
					} else {
						require.Contains(t, []apperror.Code{apperror.InvalidTransition, apperror.ErrandUnavailable}, code)
					}
				})
				continue
			}
			for _, r := range rules {
				allowed++
				t.Run(fmt.Sprintf("%s/allowed→%s", name, r.To), func(t *testing.T) {
					e := fixtureFor(s, ev)
					now := nowFor(t, r)
					tr, err := errand.Next(e, ev, actorFor(t, ev), validInput(e, now), now)
					require.NoError(t, err)
					require.Equal(t, s, tr.From)
					require.Equal(t, r.To, tr.To)
					require.Equal(t, ev, tr.Event)
					require.Equal(t, errandID, tr.ErrandID)
					require.Equal(t, now, tr.At)
					require.NotEmpty(t, tr.Outbox)

					after := tr.ApplyTo(e)
					require.Equal(t, r.To, after.Status)
					if after.Status == errand.StatusAssigned || after.Status == errand.StatusPickedUp ||
						after.Status == errand.StatusDelivered || after.Status == errand.StatusReviewRequired {
						require.NotEmpty(t, after.CourierID, "NFR6.1.1: an assigned errand has its courier")
					}
					if after.Status == errand.StatusOpen || after.Status == errand.StatusExpired && s == errand.StatusAssigned {
						require.Empty(t, after.CourierID)
					}
				})
			}
		}
	}
	require.Equal(t, len(diagram1), allowed)
	require.Equal(t, 12*len(errand.Events())-len(diagram1)+2, rejected,
		"every pair outside the table is rejected (two pairs have two guarded rules)")
}

func displayName(s errand.Status) string {
	if s == errand.StatusNone {
		return "START"
	}
	return string(s)
}

func TestNextRejectionCodes(t *testing.T) {
	cases := []struct {
		name   string
		status errand.Status
		event  errand.Event
		want   apperror.Code
	}{
		{"accept an errand someone else accepted (F3.3.2)", errand.StatusAssigned, errand.EventAccept, apperror.ErrandUnavailable},
		{"cancel an assigned errand", errand.StatusAssigned, errand.EventCancel, apperror.ErrandUnavailable},
		{"edit an assigned errand (F3.4.2)", errand.StatusAssigned, errand.EventEdit, apperror.ErrandUnavailable},
		{"accept a cancelled errand", errand.StatusCancelled, errand.EventAccept, apperror.ErrandUnavailable},
		{"confirm a completed errand (F3.10.2)", errand.StatusCompleted, errand.EventConfirm, apperror.ErrandUnavailable},
		{"deliver twice", errand.StatusDelivered, errand.EventDeliver, apperror.ErrandUnavailable},
		{"confirm before delivery (F3.10.2)", errand.StatusPickedUp, errand.EventConfirm, apperror.InvalidTransition},
		{"pick up an open errand", errand.StatusOpen, errand.EventPickUp, apperror.InvalidTransition},
		{"accept before the reservation", errand.StatusPendingReservation, errand.EventAccept, apperror.InvalidTransition},
		{"resolve a review that is not open", errand.StatusDelivered, errand.EventAdminRefund, apperror.InvalidTransition},
		{"accept an errand that does not exist", errand.StatusNone, errand.EventAccept, apperror.InvalidTransition},
		{"submit an existing errand", errand.StatusOpen, errand.EventSubmit, apperror.ErrandUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := errandIn(tc.status)
			_, err := errand.Next(e, tc.event, actorFor(t, tc.event), validInput(e, t0), t0)
			requireCode(t, tc.want, err)
		})
	}

	t.Run("unknown status", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		e.Status = "LOST"
		_, err := errand.Next(e, errand.EventCancel, asRequester, validInput(e, t0), t0)
		requireCode(t, apperror.InvalidArgument, err)
	})
	t.Run("unknown event", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		_, err := errand.Next(e, "teleport", asRequester, validInput(e, t0), t0)
		requireCode(t, apperror.InvalidTransition, err)
	})
}

func TestNextActorChecks_NFR3_3_2(t *testing.T) {
	stranger := errand.Actor{Type: errand.ActorRequester, ID: otherID}
	strangerCourier := errand.Actor{Type: errand.ActorCourier, ID: otherID}
	requesterAsCourier := errand.Actor{Type: errand.ActorCourier, ID: requesterID}
	courierAsRequester := errand.Actor{Type: errand.ActorRequester, ID: courierID}
	userAsAdmin := errand.Actor{Type: errand.ActorAdmin}
	userAsSystem := errand.Actor{Type: errand.ActorSystem, ID: otherID}

	cases := []struct {
		name   string
		status errand.Status
		event  errand.Event
		actor  errand.Actor
		input  func(*errand.EventInput)
		want   apperror.Code
	}{
		{"requester cannot accept own errand (F3.3.3)", errand.StatusOpen, errand.EventAccept, requesterAsCourier, nil, apperror.NotEligible},
		{"courier who withdrew cannot accept again (F3.6.3)", errand.StatusOpen, errand.EventAccept, asCourier,
			func(in *errand.EventInput) { in.PreviouslyWithdrew = true }, apperror.NotEligible},
		{"restricted courier cannot accept (F3.6.4)", errand.StatusOpen, errand.EventAccept, asCourier,
			func(in *errand.EventInput) { in.Restricted = true }, apperror.CourierRestricted},
		{"accept needs the courier role", errand.StatusOpen, errand.EventAccept, stranger, nil, apperror.Forbidden},
		{"accept needs an identity", errand.StatusOpen, errand.EventAccept, errand.Actor{Type: errand.ActorCourier}, nil, apperror.Forbidden},

		{"unrelated user cannot edit (F3.4)", errand.StatusOpen, errand.EventEdit, stranger, nil, apperror.Forbidden},
		{"unrelated user cannot cancel (F3.5)", errand.StatusOpen, errand.EventCancel, stranger, nil, apperror.Forbidden},
		{"courier cannot cancel", errand.StatusOpen, errand.EventCancel, asCourier, nil, apperror.Forbidden},
		{"unrelated user cannot confirm (F3.10)", errand.StatusDelivered, errand.EventConfirm, stranger, nil, apperror.Forbidden},
		{"assigned courier cannot confirm", errand.StatusDelivered, errand.EventConfirm, courierAsRequester, nil, apperror.Forbidden},
		{"unrelated user cannot dispute", errand.StatusDelivered, errand.EventDispute, stranger, nil, apperror.Forbidden},

		{"only assigned courier can pick up (F3.8.3)", errand.StatusAssigned, errand.EventPickUp, strangerCourier, nil, apperror.Forbidden},
		{"requester cannot pick up", errand.StatusAssigned, errand.EventPickUp, requesterAsCourier, nil, apperror.Forbidden},
		{"assigned courier must act as courier", errand.StatusAssigned, errand.EventPickUp, courierAsRequester, nil, apperror.Forbidden},
		{"only assigned courier can deliver (F3.8.3)", errand.StatusPickedUp, errand.EventDeliver, strangerCourier, nil, apperror.Forbidden},
		{"requester cannot deliver", errand.StatusPickedUp, errand.EventDeliver, asRequester, nil, apperror.Forbidden},
		{"only assigned courier can withdraw", errand.StatusAssigned, errand.EventWithdraw, strangerCourier, nil, apperror.Forbidden},
		{"only assigned courier can report an issue", errand.StatusPickedUp, errand.EventReportIssue, strangerCourier, nil, apperror.Forbidden},

		{"user cannot resolve a review (F1.6.2)", errand.StatusReviewRequired, errand.EventAdminComplete, asRequester, nil, apperror.Forbidden},
		{"admin needs an identity", errand.StatusReviewRequired, errand.EventAdminRefund, userAsAdmin, nil, apperror.Forbidden},
		{"user cannot expire an errand", errand.StatusOpen, errand.EventAcceptanceDeadlinePassed, asRequester, nil, apperror.Forbidden},
		{"user cannot confirm a reservation", errand.StatusPendingReservation, errand.EventReservationConfirmed, userAsSystem, nil, apperror.Forbidden},
		{"admin cannot cancel for the requester", errand.StatusOpen, errand.EventCancel, asAdmin, nil, apperror.Forbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := errandIn(tc.status)
			now := t0.Add(11 * time.Hour) // every After guard holds
			if tc.event == errand.EventAccept {
				now = t0
			}
			in := validInput(e, now)
			if tc.input != nil {
				tc.input(&in)
			}
			_, err := errand.Next(e, tc.event, tc.actor, in, now)
			requireCode(t, tc.want, err)
		})
	}

	t.Run("submit only by the errand's requester", func(t *testing.T) {
		e := errandIn(errand.StatusNone)
		_, err := errand.Next(e, errand.EventSubmit, stranger, validInput(e, t0), t0)
		requireCode(t, apperror.Forbidden, err)
	})
}

// Deadlines pass at the exact instant they fall (errand.Passed).
func TestNextDeadlineBoundaries(t *testing.T) {
	justBefore := func(d time.Time) time.Time { return d.Add(-time.Nanosecond) }

	t.Run("accept just before the acceptance deadline", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		now := justBefore(acceptanceDeadline)
		tr, err := errand.Next(e, errand.EventAccept, asCourier, validInput(e, now), now)
		require.NoError(t, err)
		require.Equal(t, errand.StatusAssigned, tr.To)
	})
	t.Run("accept at the acceptance deadline is unavailable (F3.3.4)", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		_, err := errand.Next(e, errand.EventAccept, asCourier, validInput(e, acceptanceDeadline), acceptanceDeadline)
		requireCode(t, apperror.ErrandUnavailable, err)
	})
	t.Run("expiry at the acceptance deadline", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		tr, err := errand.Next(e, errand.EventAcceptanceDeadlinePassed, asSystem, errand.EventInput{}, acceptanceDeadline)
		require.NoError(t, err)
		require.Equal(t, errand.StatusExpired, tr.To)
	})
	t.Run("expiry just before the acceptance deadline", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		_, err := errand.Next(e, errand.EventAcceptanceDeadlinePassed, asSystem, errand.EventInput{}, justBefore(acceptanceDeadline))
		requireCode(t, apperror.InvalidTransition, err)
	})
	t.Run("expiry after the deadline was extended", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		e.Details.AcceptanceDeadline = acceptanceDeadline.Add(time.Hour)
		_, err := errand.Next(e, errand.EventAcceptanceDeadlinePassed, asSystem, errand.EventInput{}, acceptanceDeadline)
		requireCode(t, apperror.InvalidTransition, err)
	})

	t.Run("withdraw just before the acceptance deadline reopens (F3.6.1)", func(t *testing.T) {
		e := errandIn(errand.StatusAssigned)
		tr, err := errand.Next(e, errand.EventWithdraw, asCourier, errand.EventInput{}, justBefore(acceptanceDeadline))
		require.NoError(t, err)
		require.Equal(t, errand.StatusOpen, tr.To)
	})
	t.Run("withdraw at the acceptance deadline expires (F3.6.1)", func(t *testing.T) {
		e := errandIn(errand.StatusAssigned)
		tr, err := errand.Next(e, errand.EventWithdraw, asCourier, errand.EventInput{}, acceptanceDeadline)
		require.NoError(t, err)
		require.Equal(t, errand.StatusExpired, tr.To)
	})

	t.Run("pickup deadline at the deadline", func(t *testing.T) {
		e := errandIn(errand.StatusAssigned)
		tr, err := errand.Next(e, errand.EventPickupDeadlinePassed, asSystem, errand.EventInput{}, pickupDeadline)
		require.NoError(t, err)
		require.Equal(t, errand.StatusOpen, tr.To)
	})
	t.Run("pickup deadline just before the deadline", func(t *testing.T) {
		e := errandIn(errand.StatusAssigned)
		_, err := errand.Next(e, errand.EventPickupDeadlinePassed, asSystem, errand.EventInput{}, justBefore(pickupDeadline))
		requireCode(t, apperror.InvalidTransition, err)
	})
	t.Run("pickup deadline at the same instant as the acceptance deadline expires (F3.8.4)", func(t *testing.T) {
		e := errandIn(errand.StatusAssigned)
		e.PickupDeadline = ptr(acceptanceDeadline)
		tr, err := errand.Next(e, errand.EventPickupDeadlinePassed, asSystem, errand.EventInput{}, acceptanceDeadline)
		require.NoError(t, err)
		require.Equal(t, errand.StatusExpired, tr.To)
	})

	t.Run("delivery deadline at the deadline", func(t *testing.T) {
		e := errandIn(errand.StatusPickedUp)
		tr, err := errand.Next(e, errand.EventDeliveryDeadlinePassed, asSystem, errand.EventInput{}, deliveryDeadline)
		require.NoError(t, err)
		require.Equal(t, errand.StatusReviewRequired, tr.To)
		require.Equal(t, errand.ReviewDeliveryOverdue, tr.ApplyTo(e).ReviewReason)
	})
	t.Run("delivery deadline just before the deadline", func(t *testing.T) {
		e := errandIn(errand.StatusPickedUp)
		_, err := errand.Next(e, errand.EventDeliveryDeadlinePassed, asSystem, errand.EventInput{}, justBefore(deliveryDeadline))
		requireCode(t, apperror.InvalidTransition, err)
	})
	t.Run("missing delivery deadline never passes", func(t *testing.T) {
		e := errandIn(errand.StatusPickedUp)
		e.DeliveryDeadline = nil
		_, err := errand.Next(e, errand.EventDeliveryDeadlinePassed, asSystem, errand.EventInput{}, t0.Add(100*time.Hour))
		requireCode(t, apperror.InvalidTransition, err)
	})

	t.Run("verification lapses at the deadline (F3.10.3)", func(t *testing.T) {
		e := errandIn(errand.StatusDelivered)
		tr, err := errand.Next(e, errand.EventVerificationLapsed, asSystem, errand.EventInput{}, verificationDeadline)
		require.NoError(t, err)
		require.Equal(t, errand.StatusCompleted, tr.To)
	})
	t.Run("verification just before the deadline", func(t *testing.T) {
		e := errandIn(errand.StatusDelivered)
		_, err := errand.Next(e, errand.EventVerificationLapsed, asSystem, errand.EventInput{}, justBefore(verificationDeadline))
		requireCode(t, apperror.InvalidTransition, err)
	})
}

func TestNextFieldChanges(t *testing.T) {
	settings := errand.DefaultDeadlineSettings()

	t.Run("submit creates version 1", func(t *testing.T) {
		e := errandIn(errand.StatusNone)
		tr, err := errand.Next(e, errand.EventSubmit, asRequester, errand.EventInput{}, t0)
		require.NoError(t, err)
		require.Equal(t, 0, tr.ExpectedVersion)
		require.Equal(t, 1, tr.NewVersion)
		after := tr.ApplyTo(e)
		require.Equal(t, errand.StatusPendingReservation, after.Status)
		require.Equal(t, t0, after.CreatedAt)
	})

	t.Run("accept assigns the courier and sets pickup and delivery deadlines (F3.8.4, F3.8.5)", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		tr, err := errand.Next(e, errand.EventAccept, asCourier, errand.EventInput{Deadlines: settings}, t0)
		require.NoError(t, err)
		require.Equal(t, baseVersion, tr.ExpectedVersion)
		require.Equal(t, baseVersion+1, tr.NewVersion)
		require.Equal(t, asCourier, tr.Actor)
		after := tr.ApplyTo(e)
		require.Equal(t, courierID, after.CourierID)
		require.Equal(t, t0.Add(2*time.Hour), *after.PickupDeadline)
		require.Equal(t, t0.Add(2*time.Hour), *after.DeliveryDeadline)
		require.Equal(t, baseVersion+1, after.Version)
		require.Equal(t, t0, after.UpdatedAt)
	})

	t.Run("accept needs valid deadline settings", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		_, err := errand.Next(e, errand.EventAccept, asCourier, errand.EventInput{}, t0)
		requireCode(t, apperror.ValidationFailed, err)
	})

	for _, ev := range []errand.Event{errand.EventWithdraw, errand.EventPickupDeadlinePassed} {
		t.Run(string(ev)+" clears the courier and its deadlines", func(t *testing.T) {
			e := errandIn(errand.StatusAssigned)
			now := t0.Add(5 * time.Hour)
			tr, err := errand.Next(e, ev, actorFor(t, ev), errand.EventInput{}, now)
			require.NoError(t, err)
			after := tr.ApplyTo(e)
			require.Equal(t, errand.StatusOpen, after.Status)
			require.Empty(t, after.CourierID)
			require.Nil(t, after.PickupDeadline)
			require.Nil(t, after.DeliveryDeadline)
		})
	}

	t.Run("deliver sets delivery time and verification deadline", func(t *testing.T) {
		e := errandIn(errand.StatusPickedUp)
		tr, err := errand.Next(e, errand.EventDeliver, asCourier, errand.EventInput{Deadlines: settings}, t0)
		require.NoError(t, err)
		after := tr.ApplyTo(e)
		require.Equal(t, t0, *after.DeliveredAt)
		require.Equal(t, t0.Add(30*time.Minute), *after.VerificationDeadline)
		require.Equal(t, courierID, after.CourierID)
	})

	reviews := []struct {
		status errand.Status
		event  errand.Event
		actor  errand.Actor
		want   errand.ReviewReason
	}{
		{errand.StatusPickedUp, errand.EventReportIssue, asCourier, errand.ReviewCourierIssue},
		{errand.StatusDelivered, errand.EventDispute, asRequester, errand.ReviewRequesterDispute},
	}
	for _, tc := range reviews {
		t.Run(string(tc.event)+" records the review reason and note", func(t *testing.T) {
			e := errandIn(tc.status)
			tr, err := errand.Next(e, tc.event, tc.actor, errand.EventInput{Reason: "item was sold out"}, t0)
			require.NoError(t, err)
			require.Equal(t, "item was sold out", tr.Reason)
			require.Equal(t, tc.want, tr.ApplyTo(e).ReviewReason)
		})
		t.Run(string(tc.event)+" needs a reason", func(t *testing.T) {
			e := errandIn(tc.status)
			_, err := errand.Next(e, tc.event, tc.actor, errand.EventInput{Reason: "  "}, t0)
			requireCode(t, apperror.ValidationFailed, err)
			require.Equal(t, []string{"reason"}, fieldsOf(t, err))
		})
	}

	for _, ev := range []errand.Event{errand.EventAdminComplete, errand.EventAdminRefund} {
		t.Run(string(ev)+" needs a resolution reason (F6.1.4)", func(t *testing.T) {
			e := errandIn(errand.StatusReviewRequired)
			_, err := errand.Next(e, ev, asAdmin, errand.EventInput{}, t0)
			requireCode(t, apperror.ValidationFailed, err)

			tr, err := errand.Next(e, ev, asAdmin, errand.EventInput{Reason: "photo shows delivery"}, t0)
			require.NoError(t, err)
			require.Equal(t, "photo shows delivery", tr.Reason)
			require.Equal(t, asAdmin, tr.Actor)
		})
	}

	t.Run("reservation outcomes default their reason", func(t *testing.T) {
		e := errandIn(errand.StatusPendingReservation)
		tr, err := errand.Next(e, errand.EventReservationRefused, asSystem, errand.EventInput{}, t0)
		require.NoError(t, err)
		require.Equal(t, errand.ReasonInsufficientCredits, tr.Reason)

		tr, err = errand.Next(e, errand.EventReservationFailed, asSystem, errand.EventInput{}, t0)
		require.NoError(t, err)
		require.Equal(t, errand.ReasonReservationFailed, tr.Reason)
	})
}

func TestNextValidatesSubmissionsAndEdits(t *testing.T) {
	t.Run("submit", func(t *testing.T) {
		e := errandIn(errand.StatusNone)
		e.Details.Reward = 0
		e.Details.Items = []byte(`[]`)
		e.Details.AcceptanceDeadline = t0.Add(-time.Minute)
		_, err := errand.Next(e, errand.EventSubmit, asRequester, errand.EventInput{}, t0)
		requireCode(t, apperror.ValidationFailed, err)
		require.ElementsMatch(t, []string{"reward", "items", "acceptanceDeadline"}, fieldsOf(t, err))
	})
	t.Run("submit needs an ID", func(t *testing.T) {
		e := errandIn(errand.StatusNone)
		e.ID = ""
		_, err := errand.Next(e, errand.EventSubmit, asRequester, errand.EventInput{}, t0)
		requireCode(t, apperror.InvalidArgument, err)
	})

	edits := []struct {
		name  string
		edit  func(*errand.Details)
		field string
	}{
		{"zero reward", func(d *errand.Details) { d.Reward = 0 }, "reward"},
		{"negative reward", func(d *errand.Details) { d.Reward = -5 }, "reward"},
		{"no items", func(d *errand.Details) { d.Items = []byte(`[]`) }, "items"},
		{"items not an array", func(d *errand.Details) { d.Items = []byte(`{"a":1}`) }, "items"},
		{"deadline in the past (F3.1.3)", func(d *errand.Details) { d.AcceptanceDeadline = t0.Add(-time.Second) }, "acceptanceDeadline"},
		{"deadline under 10 minutes", func(d *errand.Details) { d.AcceptanceDeadline = t0.Add(9 * time.Minute) }, "acceptanceDeadline"},
		{"deadline over 24 hours", func(d *errand.Details) { d.AcceptanceDeadline = t0.Add(25 * time.Hour) }, "acceptanceDeadline"},
	}
	for _, tc := range edits {
		t.Run("edit with "+tc.name, func(t *testing.T) {
			e := errandIn(errand.StatusOpen)
			edit := details(baseReward, t0.Add(time.Hour))
			tc.edit(&edit)
			_, err := errand.Next(e, errand.EventEdit, asRequester, errand.EventInput{Edit: &edit}, t0)
			requireCode(t, apperror.ValidationFailed, err)
			require.Equal(t, []string{tc.field}, fieldsOf(t, err))
		})
	}

	// The window applies only to a changed deadline (F3.1.3); an unchanged
	// one only has to be in the future.
	t.Run("edit keeps a deadline under 10 minutes away", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		now := acceptanceDeadline.Add(-5 * time.Minute)
		edit := e.Details
		edit.Notes = "no ice"
		tr, err := errand.Next(e, errand.EventEdit, asRequester, errand.EventInput{Edit: &edit}, now)
		require.NoError(t, err)
		require.Equal(t, &edit, tr.Changes.Details)
	})
	t.Run("edit keeps a deadline that has passed", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		edit := e.Details
		edit.Notes = "no ice"
		_, err := errand.Next(e, errand.EventEdit, asRequester, errand.EventInput{Edit: &edit}, acceptanceDeadline)
		requireCode(t, apperror.ValidationFailed, err)
		require.Equal(t, []string{"acceptanceDeadline"}, fieldsOf(t, err))
	})
	t.Run("edit moves the deadline under 10 minutes away", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		now := acceptanceDeadline.Add(-time.Hour)
		edit := e.Details
		edit.AcceptanceDeadline = now.Add(5 * time.Minute)
		_, err := errand.Next(e, errand.EventEdit, asRequester, errand.EventInput{Edit: &edit}, now)
		requireCode(t, apperror.ValidationFailed, err)
		require.Equal(t, []string{"acceptanceDeadline"}, fieldsOf(t, err))
	})

	t.Run("edit without details", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		_, err := errand.Next(e, errand.EventEdit, asRequester, errand.EventInput{}, t0)
		requireCode(t, apperror.InvalidArgument, err)
	})
}
