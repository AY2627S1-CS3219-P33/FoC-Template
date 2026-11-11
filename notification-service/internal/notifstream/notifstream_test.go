// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Tests for notification-service notification real-time delivery.
// Author review: I validated correctness, edited for style, and added boundary checks.

package notifstream

import (
	"context"
	"errors"
	"testing"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notification"
)

// fakeRecorder is the inner write path. It reports a fixed inserted count and
// optional error so the decorator's signalling can be checked in isolation.
type fakeRecorder struct {
	n   int
	err error
}

func (f fakeRecorder) Record(context.Context, string, []notification.Pending) (int, error) {
	return f.n, f.err
}

func items(recipients ...string) []notification.Pending {
	out := make([]notification.Pending, 0, len(recipients))
	for _, r := range recipients {
		out = append(out, notification.Pending{Recipient: r})
	}
	return out
}

func TestSubscribeReceivesASignalForItsRecipientOnly(t *testing.T) {
	hub := NewHub()
	req, cancelReq := hub.Subscribe("req")
	defer cancelReq()
	other, cancelOther := hub.Subscribe("someone-else")
	defer cancelOther()

	hub.Notify("req")

	select {
	case <-req:
	default:
		t.Fatal("the subscribed recipient was not signalled")
	}
	select {
	case <-other:
		t.Fatal("a different recipient must not be signalled")
	default:
	}
}

func TestNotifyCoalescesAndNeverBlocks(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe("req")
	defer cancel()

	// Many events with nobody draining must not block, and collapse to one
	// pending signal: a single refresh will observe the latest state.
	for i := 0; i < 100; i++ {
		hub.Notify("req")
	}

	got := 0
	for {
		select {
		case <-ch:
			got++
			continue
		default:
		}
		break
	}
	if got != 1 {
		t.Fatalf("expected one coalesced signal, got %d", got)
	}
}

func TestCancelStopsAndReleasesTheSubscription(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe("req")

	cancel()
	cancel() // idempotent: a second call must not panic
	hub.Notify("req")

	select {
	case <-ch:
		t.Fatal("a cancelled subscription must not be signalled")
	default:
	}
	if len(hub.subs) != 0 {
		t.Fatalf("cancel should remove the recipient's empty subscriber set, have %d", len(hub.subs))
	}
}

func TestRecorderSignalsEachRecipientOnceAfterAWrite(t *testing.T) {
	hub := NewHub()
	rec := NewRecorder(fakeRecorder{n: 2}, hub)
	req, cancelReq := hub.Subscribe("req")
	defer cancelReq()
	cour, cancelCour := hub.Subscribe("cour")
	defer cancelCour()

	// One event addressing both parties (requester listed twice) signals each
	// recipient exactly once.
	n, err := rec.Record(context.Background(), "m1", items("req", "cour", "req"))
	if err != nil || n != 2 {
		t.Fatalf("Record returned (%d, %v), want (2, nil)", n, err)
	}
	for name, ch := range map[string]<-chan struct{}{"req": req, "cour": cour} {
		select {
		case <-ch:
		default:
			t.Fatalf("recipient %q was not signalled", name)
		}
	}
}

func TestRecorderDoesNotSignalWhenNothingWasStored(t *testing.T) {
	hub := NewHub()
	req, cancel := hub.Subscribe("req")
	defer cancel()

	// A redelivery stores nothing (n == 0): no signal, so a stream is never
	// woken for a notification that was not written.
	rec := NewRecorder(fakeRecorder{n: 0}, hub)
	if _, err := rec.Record(context.Background(), "m1", items("req")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	select {
	case <-req:
		t.Fatal("an idempotent redelivery must not signal")
	default:
	}
}

func TestRecorderDoesNotSignalOnError(t *testing.T) {
	hub := NewHub()
	req, cancel := hub.Subscribe("req")
	defer cancel()

	want := errors.New("database unavailable")
	rec := NewRecorder(fakeRecorder{n: 1, err: want}, hub)
	if _, err := rec.Record(context.Background(), "m1", items("req")); !errors.Is(err, want) {
		t.Fatalf("Record error = %v, want %v", err, want)
	}
	select {
	case <-req:
		t.Fatal("a failed write must not signal")
	default:
	}
}
