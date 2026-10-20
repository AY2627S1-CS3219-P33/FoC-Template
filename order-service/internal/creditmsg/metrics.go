package creditmsg

import "sync"

// Outcomes counted per result type.
const (
	// OutcomeApplied: the result changed an errand or an outbox row.
	OutcomeApplied = "applied"
	// OutcomeResolved: the result no longer applied (redelivery, or the
	// errand or edit moved on) and was acknowledged without a change.
	OutcomeResolved = "resolved"
	// OutcomeRetried: a transient failure; the message was requeued.
	OutcomeRetried = "retried"
	// OutcomeRejected: the message broke the contract and was dead-lettered.
	OutcomeRejected = "rejected"
)

// ResultUnknown is the result label for a routing key that is not a credit
// result, so counter labels stay bounded.
const ResultUnknown = "unknown"

// Metrics receives the consumer's counters. It is small on purpose so I3 can
// back it with a metrics library; implementations must be safe for
// concurrent use.
type Metrics interface {
	// Handled counts one delivery of result (a credit result routing key or
	// ResultUnknown) with its outcome.
	Handled(result, outcome string)
}

// Counters is an in-memory Metrics.
type Counters struct {
	mu     sync.Mutex
	counts map[[2]string]int64
}

var _ Metrics = (*Counters)(nil)

func NewCounters() *Counters { return &Counters{counts: map[[2]string]int64{}} }

func (c *Counters) Handled(result, outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[[2]string{result, outcome}]++
}

// Count returns how many deliveries of result ended with outcome.
func (c *Counters) Count(result, outcome string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[[2]string{result, outcome}]
}
