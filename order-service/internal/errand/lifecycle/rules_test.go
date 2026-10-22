package lifecycle_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// The rules A8 names: self-acceptance, a stale version, the withdrawal
// restriction and repeated confirmation.

func TestSelfAcceptance(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		id := w.create(t)
		w.reserve(t, id)

		requireError(t, w.act(t, w.requester, id, "accept", 2), http.StatusForbidden, apperror.NotEligible)
		assert.Equal(t, errand.StatusOpen, w.errand(t, id).Status)
		assert.NotContains(t, w.records.outbox(t, id), errand.ErrandAcceptRejected.Kind(),
			"eligibility refusals are not announced")
		assert.Equal(t, "ASSIGNED", decode(t, w.act(t, w.courier, id, "accept", 2), http.StatusOK)["status"])
	})
}

func TestStaleVersion(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		id := w.create(t)
		w.reserve(t, id)
		seen := decode(t, w.get(t, w.courier, "/errands/"+id), http.StatusOK)
		assert.EqualValues(t, 2, seen["version"])

		decode(t, w.edit(t, w.requester, id, 2, map[string]any{"deliveryDetail": "Level 3 lab"}), http.StatusOK)

		t.Run("the courier accepting what they saw is told it changed", func(t *testing.T) {
			requireError(t, w.act(t, w.courier, id, "accept", 2), http.StatusConflict, apperror.ErrandChanged)
			requireError(t, w.act(t, w.courier, id, "accept", 2), http.StatusConflict, apperror.ErrandChanged)
			assert.Equal(t, 1, count(w.records.outbox(t, id), errand.ErrandAcceptRejected.Kind()),
				"notified once for the same refused request (F3.3.7)")
			assert.Equal(t, errand.StatusOpen, w.errand(t, id).Status)
		})

		t.Run("the requester acting on an old version is refused too", func(t *testing.T) {
			requireError(t, w.edit(t, w.requester, id, 2, map[string]any{"notes": "x"}), http.StatusConflict, apperror.ErrandChanged)
			requireError(t, w.act(t, w.requester, id, "cancel", 2), http.StatusConflict, apperror.ErrandChanged)
			assert.Equal(t, 1, count(w.records.outbox(t, id), errand.ErrandCancelRejected.Kind()))
		})

		t.Run("after reloading, the accept succeeds", func(t *testing.T) {
			accepted := decode(t, w.act(t, w.courier, id, "accept", 3), http.StatusOK)
			assert.Equal(t, "Level 3 lab", accepted["deliveryDetail"])
		})

		t.Run("courier actions check the version as well", func(t *testing.T) {
			requireError(t, w.act(t, w.courier, id, "pickup", 3), http.StatusConflict, apperror.ErrandChanged)
			assert.Equal(t, 1, count(w.records.outbox(t, id), errand.ErrandPickupRejected.Kind()))
			assert.Equal(t, errand.StatusAssigned, w.errand(t, id).Status)
		})
	})
}

func TestWithdrawalRestriction(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		rule := errand.DefaultSystemSettings().WithdrawalRestriction
		for range rule.Count {
			id := w.advance(t, errand.StatusAssigned)
			decode(t, w.act(t, w.courier, id, "withdraw", 3), http.StatusOK)
		}

		id := w.create(t)
		w.reserve(t, id)
		requireError(t, w.act(t, w.courier, id, "accept", 2), http.StatusForbidden, apperror.CourierRestricted)
		assert.NotContains(t, w.records.outbox(t, id), errand.ErrandAcceptRejected.Kind())

		t.Run("other couriers are not affected", func(t *testing.T) {
			other := w.create(t)
			w.reserve(t, other)
			decode(t, w.act(t, w.rival, other, "accept", 2), http.StatusOK)
		})

		t.Run("the restriction lapses once the withdrawals leave the window (F3.6.4)", func(t *testing.T) {
			w.clock.Advance(rule.Window - time.Second)
			requireError(t, w.act(t, w.courier, id, "accept", 2), http.StatusForbidden, apperror.CourierRestricted)
			w.clock.Advance(2 * time.Second)
			assert.Equal(t, "ASSIGNED", decode(t, w.act(t, w.courier, id, "accept", 2), http.StatusOK)["status"])
		})
	})
}

func TestRepeatedConfirmation(t *testing.T) {
	forEachStore(t, func(t *testing.T, w *world) {
		id := w.advance(t, errand.StatusDelivered)
		first := w.act(t, w.requester, id, "confirm", 5)
		assert.Equal(t, "COMPLETED", decode(t, first, http.StatusOK)["status"])

		for _, version := range []int{5, 6, 1} {
			again := w.act(t, w.requester, id, "confirm", version)
			assert.JSONEq(t, first.Body.String(), again.Body.String(),
				"a repeated confirm answers as the first, whatever version it sends (F3.10.2)")
		}
		assert.Equal(t, 1, count(w.records.events(t, id), "confirm"))
		assert.Equal(t, 1, count(w.records.outbox(t, id), errand.KindCreditTransfer), "credits move once")
		requireError(t, w.act(t, w.courier, id, "confirm", 6), http.StatusForbidden, apperror.Forbidden)
	})
}
