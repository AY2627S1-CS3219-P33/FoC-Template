package lifecycle_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// The happy paths of both roles, through the HTTP API only, except for the
// system events (credit results, deadline jobs) that A does not serve.

func TestDeliveryWorkflow(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		id := w.create(t)
		pending := decode(t, w.get(t, w.requester, "/errands/"+id), http.StatusOK)
		assert.Equal(t, "PENDING_RESERVATION", pending["status"])
		assert.Equal(t, "REQUESTER", pending["viewerRole"])
		requireError(t, w.get(t, w.courier, "/errands/"+id), http.StatusNotFound, apperror.ErrandNotFound)
		assert.Equal(t, []string{id}, listIDs(t, w.get(t, w.requester, "/me/errands/requested")),
			"an errand waiting for credit-service is active (F3.11)")

		w.reserve(t, id)
		open := listItems(t, w.get(t, w.courier, "/errands/open"))
		require.Len(t, open, 1)
		assert.Equal(t, id, open[0]["errandId"])
		assert.EqualValues(t, 2, open[0]["version"])
		for _, hidden := range []string{"requesterId", "notes", "contactName", "contactMobile"} {
			assert.NotContains(t, open[0], hidden, "NFR3.6.4")
		}
		assert.Equal(t, "PUBLIC", decode(t, w.get(t, w.courier, "/errands/"+id), http.StatusOK)["viewerRole"])

		accepted := decode(t, w.act(t, w.courier, id, "accept", 2), http.StatusOK)
		assert.Equal(t, "ASSIGNED", accepted["status"])
		assert.EqualValues(t, 3, accepted["version"])
		assert.Equal(t, "COURIER", accepted["viewerRole"])
		assert.Equal(t, contactName, accepted["contactName"])
		assert.NotNil(t, accepted["pickupDeadline"])
		assert.NotNil(t, accepted["deliveryDeadline"])

		requireError(t, w.get(t, w.rival, "/errands/"+id), http.StatusNotFound, apperror.ErrandNotFound)
		w.clock.Advance(5 * time.Second) // past the open-list cache
		assert.Empty(t, listIDs(t, w.get(t, w.rival, "/errands/open")))
		requested := listItems(t, w.get(t, w.requester, "/me/errands/requested"))
		require.Len(t, requested, 1)
		assert.Equal(t, "ASSIGNED", requested[0]["status"])
		assert.NotContains(t, requested[0], "courierId", "the requester sees nothing about the courier")
		assert.Equal(t, []string{id}, listIDs(t, w.get(t, w.courier, "/me/errands/delivering")))

		pickedUp := decode(t, w.act(t, w.courier, id, "pickup", 3), http.StatusOK)
		assert.Equal(t, "PICKED_UP", pickedUp["status"])
		delivered := decode(t, w.act(t, w.courier, id, "deliver", 4), http.StatusOK)
		assert.Equal(t, "DELIVERED", delivered["status"])
		assert.NotNil(t, delivered["verificationDeadline"])

		completed := decode(t, w.act(t, w.requester, id, "confirm", 5), http.StatusOK)
		assert.Equal(t, "COMPLETED", completed["status"])
		assert.EqualValues(t, 6, completed["version"])

		assert.Empty(t, listIDs(t, w.get(t, w.requester, "/me/errands/requested")))
		assert.Empty(t, listIDs(t, w.get(t, w.courier, "/me/errands/delivering")))
		mine := listItems(t, w.get(t, w.requester, "/me/errands/history"))
		require.Len(t, mine, 1)
		assert.Equal(t, "REQUESTER", mine[0]["viewerRole"])
		assert.Equal(t, contactName, mine[0]["contactName"])
		theirs := listItems(t, w.get(t, w.courier, "/me/errands/history"))
		require.Len(t, theirs, 1)
		assert.Equal(t, "COURIER", theirs[0]["viewerRole"])
		assert.NotContains(t, theirs[0], "contactName", "a finished errand hides the contact person")

		assert.Equal(t, []string{"submit", "reservation_confirmed", "accept", "pick_up", "deliver", "confirm"},
			w.records.events(t, id))
		assert.ElementsMatch(t, []string{
			errand.KindCreditReserve, errand.ErrandPlaced.Kind(),
			errand.ErrandOpened.Kind(),
			errand.ErrandAccepted.Kind(),
			errand.ErrandPickedUp.Kind(),
			errand.ErrandDelivered.Kind(),
			errand.KindCreditTransfer, errand.ErrandCompleted.Kind(),
		}, w.records.outbox(t, id), "credits are reserved once and transferred once")
	})
}

func TestWithdrawAndReassign(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		id := w.advance(t, errand.StatusAssigned)

		reopened := decode(t, w.act(t, w.courier, id, "withdraw", 3), http.StatusOK)
		assert.Equal(t, "OPEN", reopened["status"], "withdrawn before the acceptance deadline (F3.6.1)")
		assert.NotContains(t, reopened, "contactName")
		assert.Empty(t, listIDs(t, w.get(t, w.courier, "/me/errands/delivering")))
		assert.Equal(t, "PUBLIC", decode(t, w.get(t, w.courier, "/errands/"+id), http.StatusOK)["viewerRole"],
			"the former courier sees the reopened errand as anyone does")

		requireError(t, w.act(t, w.courier, id, "accept", 4), http.StatusForbidden, apperror.NotEligible)
		assert.Equal(t, "ASSIGNED", decode(t, w.act(t, w.rival, id, "accept", 4), http.StatusOK)["status"])
		assert.Equal(t, []string{id}, listIDs(t, w.get(t, w.rival, "/me/errands/delivering")))
		requireError(t, w.act(t, w.courier, id, "pickup", 5), http.StatusConflict, apperror.ErrandUnavailable)

		assert.Equal(t, []string{"submit", "reservation_confirmed", "accept", "withdraw", "accept"}, w.records.events(t, id))
		assert.NotContains(t, w.records.outbox(t, id), errand.KindCreditRelease, "the reservation stays held")
	})
}

func TestWithdrawAfterAcceptanceDeadlineExpires(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		id := w.advance(t, errand.StatusAssigned)
		w.clock.Advance(time.Hour) // the default acceptance window

		expired := decode(t, w.act(t, w.courier, id, "withdraw", 3), http.StatusOK)
		assert.Equal(t, "EXPIRED", expired["status"])
		outbox := w.records.outbox(t, id)
		assert.Equal(t, 1, count(outbox, errand.KindCreditRelease), "F4.3.3")
		assert.Equal(t, 1, count(outbox, errand.ErrandWithdrawn.Kind()))
		assert.Equal(t, 1, count(outbox, errand.ErrandExpired.Kind()))

		assert.Equal(t, []string{id}, listIDs(t, w.get(t, w.requester, "/me/errands/history")))
		assert.Empty(t, listIDs(t, w.get(t, w.courier, "/me/errands/history")),
			"a courier who withdrew is no longer part of the errand")
	})
}

func TestEditThenCancel(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		id := w.create(t)
		w.reserve(t, id)

		edited := decode(t, w.edit(t, w.requester, id, 2, map[string]any{"notes": "no sugar"}), http.StatusOK)
		assert.Equal(t, "no sugar", edited["notes"])
		assert.EqualValues(t, 3, edited["version"], "an edit that keeps the reward applies at once")

		pending := decode(t, w.edit(t, w.requester, id, 3, map[string]any{"reward": 15}), http.StatusAccepted)
		assert.EqualValues(t, 3, pending["version"], "a reward edit waits for credit-service (F4.4)")
		assert.EqualValues(t, 10, pending["reward"])
		assert.EqualValues(t, 15, pending["pendingEdit"].(map[string]any)["reward"])
		requireError(t, w.edit(t, w.requester, id, 3, map[string]any{"notes": "x"}), http.StatusConflict, apperror.EditPending)

		cancelled := decode(t, w.act(t, w.requester, id, "cancel", 3), http.StatusOK)
		assert.Equal(t, "CANCELLED", cancelled["status"])
		assert.NotContains(t, cancelled, "pendingEdit", "cancelling drops the pending edit")
		requireError(t, w.act(t, w.courier, id, "accept", 4), http.StatusConflict, apperror.ErrandUnavailable)

		outbox := w.records.outbox(t, id)
		assert.Equal(t, 1, count(outbox, errand.KindCreditAdjust))
		assert.Equal(t, 1, count(outbox, errand.KindCreditRelease), "the release returns the whole reservation (F4.5.1)")
		assert.Equal(t, 1, count(outbox, errand.ErrandAcceptRejected.Kind()), "the late courier is notified")
		assert.Equal(t, []string{id}, listIDs(t, w.get(t, w.requester, "/me/errands/history")))
	})
}

func TestProblemReports(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		t.Run("the requester disputes a delivery", func(t *testing.T) {
			id := w.advance(t, errand.StatusDelivered)
			disputed := decode(t, w.reasoned(t, w.requester, id, "dispute", 5, "nothing arrived"), http.StatusOK)
			assert.Equal(t, "REVIEW_REQUIRED", disputed["status"])
			assert.Equal(t, "REQUESTER_DISPUTE", disputed["reviewReason"])
			requireError(t, w.act(t, w.requester, id, "confirm", 6), http.StatusConflict, apperror.ErrandUnavailable)
			assert.NotContains(t, w.records.outbox(t, id), errand.KindCreditTransfer, "credits wait for the review")
		})

		t.Run("the courier reports an issue after pickup", func(t *testing.T) {
			id := w.advance(t, errand.StatusPickedUp)
			reported := decode(t, w.reasoned(t, w.courier, id, "report-issue", 4, "shop closed early"), http.StatusOK)
			assert.Equal(t, "REVIEW_REQUIRED", reported["status"])
			assert.Equal(t, "COURIER_ISSUE", reported["reviewReason"])
			assert.Equal(t, contactName, reported["contactName"], "the courier keeps the contact during the review")
			requireError(t, w.act(t, w.courier, id, "deliver", 5), http.StatusConflict, apperror.ErrandUnavailable)
		})
	})
}

func TestRejectedReservation(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		id := w.create(t)
		require.Equal(t, errand.StatusRejected, w.system(t, id, errand.EventReservationRefused).Status)

		assert.Equal(t, "REJECTED", decode(t, w.get(t, w.requester, "/errands/"+id), http.StatusOK)["status"])
		requireError(t, w.act(t, w.requester, id, "cancel", 2), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Empty(t, listIDs(t, w.get(t, w.requester, "/me/errands/requested")))
		assert.Equal(t, []string{id}, listIDs(t, w.get(t, w.requester, "/me/errands/history")))
		assert.NotContains(t, w.records.outbox(t, id), errand.KindCreditRelease, "nothing was reserved")
	})
}
