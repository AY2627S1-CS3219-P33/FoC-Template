// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-20
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package messaging

import "sync"

// Outbox row families, used as a log attribute.
const (
	FamilyCredit = "credit"
	FamilyEvent  = "event"
)

// Metrics receives the dispatcher's counters. It is small on purpose so I3
// can back it with a metrics library; implementations must be safe for
// concurrent use.
type Metrics interface {
	// Sent, Rescheduled and Failed count rows by outbox kind.
	Sent(kind string)
	Rescheduled(kind string)
	Failed(kind string)
}

// Counter names for Counters.Count.
const (
	CounterSent        = "sent"
	CounterRescheduled = "rescheduled"
	CounterFailed      = "failed"
)

// Counters is an in-memory Metrics.
type Counters struct {
	mu     sync.Mutex
	counts map[[2]string]int64
}

var _ Metrics = (*Counters)(nil)

func NewCounters() *Counters {
	return &Counters{counts: map[[2]string]int64{}}
}

func (c *Counters) Sent(kind string)        { c.add(CounterSent, kind) }
func (c *Counters) Rescheduled(kind string) { c.add(CounterRescheduled, kind) }
func (c *Counters) Failed(kind string)      { c.add(CounterFailed, kind) }

// Count returns counter (CounterSent, …) for kind.
func (c *Counters) Count(counter, kind string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[[2]string{counter, kind}]
}

func (c *Counters) add(counter, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[[2]string{counter, kind}]++
}
