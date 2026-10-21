package messaging

import "sync"

// Outbox row families, as reported to Metrics.Backlog.
const (
	FamilyCredit = "credit"
	FamilyEvent  = "event"
)

// Metrics receives the dispatcher's counters. It is small on purpose so I3
// can back it with a metrics library; implementations must be safe for
// concurrent use.
type Metrics interface {
	// Sent, Rescheduled, Resent and Failed count rows by outbox kind.
	Sent(kind string)
	Rescheduled(kind string)
	Resent(kind string)
	Failed(kind string)
	// Backlog records how many rows of family (FamilyCredit or FamilyEvent)
	// were due at the last sweep.
	Backlog(family string, due int64)
}

// Counter names for Counters.Count.
const (
	CounterSent        = "sent"
	CounterRescheduled = "rescheduled"
	CounterResent      = "resent"
	CounterFailed      = "failed"
)

// Counters is an in-memory Metrics.
type Counters struct {
	mu      sync.Mutex
	counts  map[[2]string]int64
	backlog map[string]int64
}

var _ Metrics = (*Counters)(nil)

func NewCounters() *Counters {
	return &Counters{counts: map[[2]string]int64{}, backlog: map[string]int64{}}
}

func (c *Counters) Sent(kind string)        { c.add(CounterSent, kind) }
func (c *Counters) Rescheduled(kind string) { c.add(CounterRescheduled, kind) }
func (c *Counters) Resent(kind string)      { c.add(CounterResent, kind) }
func (c *Counters) Failed(kind string)      { c.add(CounterFailed, kind) }

func (c *Counters) Backlog(family string, due int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backlog[family] = due
}

// Count returns counter (CounterSent, …) for kind.
func (c *Counters) Count(counter, kind string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[[2]string{counter, kind}]
}

// LastBacklog returns the backlog last recorded for family.
func (c *Counters) LastBacklog(family string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.backlog[family]
}

func (c *Counters) add(counter, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[[2]string{counter, kind}]++
}
