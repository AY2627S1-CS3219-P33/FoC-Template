// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service notification real-time delivery.
// Author review: I validated correctness, edited for style, and added boundary checks.

package notifstream

import (
	"context"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notification"
)

// recorder is the write path notifstream decorates: the notification Service.
type recorder interface {
	Record(ctx context.Context, messageID string, items []notification.Pending) (int, error)
}

// Recorder records notifications and then wakes the recipients' open streams.
// It has the same Record method the event handler depends on, so it drops into
// the consumer in place of the bare Service.
type Recorder struct {
	inner recorder
	hub   *Hub
}

// NewRecorder wraps a recorder so that each newly stored notification wakes the
// recipient's live streams.
func NewRecorder(inner recorder, hub *Hub) *Recorder {
	if inner == nil || hub == nil {
		panic("notifstream: recorder and hub are required")
	}
	return &Recorder{inner: inner, hub: hub}
}

// Record stores the notifications, then signals each recipient. Signalling is
// best-effort and happens only after a successful write, so a stream is never
// woken for a notification that was not stored.
func (r *Recorder) Record(ctx context.Context, messageID string, items []notification.Pending) (int, error) {
	n, err := r.inner.Record(ctx, messageID, items)
	// Wake streams only when something was actually stored. A redelivery of an
	// already-recorded event stores nothing (the write is idempotent) and must
	// not produce a spurious signal.
	if err != nil || n == 0 {
		return n, err
	}
	for _, recipient := range distinct(items) {
		r.hub.Notify(recipient)
	}
	return n, err
}

// distinct returns each recipient once, preserving order. One event can address
// both parties, but a recipient is signalled at most once per event.
func distinct(items []notification.Pending) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		if _, ok := seen[it.Recipient]; ok {
			continue
		}
		seen[it.Recipient] = struct{}{}
		out = append(out, it.Recipient)
	}
	return out
}
