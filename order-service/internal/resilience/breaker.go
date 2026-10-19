package resilience

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrCircuitOpen is returned without calling the dependency while the breaker
// is open, or while it is half-open and its single trial call is in flight.
var ErrCircuitOpen = errors.New("resilience: circuit open")

// Clock is the breaker's time source. errand.Clock, errand.SystemClock and
// testfakes.FakeClock all satisfy it.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// State is a circuit breaker state.
type State int

const (
	// StateClosed lets every call through and counts consecutive failures.
	StateClosed State = iota
	// StateOpen rejects every call until the cool-down has elapsed.
	StateOpen
	// StateHalfOpen lets exactly one trial call through; its outcome closes
	// or reopens the breaker.
	StateHalfOpen
)

// String returns "closed", "open" or "half-open", for readiness output.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Default breaker settings.
const (
	DefaultFailureThreshold = 5
	DefaultCoolDown         = 30 * time.Second
)

// BreakerConfig configures a Breaker.
type BreakerConfig struct {
	// Name identifies the dependency in readiness output and logs.
	Name string
	// FailureThreshold is the number of consecutive failures that opens the
	// breaker.
	FailureThreshold int
	// CoolDown is how long the breaker stays open before allowing a trial.
	CoolDown time.Duration
	// Clock is the time source; nil means the wall clock.
	Clock Clock
	// IsFailure decides whether an error returned by the call counts against
	// the breaker, so that, for example, a 404 or a validation refusal from a
	// healthy dependency does not open it. nil means every error counts. It
	// is not consulted for per-call timeouts (always a failure) or for the
	// caller's own cancellation (never a failure).
	IsFailure func(error) bool
}

// DefaultBreakerConfig returns a config with the default threshold and
// cool-down for the named dependency.
func DefaultBreakerConfig(name string) BreakerConfig {
	return BreakerConfig{
		Name:             name,
		FailureThreshold: DefaultFailureThreshold,
		CoolDown:         DefaultCoolDown,
	}
}

// Validate reports a config the breaker cannot run with.
func (c BreakerConfig) Validate() error {
	switch {
	case c.Name == "":
		return errors.New("resilience: breaker name is required")
	case c.FailureThreshold < 1:
		return errors.New("resilience: breaker failure threshold must be at least 1")
	case c.CoolDown <= 0:
		return errors.New("resilience: breaker cool-down must be positive")
	}
	return nil
}

// Breaker is a consecutive-failure circuit breaker. It is safe for
// concurrent use. Calls go through Do.
type Breaker struct {
	cfg BreakerConfig

	mu       sync.Mutex
	state    State
	failures int       // consecutive failures while closed
	openedAt time.Time // when the breaker last opened
	trial    bool      // half-open trial call in flight
	// generation changes on every state transition, so the outcome of a call
	// admitted under an earlier state is ignored.
	generation uint64
}

// NewBreaker returns a closed breaker.
func NewBreaker(cfg BreakerConfig) (*Breaker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	return &Breaker{cfg: cfg}, nil
}

// Name returns the configured dependency name.
func (b *Breaker) Name() string { return b.cfg.Name }

// State returns the current state. An open breaker whose cool-down has
// elapsed reports half-open.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.coolDownLocked()
	return b.state
}

// String returns "<name>: <state>", for readiness output.
func (b *Breaker) String() string {
	return b.cfg.Name + ": " + b.State().String()
}

// outcome is how a finished call affects the breaker.
type outcome int

const (
	outcomeSuccess outcome = iota
	outcomeFailure
	outcomeIgnored // caller cancelled, or an error IsFailure excludes
)

// ticket is an admitted call.
type ticket struct {
	generation uint64
	trial      bool
}

func (b *Breaker) coolDownLocked() {
	if b.state == StateOpen && !b.cfg.Clock.Now().Before(b.openedAt.Add(b.cfg.CoolDown)) {
		b.setStateLocked(StateHalfOpen)
	}
}

func (b *Breaker) setStateLocked(s State) {
	b.state = s
	b.failures = 0
	b.trial = false
	b.generation++
	if s == StateOpen {
		b.openedAt = b.cfg.Clock.Now()
	}
}

// allow admits a call or returns ErrCircuitOpen.
func (b *Breaker) allow() (ticket, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.coolDownLocked()
	switch b.state {
	case StateOpen:
		return ticket{}, ErrCircuitOpen
	case StateHalfOpen:
		if b.trial {
			return ticket{}, ErrCircuitOpen
		}
		b.trial = true
		return ticket{generation: b.generation, trial: true}, nil
	}
	return ticket{generation: b.generation}, nil
}

// record applies the outcome of an admitted call.
func (b *Breaker) record(t ticket, o outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t.generation != b.generation {
		return // the state changed while the call ran
	}
	if t.trial {
		switch o {
		case outcomeSuccess:
			b.setStateLocked(StateClosed)
		case outcomeFailure:
			b.setStateLocked(StateOpen)
		case outcomeIgnored:
			b.trial = false // no verdict; let the next call be the trial
		}
		return
	}
	switch o {
	case outcomeSuccess:
		b.failures = 0
	case outcomeFailure:
		b.failures++
		if b.failures >= b.cfg.FailureThreshold {
			b.setStateLocked(StateOpen)
		}
	}
}

func (b *Breaker) isFailure(err error) bool {
	return b.cfg.IsFailure == nil || b.cfg.IsFailure(err)
}
