// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service notification real-time delivery.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package notifstream is the push side of the service. When the event consumer
// records a notification for a user, every HTTP stream that user has open is
// woken so it can send the fresh unread state. This replaces client polling:
// the server initiates delivery the moment something changes.
package notifstream

import "sync"

// Hub is an in-process, per-recipient signal bus. Recording a notification
// Notifies its recipient; each open stream Subscribed for that recipient is
// woken. Signals carry no data and coalesce: a woken stream reads the
// authoritative state over the normal query path, so a burst of events costs
// one refresh rather than one per event.
//
// The hub is in-process, so it wakes only streams connected to this instance.
// With several instances behind a load balancer a user's events and their
// stream can land on different instances; the per-stream heartbeat and the
// client's reconnect keep the badge eventually correct, and the authoritative
// count always comes from the shared database. A cross-instance fan-out (a
// shared bus) can replace this hub without touching the HTTP or consumer code.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

// NewHub returns an empty hub ready to Subscribe and Notify.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan struct{}]struct{})}
}

// Subscribe registers interest in one recipient's notifications. It returns a
// channel that receives a signal whenever that recipient is Notified, and a
// cancel that releases the subscription. cancel is idempotent and must be
// called (defer it) so a disconnected stream does not leak.
func (h *Hub) Subscribe(recipient string) (<-chan struct{}, func()) {
	// Buffer one so a Notify that races an in-progress refresh is not lost: the
	// stream sees the pending signal on its next loop and reads the latest
	// state once.
	ch := make(chan struct{}, 1)

	h.mu.Lock()
	set := h.subs[recipient]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		h.subs[recipient] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if set := h.subs[recipient]; set != nil {
				delete(set, ch)
				if len(set) == 0 {
					delete(h.subs, recipient)
				}
			}
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// Notify wakes every stream open for recipient. It never blocks: a subscriber
// that already has a pending signal is left as is, because one refresh observes
// the latest state no matter how many events arrived in the meantime.
func (h *Hub) Notify(recipient string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[recipient] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
