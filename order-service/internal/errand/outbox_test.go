package errand_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

func TestIdempotencyKeyFormat(t *testing.T) {
	require.Equal(t, "e-1:reserve", errand.IdempotencyKey("e-1", "reserve", 0))
	require.Equal(t, "e-1:withdrawn:7", errand.IdempotencyKey("e-1", "withdrawn", 7))
}

func TestEventNamesAndKinds(t *testing.T) {
	require.Equal(t, "event.errand.picked_up", errand.ErrandPickedUp.Kind())
	emitted := map[errand.EventName]bool{errand.ErrandEditRequested: true} // emitted by reward edits
	for _, r := range errand.Rules() {
		for _, name := range r.Emits {
			emitted[name] = true
		}
	}
	require.ElementsMatch(t, errand.TransitionEventNames(), keys(emitted))
}

func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestOutboxRowsPerTransition checks the rows, in order, that each
// transition emits, and their idempotency keys. The fixture errand is at
// version 3, so transitions write version 4.
func TestOutboxRowsPerTransition(t *testing.T) {
	afterAcceptance := t0.Add(11 * time.Hour)
	beforeAcceptance := t0.Add(5 * time.Hour)

	cases := []struct {
		name   string
		status errand.Status
		event  errand.Event
		now    time.Time
		want   []rowSummary
	}{
		{"submit reserves (F4.3)", errand.StatusNone, errand.EventSubmit, t0, []rowSummary{
			{"credit.reserve", "e-1:reserve"},
			{"event.errand.placed", "e-1:placed:1"},
		}},
		{"reservation confirmed opens", errand.StatusPendingReservation, errand.EventReservationConfirmed, t0, []rowSummary{
			{"event.errand.opened", "e-1:opened:4"},
		}},
		{"reservation refused rejects without credit", errand.StatusPendingReservation, errand.EventReservationRefused, t0, []rowSummary{
			{"event.errand.rejected", "e-1:rejected:4"},
		}},
		{"reservation failed compensates (NFR7.5.1)", errand.StatusPendingReservation, errand.EventReservationFailed, t0, []rowSummary{
			{"credit.release", "e-1:release"},
			{"event.errand.rejected", "e-1:rejected:4"},
		}},
		{"edit without a reward change", errand.StatusOpen, errand.EventEdit, t0, []rowSummary{
			{"event.errand.edited", "e-1:edited:4"},
		}},
		{"accept", errand.StatusOpen, errand.EventAccept, t0, []rowSummary{
			{"event.errand.accepted", "e-1:accepted:4"},
		}},
		{"cancel releases (F4.5.1)", errand.StatusOpen, errand.EventCancel, t0, []rowSummary{
			{"credit.release", "e-1:release"},
			{"event.errand.cancelled", "e-1:cancelled:4"},
		}},
		{"acceptance expiry releases (F4.5)", errand.StatusOpen, errand.EventAcceptanceDeadlinePassed, afterAcceptance, []rowSummary{
			{"credit.release", "e-1:release"},
			{"event.errand.expired", "e-1:expired:4"},
		}},
		{"withdraw before the acceptance deadline holds (F4.3.3)", errand.StatusAssigned, errand.EventWithdraw, beforeAcceptance, []rowSummary{
			{"event.errand.withdrawn", "e-1:withdrawn:4"},
		}},
		{"withdraw after the acceptance deadline releases and emits both", errand.StatusAssigned, errand.EventWithdraw, afterAcceptance, []rowSummary{
			{"credit.release", "e-1:release"},
			{"event.errand.withdrawn", "e-1:withdrawn:4"},
			{"event.errand.expired", "e-1:expired:4"},
		}},
		{"pickup deadline before the acceptance deadline holds", errand.StatusAssigned, errand.EventPickupDeadlinePassed, beforeAcceptance, []rowSummary{
			{"event.errand.withdrawn", "e-1:withdrawn:4"},
		}},
		{"pickup deadline after the acceptance deadline releases and emits both", errand.StatusAssigned, errand.EventPickupDeadlinePassed, afterAcceptance, []rowSummary{
			{"credit.release", "e-1:release"},
			{"event.errand.withdrawn", "e-1:withdrawn:4"},
			{"event.errand.expired", "e-1:expired:4"},
		}},
		{"pick up", errand.StatusAssigned, errand.EventPickUp, t0, []rowSummary{
			{"event.errand.picked_up", "e-1:picked_up:4"},
		}},
		{"deliver", errand.StatusPickedUp, errand.EventDeliver, t0, []rowSummary{
			{"event.errand.delivered", "e-1:delivered:4"},
		}},
		{"report issue holds", errand.StatusPickedUp, errand.EventReportIssue, t0, []rowSummary{
			{"event.errand.review_required", "e-1:review_required:4"},
		}},
		{"delivery deadline holds", errand.StatusPickedUp, errand.EventDeliveryDeadlinePassed, beforeAcceptance, []rowSummary{
			{"event.errand.review_required", "e-1:review_required:4"},
		}},
		{"confirm transfers (F4.2.2)", errand.StatusDelivered, errand.EventConfirm, t0, []rowSummary{
			{"credit.transfer", "e-1:transfer"},
			{"event.errand.completed", "e-1:completed:4"},
		}},
		{"verification lapse transfers (F3.10.3)", errand.StatusDelivered, errand.EventVerificationLapsed, beforeAcceptance, []rowSummary{
			{"credit.transfer", "e-1:transfer"},
			{"event.errand.completed", "e-1:completed:4"},
		}},
		{"dispute holds", errand.StatusDelivered, errand.EventDispute, t0, []rowSummary{
			{"event.errand.review_required", "e-1:review_required:4"},
		}},
		{"admin completion transfers (F6.1.3)", errand.StatusReviewRequired, errand.EventAdminComplete, t0, []rowSummary{
			{"credit.transfer", "e-1:transfer"},
			{"event.errand.completed", "e-1:completed:4"},
		}},
		{"admin refund releases (F6.1.3)", errand.StatusReviewRequired, errand.EventAdminRefund, t0, []rowSummary{
			{"credit.release", "e-1:release"},
			{"event.errand.refunded", "e-1:refunded:4"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := errandIn(tc.status)
			tr, err := errand.Next(e, tc.event, actorFor(t, tc.event), validInput(e, tc.now), tc.now)
			require.NoError(t, err)
			require.Equal(t, tc.want, summarize(tr.Outbox))
			for _, row := range tr.Outbox {
				require.Equal(t, errandID, row.ErrandID)
				_, err := json.Marshal(row.Payload)
				require.NoError(t, err)
			}
		})
	}
}

func TestCreditPayloads(t *testing.T) {
	t.Run("reserve carries the reward", func(t *testing.T) {
		e := errandIn(errand.StatusNone)
		tr, err := errand.Next(e, errand.EventSubmit, asRequester, errand.EventInput{}, t0)
		require.NoError(t, err)
		require.Equal(t, errand.CreditCommand{
			ErrandID: errandID, ErrandVersion: 1, RequesterID: requesterID, Amount: baseReward,
		}, creditPayload(t, tr.Outbox, errand.KindCreditReserve))
	})
	t.Run("release returns the whole reservation", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		tr, err := errand.Next(e, errand.EventCancel, asRequester, errand.EventInput{}, t0)
		require.NoError(t, err)
		require.Equal(t, errand.CreditCommand{
			ErrandID: errandID, ErrandVersion: baseVersion + 1, RequesterID: requesterID,
		}, creditPayload(t, tr.Outbox, errand.KindCreditRelease))
	})
	t.Run("transfer pays the assigned courier the reward", func(t *testing.T) {
		e := errandIn(errand.StatusDelivered)
		tr, err := errand.Next(e, errand.EventConfirm, asRequester, errand.EventInput{}, t0)
		require.NoError(t, err)
		require.Equal(t, errand.CreditCommand{
			ErrandID: errandID, ErrandVersion: baseVersion + 1, RequesterID: requesterID,
			CourierID: courierID, Amount: baseReward,
		}, creditPayload(t, tr.Outbox, errand.KindCreditTransfer))
	})
}

func TestEventPayloads(t *testing.T) {
	t.Run("forced withdrawal names the courier who was withdrawn (F5.1.3)", func(t *testing.T) {
		e := errandIn(errand.StatusAssigned)
		now := t0.Add(11 * time.Hour)
		tr, err := errand.Next(e, errand.EventPickupDeadlinePassed, asSystem, errand.EventInput{}, now)
		require.NoError(t, err)
		for _, name := range []errand.EventName{errand.ErrandWithdrawn, errand.ErrandExpired} {
			require.Equal(t, errand.EventData{
				ErrandID:      errandID,
				ErrandVersion: baseVersion + 1,
				Event:         errand.EventPickupDeadlinePassed,
				From:          errand.StatusAssigned,
				To:            errand.StatusExpired,
				ActorType:     errand.ActorSystem,
				RequesterID:   requesterID,
				CourierID:     courierID,
				Reward:        baseReward,
				At:            now,
			}, eventPayload(t, tr.Outbox, name))
		}
	})
	t.Run("accept names the new courier", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		tr, err := errand.Next(e, errand.EventAccept, asCourier, validInput(e, t0), t0)
		require.NoError(t, err)
		data := eventPayload(t, tr.Outbox, errand.ErrandAccepted)
		require.Equal(t, courierID, data.CourierID)
		require.Equal(t, courierID, data.ActorID)
		require.False(t, data.EditDropped)
	})
	t.Run("review carries reason and review reason", func(t *testing.T) {
		e := errandIn(errand.StatusPickedUp)
		tr, err := errand.Next(e, errand.EventReportIssue, asCourier, errand.EventInput{Reason: "shop closed"}, t0)
		require.NoError(t, err)
		data := eventPayload(t, tr.Outbox, errand.ErrandReviewRequired)
		require.Equal(t, "shop closed", data.Reason)
		require.Equal(t, errand.ReviewCourierIssue, data.ReviewReason)
	})
	t.Run("JSON field names", func(t *testing.T) {
		body, err := json.Marshal(errand.CreditCommand{ErrandID: "e", ErrandVersion: 2, RequesterID: "r", NewAmount: 5, Seq: 4})
		require.NoError(t, err)
		require.JSONEq(t, `{"errandId":"e","errandVersion":2,"requesterId":"r","newAmount":5,"seq":4}`, string(body))
	})
}

func TestPendingRewardEdit_F4_4(t *testing.T) {
	newReward := baseReward + 5

	t.Run("a reward change is stored as the pending edit without a version change", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		edit := details(newReward, t0.Add(2*time.Hour))
		edit.Notes = "no ice"
		tr, err := errand.Next(e, errand.EventEdit, asRequester, errand.EventInput{Edit: &edit}, t0)
		require.NoError(t, err)
		require.Equal(t, baseVersion, tr.ExpectedVersion)
		require.Equal(t, baseVersion, tr.NewVersion)
		require.Empty(t, tr.ExpectedPendingEditKey)
		require.Equal(t, []rowSummary{
			{"credit.adjust", "e-1:adjust:3"},
			{"event.errand.edit_requested", "e-1:edit_requested:3"},
		}, summarize(tr.Outbox))
		require.Equal(t, errand.CreditCommand{
			ErrandID: errandID, ErrandVersion: baseVersion, RequesterID: requesterID,
			NewAmount: newReward, Seq: errand.AdjustSeq(baseVersion),
		}, creditPayload(t, tr.Outbox, errand.KindCreditAdjust))
		event := eventPayload(t, tr.Outbox, errand.ErrandEditRequested)
		published := edit.WithoutContact()
		require.Equal(t, &published, event.PendingEdit, "the contact person never goes on the broker")
		raw, err := json.Marshal(event)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "contact")

		after := tr.ApplyTo(e)
		require.Equal(t, e.Details, after.Details, "current details are unchanged until credit confirms")
		require.Equal(t, baseVersion, after.Version)
		require.Equal(t, &errand.PendingEdit{Details: edit, Key: "e-1:adjust:3", At: t0}, after.PendingEdit)
	})

	for _, reward := range []int{baseReward, newReward + 1} {
		t.Run("a second edit while one is pending is refused", func(t *testing.T) {
			e := withPendingEdit(errandIn(errand.StatusOpen), newReward)
			edit := details(reward, t0.Add(time.Hour))
			_, err := errand.Next(e, errand.EventEdit, asRequester, errand.EventInput{Edit: &edit}, t0)
			requireCode(t, apperror.EditPending, err)
		})
	}

	t.Run("pending edit then accept reverts the adjustment (F3.4.2)", func(t *testing.T) {
		e := withPendingEdit(errandIn(errand.StatusOpen), newReward)
		tr, err := errand.Next(e, errand.EventAccept, asCourier, validInput(e, t0), t0)
		require.NoError(t, err)
		require.Equal(t, "e-1:adjust:3", tr.ExpectedPendingEditKey)
		require.Equal(t, []rowSummary{
			{"credit.adjust", "e-1:adjust-revert:4"},
			{"event.errand.accepted", "e-1:accepted:4"},
		}, summarize(tr.Outbox))
		revert := creditPayload(t, tr.Outbox, errand.KindCreditAdjust)
		require.Equal(t, baseReward, revert.NewAmount, "back to the current reward")
		require.Greater(t, revert.Seq, errand.AdjustSeq(baseVersion), "newer than the pending adjust")
		require.True(t, eventPayload(t, tr.Outbox, errand.ErrandAccepted).EditDropped)

		after := tr.ApplyTo(e)
		require.Nil(t, after.PendingEdit)
		require.Equal(t, baseReward, after.Details.Reward)
	})

	t.Run("pending edit then cancel only releases", func(t *testing.T) {
		e := withPendingEdit(errandIn(errand.StatusOpen), newReward)
		tr, err := errand.Next(e, errand.EventCancel, asRequester, errand.EventInput{}, t0)
		require.NoError(t, err)
		require.Equal(t, []rowSummary{
			{"credit.release", "e-1:release"},
			{"event.errand.cancelled", "e-1:cancelled:4"},
		}, summarize(tr.Outbox))
		require.Nil(t, tr.ApplyTo(e).PendingEdit)
	})

	t.Run("pending edit then expiry only releases", func(t *testing.T) {
		e := withPendingEdit(errandIn(errand.StatusOpen), newReward)
		now := acceptanceDeadline
		tr, err := errand.Next(e, errand.EventAcceptanceDeadlinePassed, asSystem, errand.EventInput{}, now)
		require.NoError(t, err)
		require.Equal(t, []rowSummary{
			{"credit.release", "e-1:release"},
			{"event.errand.expired", "e-1:expired:4"},
		}, summarize(tr.Outbox))
		require.Nil(t, tr.ApplyTo(e).PendingEdit)
	})

	t.Run("edit confirmed applies the whole edit", func(t *testing.T) {
		e := withPendingEdit(errandIn(errand.StatusOpen), newReward)
		tr, err := errand.Next(e, errand.EventEditConfirmed, asSystem, errand.EventInput{EditKey: "e-1:adjust:3"}, t0)
		require.NoError(t, err)
		require.Equal(t, baseVersion+1, tr.NewVersion)
		require.Equal(t, []rowSummary{{"event.errand.edited", "e-1:edited:4"}}, summarize(tr.Outbox))
		after := tr.ApplyTo(e)
		require.Equal(t, e.PendingEdit.Details, after.Details)
		require.Nil(t, after.PendingEdit)
	})

	t.Run("edit refused by credit-service keeps the original details (F4.4.3)", func(t *testing.T) {
		e := withPendingEdit(errandIn(errand.StatusOpen), newReward)
		tr, err := errand.Next(e, errand.EventEditRefused, asSystem, errand.EventInput{EditKey: "e-1:adjust:3"}, t0)
		require.NoError(t, err)
		require.Equal(t, baseVersion+1, tr.NewVersion, "so the next edit's adjust gets a newer seq")
		require.Equal(t, errand.ReasonInsufficientCredits, tr.Reason)
		require.Equal(t, []rowSummary{{"event.errand.edit_rejected", "e-1:edit_rejected:4"}}, summarize(tr.Outbox))
		after := tr.ApplyTo(e)
		require.Equal(t, e.Details, after.Details)
		require.Nil(t, after.PendingEdit)
	})

	t.Run("timed-out edit refusal reverts the adjustment", func(t *testing.T) {
		e := withPendingEdit(errandIn(errand.StatusOpen), newReward)
		in := errand.EventInput{EditKey: "e-1:adjust:3", EditTimedOut: true}
		tr, err := errand.Next(e, errand.EventEditRefused, asSystem, in, t0)
		require.NoError(t, err)
		require.Equal(t, errand.ReasonEditFailed, tr.Reason)
		require.Equal(t, []rowSummary{
			{"credit.adjust", "e-1:adjust-revert:4"},
			{"event.errand.edit_rejected", "e-1:edit_rejected:4"},
		}, summarize(tr.Outbox))
		require.Equal(t, baseReward, creditPayload(t, tr.Outbox, errand.KindCreditAdjust).NewAmount)
	})

	t.Run("edit outcome for another edit is stale", func(t *testing.T) {
		e := withPendingEdit(errandIn(errand.StatusOpen), newReward)
		for _, ev := range []errand.Event{errand.EventEditConfirmed, errand.EventEditRefused} {
			_, err := errand.Next(e, ev, asSystem, errand.EventInput{EditKey: "e-1:adjust:1"}, t0)
			requireCode(t, apperror.ErrandChanged, err)
		}
	})
	t.Run("edit outcome with no pending edit is stale", func(t *testing.T) {
		e := errandIn(errand.StatusOpen)
		_, err := errand.Next(e, errand.EventEditConfirmed, asSystem, errand.EventInput{EditKey: "e-1:adjust:3"}, t0)
		requireCode(t, apperror.ErrandChanged, err)
	})
	t.Run("edit outcome after the errand was accepted is unavailable", func(t *testing.T) {
		e := errandIn(errand.StatusAssigned)
		_, err := errand.Next(e, errand.EventEditConfirmed, asSystem, errand.EventInput{EditKey: "e-1:adjust:3"}, t0)
		requireCode(t, apperror.ErrandUnavailable, err)
	})
}

// TestLifecycleKeysAndSeqs drives one errand through every reward-edit path
// and checks that no idempotency key repeats and that adjust seqs strictly
// increase, which credit-service relies on to drop stale adjustments.
func TestLifecycleKeysAndSeqs(t *testing.T) {
	e := errandIn(errand.StatusNone)
	now := t0
	seen := map[string]bool{}
	var seqs []int
	settings := errand.DefaultDeadlineSettings()

	step := func(ev errand.Event, actor errand.Actor, in errand.EventInput) {
		t.Helper()
		in.Deadlines = settings
		tr, err := errand.Next(e, ev, actor, in, now)
		require.NoError(t, err, "%s from %s", ev, e.Status)
		for _, row := range tr.Outbox {
			require.False(t, seen[row.IdempotencyKey], "key %s repeats", row.IdempotencyKey)
			seen[row.IdempotencyKey] = true
			if row.Kind == errand.KindCreditAdjust {
				seqs = append(seqs, row.Payload.(errand.CreditCommand).Seq)
			}
		}
		e = tr.ApplyTo(e)
		now = now.Add(time.Minute)
	}
	editReward := func(reward int) errand.EventInput {
		return errand.EventInput{Edit: ptr(details(reward, now.Add(time.Hour)))}
	}
	pendingKey := func() errand.EventInput { return errand.EventInput{EditKey: e.PendingEdit.Key} }

	step(errand.EventSubmit, asRequester, errand.EventInput{})
	step(errand.EventReservationConfirmed, asSystem, errand.EventInput{})
	step(errand.EventEdit, asRequester, editReward(12))
	in := pendingKey()
	in.EditTimedOut = true
	step(errand.EventEditRefused, asSystem, in) // revert
	step(errand.EventEdit, asRequester, editReward(13))
	step(errand.EventEditRefused, asSystem, pendingKey())
	step(errand.EventEdit, asRequester, editReward(14))
	step(errand.EventAccept, asCourier, errand.EventInput{}) // revert
	step(errand.EventWithdraw, asCourier, errand.EventInput{})
	step(errand.EventEdit, asRequester, editReward(15))
	step(errand.EventEditConfirmed, asSystem, pendingKey())
	step(errand.EventEdit, asRequester, editReward(15)) // immediate
	step(errand.EventAccept, asCourier, errand.EventInput{})
	step(errand.EventPickUp, asCourier, errand.EventInput{})
	step(errand.EventDeliver, asCourier, errand.EventInput{})
	step(errand.EventConfirm, asRequester, errand.EventInput{})

	require.Equal(t, errand.StatusCompleted, e.Status)
	require.Equal(t, 15, e.Details.Reward)
	require.Len(t, seqs, 6)
	for i := 1; i < len(seqs); i++ {
		require.Greater(t, seqs[i], seqs[i-1], "adjust seqs %v", seqs)
	}
}
