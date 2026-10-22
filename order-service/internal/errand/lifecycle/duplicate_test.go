package lifecycle_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

// beatenStore is a Store whose next Apply, once armed, is beaten by an
// identical request: it commits the transition, then reports the lost
// compare-and-swap the losing copy sees. It makes the duplicate races of
// race_test.go deterministic, without PostgreSQL.
type beatenStore struct {
	errand.Store
	armed atomic.Bool
}

func (s *beatenStore) Apply(ctx context.Context, t errand.Transition) (errand.Errand, error) {
	if !s.armed.CompareAndSwap(true, false) {
		return s.Store.Apply(ctx, t)
	}
	if _, err := s.Store.Apply(ctx, t); err != nil {
		return errand.Errand{}, err
	}
	return errand.Errand{}, &apperror.Error{Code: apperror.ErrandChanged, Message: "the errand has changed; reload it and try again"}
}

// TestLosingDuplicateIsAnsweredAsTheWinner covers every action with a retry
// check: a copy of the request that loses the compare-and-swap to its twin
// gets 200 and the errand as the twin left it, and no rejection event.
func TestLosingDuplicateIsAnsweredAsTheWinner(t *testing.T) {
	fake := testfakes.NewFakeStore()
	store := &beatenStore{Store: fake}
	w := newWorld(t, store, fakeRecords{fake})

	for _, tc := range []struct {
		name   string
		from   errand.Status
		status errand.Status
	}{
		{"accept", errand.StatusOpen, errand.StatusAssigned},
		{"cancel", errand.StatusOpen, errand.StatusCancelled},
		{"pickup", errand.StatusAssigned, errand.StatusPickedUp},
		{"deliver", errand.StatusPickedUp, errand.StatusDelivered},
		{"confirm", errand.StatusDelivered, errand.StatusCompleted},
		{"withdraw", errand.StatusAssigned, errand.StatusOpen},
		{"dispute", errand.StatusDelivered, errand.StatusReviewRequired},
		{"report-issue", errand.StatusPickedUp, errand.StatusReviewRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.forgetWithdrawals()
			id := w.advance(t, tc.from)
			version := w.errand(t, id).Version
			token := w.courier
			if tc.name == "cancel" || tc.name == "confirm" || tc.name == "dispute" {
				token = w.requester
			}

			store.armed.Store(true)
			var body map[string]any
			if tc.name == "dispute" || tc.name == "report-issue" {
				body = decode(t, w.reasoned(t, token, id, tc.name, version, "something went wrong"), http.StatusOK)
			} else {
				body = decode(t, w.act(t, token, id, tc.name, version), http.StatusOK)
			}
			require.False(t, store.armed.Load(), "the request reached Store.Apply")

			assert.Equal(t, string(tc.status), body["status"])
			assert.EqualValues(t, version+1, body["version"])
			for _, kind := range w.records.outbox(t, id) {
				assert.False(t, strings.HasSuffix(kind, "_rejected"), "a request that took effect is not announced as rejected: %s", kind)
			}
		})
	}

	t.Run("a lost compare-and-swap to a different change is still a 409", func(t *testing.T) {
		id := w.create(t)
		w.reserve(t, id)
		fake.FailNext("Apply", &apperror.Error{Code: apperror.ErrandChanged, Message: "changed"})
		requireError(t, w.act(t, w.courier, id, "accept", 2), http.StatusConflict, apperror.ErrandChanged)
		assert.Equal(t, errand.StatusOpen, w.errand(t, id).Status)
		assert.Equal(t, 1, count(w.records.outbox(t, id), errand.ErrandAcceptRejected.Kind()))
	})
}
