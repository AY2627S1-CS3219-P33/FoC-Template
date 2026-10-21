package lifecycle_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Races between HTTP requests, and between requests and the deadline jobs,
// against real PostgreSQL: the version compare-and-swap in Store.Apply and
// FOR UPDATE SKIP LOCKED in Store.ClaimDue must leave exactly one winner,
// and every loser a 409 with nothing written but its rejection event
// (F3.3.5, F3.3.7, NFR6.1.2). Each race is run several times on fresh
// errands so that different interleavings come up.

const rounds = 8

// concurrently starts fns together and waits for all of them. fns must not
// call t.FailNow (require), as they do not run on the test goroutine.
func concurrently(fns ...func()) {
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(len(fns))
	done.Add(len(fns))
	for _, fn := range fns {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			fn()
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
}

// lost asserts that a losing request was refused because the errand moved.
func lost(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	assert.Contains(t, []apperror.Code{apperror.ErrandChanged, apperror.ErrandUnavailable}, errorCode(t, recorder))
}

func TestRaceManyCouriersAccept(t *testing.T) {
	w := newPostgresWorld(t)
	const couriers = 8
	tokens := make([]string, couriers)
	for i := range tokens {
		tokens[i] = tokenFor(t, w.fixture, fmt.Sprintf("auth0|racer-%d", i), auth.UseErrands)
	}

	for round := range rounds {
		id := w.create(t)
		w.reserve(t, id)
		responses := make([]*httptest.ResponseRecorder, couriers)
		fns := make([]func(), couriers)
		for i := range fns {
			fns[i] = func() { responses[i] = w.act(t, tokens[i], id, "accept", 2) }
		}
		concurrently(fns...)

		winner := -1
		for i, r := range responses {
			if r.Code == http.StatusOK {
				require.Equal(t, -1, winner, "round %d: two accepts succeeded", round)
				winner = i
				continue
			}
			lost(t, r)
		}
		require.NotEqual(t, -1, winner, "round %d: no accept succeeded", round)

		e := w.errand(t, id)
		assert.Equal(t, errand.StatusAssigned, e.Status)
		assert.Equal(t, fmt.Sprintf("auth0|racer-%d", winner), e.CourierID)
		assert.Equal(t, 3, e.Version)
		assert.Equal(t, 1, count(w.records.events(t, id), "accept"))
		outbox := w.records.outbox(t, id)
		assert.Equal(t, 1, count(outbox, errand.ErrandAccepted.Kind()))
		assert.Equal(t, couriers-1, count(outbox, errand.ErrandAcceptRejected.Kind()), "every losing courier is notified")
	}
}

func TestRaceAcceptVersusCancel(t *testing.T) {
	w := newPostgresWorld(t)
	wins := map[string]int{}
	for range rounds {
		id := w.create(t)
		w.reserve(t, id)
		var accepted, cancelled *httptest.ResponseRecorder
		concurrently(
			func() { accepted = w.act(t, w.courier, id, "accept", 2) },
			func() { cancelled = w.act(t, w.requester, id, "cancel", 2) },
		)

		e := w.errand(t, id)
		outbox := w.records.outbox(t, id)
		switch {
		case accepted.Code == http.StatusOK:
			wins["accept"]++
			lost(t, cancelled)
			assert.Equal(t, errand.StatusAssigned, e.Status)
			assert.Equal(t, 1, count(outbox, errand.ErrandCancelRejected.Kind()), "the requester is told the cancel failed")
			assert.Zero(t, count(outbox, errand.KindCreditRelease))
		case cancelled.Code == http.StatusOK:
			wins["cancel"]++
			lost(t, accepted)
			assert.Equal(t, errand.StatusCancelled, e.Status)
			assert.Equal(t, 1, count(outbox, errand.ErrandAcceptRejected.Kind()))
			assert.Equal(t, 1, count(outbox, errand.KindCreditRelease))
		default:
			t.Fatalf("neither won: accept %d %s, cancel %d %s",
				accepted.Code, accepted.Body.String(), cancelled.Code, cancelled.Body.String())
		}
		assert.Equal(t, 3, e.Version)
		assert.Len(t, w.records.events(t, id), 3, "submit, reservation, and one winner")
	}
	t.Logf("wins: %v", wins)
}

func TestRaceEditVersusAccept(t *testing.T) {
	w := newPostgresWorld(t)
	wins := map[string]int{}
	for range rounds {
		id := w.create(t)
		w.reserve(t, id)
		body := mustJSON(t, map[string]any{"expectedVersion": 2, "deliveryDetail": "Level 3 lab"})
		var edited, accepted *httptest.ResponseRecorder
		concurrently(
			func() { edited = w.do(t, http.MethodPatch, "/errands/"+id, w.requester, body) },
			func() { accepted = w.act(t, w.courier, id, "accept", 2) },
		)

		e := w.errand(t, id)
		switch {
		case edited.Code == http.StatusOK:
			wins["edit"]++
			lost(t, accepted)
			assert.Equal(t, errand.StatusOpen, e.Status)
			assert.Equal(t, "Level 3 lab", e.Details.DeliveryDetail)
		case accepted.Code == http.StatusOK:
			wins["accept"]++
			lost(t, edited)
			assert.Equal(t, errand.StatusAssigned, e.Status)
			assert.Equal(t, "Level 2 lounge", e.Details.DeliveryDetail, "the courier gets the details they accepted")
		default:
			t.Fatalf("neither won: edit %d %s, accept %d %s",
				edited.Code, edited.Body.String(), accepted.Code, accepted.Body.String())
		}
		assert.Equal(t, 3, e.Version)
	}
	t.Logf("wins: %v", wins)
}

func TestRaceWithdrawVersusPickup(t *testing.T) {
	w := newPostgresWorld(t)
	wins := map[string]int{}
	for range rounds {
		w.forgetWithdrawals()
		id := w.advance(t, errand.StatusAssigned)
		var withdrawn, pickedUp *httptest.ResponseRecorder
		concurrently(
			func() { withdrawn = w.act(t, w.courier, id, "withdraw", 3) },
			func() { pickedUp = w.act(t, w.courier, id, "pickup", 3) },
		)

		e := w.errand(t, id)
		switch {
		case withdrawn.Code == http.StatusOK:
			wins["withdraw"]++
			lost(t, pickedUp)
			assert.Equal(t, errand.StatusOpen, e.Status)
			assert.Empty(t, e.CourierID)
		case pickedUp.Code == http.StatusOK:
			wins["pickup"]++
			lost(t, withdrawn)
			assert.Equal(t, errand.StatusPickedUp, e.Status)
		default:
			t.Fatalf("neither won: withdraw %d %s, pickup %d %s",
				withdrawn.Code, withdrawn.Body.String(), pickedUp.Code, pickedUp.Body.String())
		}
		assert.Equal(t, 4, e.Version)
	}
	t.Logf("wins: %v", wins)
}

// TestRacePickupVersusPickupDeadline races a courier's pickup against the
// pickup-deadline job, which withdraws the courier. The job runs with a
// clock past the pickup deadline (and so past the acceptance deadline: the
// errand expires); the pickup itself has no deadline guard.
func TestRacePickupVersusPickupDeadline(t *testing.T) {
	w := newPostgresWorld(t)
	wins := map[string]int{}
	for range rounds {
		w.forgetWithdrawals()
		id := w.advance(t, errand.StatusAssigned)
		jobNow := (*w.errand(t, id).PickupDeadline).Add(time.Second)
		var pickedUp *httptest.ResponseRecorder
		var applied int
		var jobErr error
		concurrently(
			func() { pickedUp = w.act(t, w.courier, id, "pickup", 3) },
			func() { applied, jobErr = pickupDeadlineJob(w, jobNow) },
		)
		require.NoError(t, jobErr)

		e := w.errand(t, id)
		outbox := w.records.outbox(t, id)
		switch {
		case pickedUp.Code == http.StatusOK:
			wins["pickup"]++
			assert.Zero(t, applied, "the job skipped or no longer matched the picked-up errand")
			assert.Equal(t, errand.StatusPickedUp, e.Status)
		case applied == 1:
			wins["job"]++
			lost(t, pickedUp)
			assert.Equal(t, errand.StatusExpired, e.Status)
			assert.Equal(t, 1, count(outbox, errand.ErrandPickupRejected.Kind()), "the late courier is notified")
			assert.Equal(t, 1, count(outbox, errand.KindCreditRelease))
			history, err := w.store.CourierHistory(context.Background(), id, courierID, time.Time{})
			require.NoError(t, err)
			assert.True(t, history.WithdrewFromErrand, "the forced withdrawal counts against the courier")
		default:
			t.Fatalf("neither won: pickup %d %s, job applied %d", pickedUp.Code, pickedUp.Body.String(), applied)
		}
		assert.Equal(t, 4, e.Version)
		// A forced withdrawal is recorded at the job's time, so the next
		// round's forgetWithdrawals must start from there.
		w.clock.Set(jobNow)
	}
	t.Logf("wins: %v", wins)
}

// pickupDeadlineJob is one batch of B's pickup-deadline job, safe to call
// off the test goroutine.
func pickupDeadlineJob(w *world, now time.Time) (int, error) {
	settings, err := w.settings.Get(context.Background())
	if err != nil {
		return 0, err
	}
	return w.store.ClaimDue(context.Background(), errand.PickupDeadline, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
		tr, err := errand.Next(e, errand.EventPickupDeadlinePassed, errand.SystemActor(), errand.EventInput{Deadlines: settings.Deadlines}, now)
		return tr, err == nil, nil
	})
}

// TestRaceRepeatedConfirms sends the same confirm several times at once, as
// a double-tapped button or a client retry would. Credits must move once,
// and every request is answered as the first one (F3.10.2).
func TestRaceRepeatedConfirms(t *testing.T) {
	w := newPostgresWorld(t)
	const confirms = 6
	for round := range rounds {
		id := w.advance(t, errand.StatusDelivered)
		responses := make([]*httptest.ResponseRecorder, confirms)
		fns := make([]func(), confirms)
		for i := range fns {
			fns[i] = func() { responses[i] = w.act(t, w.requester, id, "confirm", 5) }
		}
		concurrently(fns...)

		for i, r := range responses {
			require.Equal(t, http.StatusOK, r.Code, "round %d, confirm %d: %s", round, i, r.Body.String())
			assert.Equal(t, "COMPLETED", decode(t, r, http.StatusOK)["status"])
		}
		assert.Equal(t, 1, count(w.records.events(t, id), "confirm"))
		assert.Equal(t, 1, count(w.records.outbox(t, id), errand.KindCreditTransfer), "credits move once")
	}
}

// TestRaceDoubleTappedCourierActions sends the same accept, and then the
// same pickup, twice at once. Both copies are answered 200 and the courier
// is not told that the action they just took was rejected.
func TestRaceDoubleTappedCourierActions(t *testing.T) {
	w := newPostgresWorld(t)
	for round := range rounds {
		id := w.create(t)
		w.reserve(t, id)
		for _, step := range []struct {
			action   string
			version  int
			rejected string
		}{
			{"accept", 2, errand.ErrandAcceptRejected.Kind()},
			{"pickup", 3, errand.ErrandPickupRejected.Kind()},
		} {
			var first, second *httptest.ResponseRecorder
			concurrently(
				func() { first = w.act(t, w.courier, id, step.action, step.version) },
				func() { second = w.act(t, w.courier, id, step.action, step.version) },
			)
			require.Equal(t, http.StatusOK, first.Code, "round %d %s: %s", round, step.action, first.Body.String())
			require.Equal(t, http.StatusOK, second.Code, "round %d %s: %s", round, step.action, second.Body.String())
			assert.JSONEq(t, first.Body.String(), second.Body.String())
			assert.Zero(t, count(w.records.outbox(t, id), step.rejected))
		}
		assert.Equal(t, []string{"submit", "reservation_confirmed", "accept", "pick_up"}, w.records.events(t, id))
	}
}
