package facts

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const errandID = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func ev(eventType string, version int, from, to Status, minutes int) Event {
	return Event{
		MessageID: errandID + ":" + eventType[len("errand."):] + ":" + string(rune('0'+version)),
		Type:      eventType, ErrandID: errandID, Version: version, From: from, To: to,
		Reward: 8, OccurredAt: at(minutes),
	}
}

func accepted(version, minutes int, courier string) Event {
	e := ev(TypeAccepted, version, StatusOpen, StatusAssigned, minutes)
	e.CourierID = courier
	return e
}

// lifecycle is a completed errand's events: opened, accepted, a withdraw and
// re-accept, picked up, delivered, completed.
func lifecycle() []Event {
	return []Event{
		ev(TypePlaced, 1, "", StatusPendingReservation, 0),
		ev(TypeOpened, 2, StatusPendingReservation, StatusOpen, 2),
		accepted(3, 10, "auth0|cou1"),
		ev(TypeWithdrawn, 4, StatusAssigned, StatusOpen, 20),
		accepted(5, 30, "auth0|cou2"),
		ev(TypePickedUp, 6, StatusAssigned, StatusPickedUp, 40),
		ev(TypeDelivered, 7, StatusPickedUp, StatusDelivered, 50),
		ev(TypeCompleted, 8, StatusDelivered, StatusCompleted, 60),
	}
}

func fold(events []Event) Fact {
	var f Fact
	for i, e := range events {
		if i == 0 {
			f = NewFact(e)
		} else {
			f = f.Merge(e)
		}
	}
	return f
}

func TestInOrderLifecycle(t *testing.T) {
	f := fold(lifecycle())

	require.Equal(t, StatusCompleted, f.Status)
	require.Equal(t, 8, f.Version)
	require.Equal(t, at(2), *f.OpenedAt, "opened_at is the errand.opened time, not the creation time")
	require.Equal(t, at(10), *f.FirstAcceptedAt, "a withdraw and re-accept does not move the first accept")
	require.Equal(t, StatusCompleted, f.TerminalStatus)
	require.Equal(t, at(60), *f.TerminalAt)
	require.True(t, f.EnrichmentDue)
}

func TestAnyDeliveryOrderGivesTheSameRow(t *testing.T) {
	want := fold(lifecycle())
	rng := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		events := slices.Clone(lifecycle())
		rng.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })

		require.Equal(t, want, fold(events), "order: %v", types(events))
	}
}

func TestRedeliveredEventsChangeNothing(t *testing.T) {
	want := fold(lifecycle())
	events := lifecycle()
	doubled := slices.Concat(events, events, events[2:5])

	require.Equal(t, want, fold(doubled))
}

func TestEqualVersionWithdrawnAndExpiredPair(t *testing.T) {
	// The withdrawn/expired pair shares a version and, being one transition,
	// a time. The errand ends EXPIRED whichever arrives first.
	opened := ev(TypeOpened, 2, StatusPendingReservation, StatusOpen, 2)
	assigned := accepted(3, 10, "auth0|cou1")
	withdrawn := ev(TypeWithdrawn, 4, StatusAssigned, StatusOpen, 30)
	expired := ev(TypeExpired, 4, StatusAssigned, StatusExpired, 30)

	for name, order := range map[string][]Event{
		"withdrawn first": {opened, assigned, withdrawn, expired},
		"expired first":   {opened, assigned, expired, withdrawn},
	} {
		t.Run(name, func(t *testing.T) {
			f := fold(order)

			require.Equal(t, StatusExpired, f.Status)
			require.Equal(t, 4, f.Version)
			require.Equal(t, StatusExpired, f.TerminalStatus)
			require.Equal(t, at(30), *f.TerminalAt)
		})
	}
}

func TestEqualVersionLaterOccurredAtWins(t *testing.T) {
	// edit_requested keeps the version of the opened errand and happens later.
	opened := ev(TypeOpened, 2, StatusPendingReservation, StatusOpen, 2)
	edit := ev(TypeEditRequested, 2, StatusOpen, StatusOpen, 5)
	edit.Reward = 12

	require.Equal(t, 12, fold([]Event{opened, edit}).Reward)
	require.Equal(t, 12, fold([]Event{edit, opened}).Reward, "the later event wins whichever arrives first")
}

func TestStaleVersionDoesNotRollTheStateBack(t *testing.T) {
	f := fold([]Event{
		ev(TypeCompleted, 8, StatusDelivered, StatusCompleted, 60),
		ev(TypePickedUp, 6, StatusAssigned, StatusPickedUp, 40),
	})

	require.Equal(t, StatusCompleted, f.Status)
	require.Equal(t, 8, f.Version)
}

func TestOnlyAcceptedAndTerminalEventsOweEnrichment(t *testing.T) {
	require.False(t, fold(lifecycle()[:2]).EnrichmentDue, "placed and opened")
	require.True(t, fold(lifecycle()[:3]).EnrichmentDue, "accepted")

	cancelled := fold([]Event{
		ev(TypePlaced, 1, "", StatusPendingReservation, 0),
		ev(TypeOpened, 2, StatusPendingReservation, StatusOpen, 2),
		ev(TypeCancelled, 3, StatusOpen, StatusCancelled, 9),
	})
	require.True(t, cancelled.EnrichmentDue, "an errand cancelled before anyone accepted it")
	require.Nil(t, cancelled.FirstAcceptedAt)
	require.Equal(t, StatusCancelled, cancelled.TerminalStatus)
}

func TestRejectedAndRefundedAreTerminal(t *testing.T) {
	rejected := fold([]Event{
		ev(TypePlaced, 1, "", StatusPendingReservation, 0),
		ev(TypeRejected, 2, StatusPendingReservation, StatusRejected, 1),
	})
	require.Equal(t, StatusRejected, rejected.TerminalStatus)
	require.Nil(t, rejected.OpenedAt, "never opened")

	refunded := fold([]Event{
		ev(TypeReviewRequired, 7, StatusDelivered, StatusReviewRequired, 50),
		ev(TypeRefunded, 8, StatusReviewRequired, StatusRefunded, 70),
	})
	require.Equal(t, StatusRefunded, refunded.TerminalStatus)
}

func TestValidate(t *testing.T) {
	good := ev(TypeAccepted, 3, StatusOpen, StatusAssigned, 10)
	require.NoError(t, good.Validate())

	for name, mutate := range map[string]func(*Event){
		"no message id":                func(e *Event) { e.MessageID = "" },
		"not a transition event":       func(e *Event) { e.Type = "errand.accept_rejected" },
		"unrelated type":               func(e *Event) { e.Type = "credit.reserved" },
		"upper-case errand id":         func(e *Event) { e.ErrandID = "6F1C2B0E-8D4A-4E1B-9C3F-2A7D5E8B9C10" },
		"not a uuid":                   func(e *Event) { e.ErrandID = "E" },
		"zero version":                 func(e *Event) { e.Version = 0 },
		"unknown to":                   func(e *Event) { e.To = "LOST" },
		"missing to":                   func(e *Event) { e.To = "" },
		"unknown from":                 func(e *Event) { e.From = "LOST" },
		"negative reward":              func(e *Event) { e.Reward = -1 },
		"no time":                      func(e *Event) { e.OccurredAt = time.Time{} },
		"completed ending in progress": func(e *Event) { e.Type, e.To = TypeCompleted, StatusAssigned },
	} {
		t.Run(name, func(t *testing.T) {
			e := good
			mutate(&e)
			require.Error(t, e.Validate())
		})
	}
	creation := ev(TypePlaced, 1, "", StatusPendingReservation, 0)
	require.NoError(t, creation.Validate(), "the creation event has no from")
}

func TestTerminalStatuses(t *testing.T) {
	for _, s := range TerminalStatuses() {
		require.True(t, s.Terminal(), s)
	}
	require.Len(t, TerminalStatuses(), 5)
	for _, s := range []Status{StatusPendingReservation, StatusOpen, StatusAssigned, StatusPickedUp, StatusDelivered, StatusReviewRequired} {
		require.False(t, s.Terminal(), s)
	}
	require.Len(t, statuses, 11)
}

func types(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}
