package readiness

import (
	"context"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
)

// Status is one dependency's state as /readyz reports it.
type Status string

const (
	// StatusOK: a pinged dependency answered.
	StatusOK Status = "ok"
	// StatusDegraded: an optional pinged dependency did not answer in time.
	StatusDegraded Status = "degraded"
	// StatusDisabled: an optional dependency is not configured (Redis with
	// an empty REDIS_URL).
	StatusDisabled Status = "disabled"
	// StatusRunning and StatusStopped report a background consumer.
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	// StatusClosed, StatusOpen and StatusHalfOpen report a circuit breaker.
	StatusClosed   Status = "closed"
	StatusOpen     Status = "open"
	StatusHalfOpen Status = "half_open"
	// StatusDown: Postgres did not answer. It makes the service not ready
	// and is never shown in the response.
	StatusDown Status = "down"
)

// healthy reports whether s counts towards "ready" rather than "degraded".
func (s Status) healthy() bool {
	switch s {
	case StatusOK, StatusDisabled, StatusRunning, StatusClosed:
		return true
	}
	return false
}

// reportable reports whether s may appear in the response. Anything else a
// Checker returns is shown as StatusDegraded, so no free text leaks out.
func (s Status) reportable() bool {
	switch s {
	case StatusOK, StatusDegraded, StatusDisabled, StatusRunning, StatusStopped,
		StatusClosed, StatusOpen, StatusHalfOpen:
		return true
	}
	return false
}

// Dependency names, as they appear under "dependencies" (openapi/common.yaml
// Readiness).
const (
	NamePostgres      = "postgres"
	NameRedis         = "redis"
	NameRabbitMQ      = "rabbitmq"
	NameCreditResults = "creditResults"
	NameSupplier      = "supplier"
	NameUser          = "user"
)

// Checker reports one named dependency. Check must return soon after ctx is
// done; the Handler gives each check its own short deadline. The adapters in
// this package guarantee that even when the function they wrap ignores ctx.
type Checker interface {
	Name() string
	Check(ctx context.Context) Status
}

// Func adapts a function to a Checker.
func Func(name string, check func(context.Context) Status) Checker {
	if check == nil {
		panic("readiness: check function is required for " + name)
	}
	return funcChecker{name: name, check: check}
}

type funcChecker struct {
	name  string
	check func(context.Context) Status
}

func (c funcChecker) Name() string                     { return c.name }
func (c funcChecker) Check(ctx context.Context) Status { return c.check(ctx) }

// Ping reports up when ping returns nil before ctx is done, and down
// otherwise. The error is discarded. If ping ignores ctx, Ping still returns
// down at the deadline and lets ping finish in the background.
func Ping(name string, ping func(context.Context) error, up, down Status) Checker {
	if ping == nil {
		panic("readiness: ping function is required for " + name)
	}
	return Func(name, func(ctx context.Context) Status {
		result := make(chan error, 1)
		go func() { result <- ping(ctx) }()
		select {
		case err := <-result:
			if err != nil {
				return down
			}
			return up
		case <-ctx.Done():
			return down
		}
	})
}

// Postgres pings the database, for example with (*pgxpool.Pool).Ping:
// StatusOK or StatusDown.
func Postgres(ping func(context.Context) error) Checker {
	return Ping(NamePostgres, ping, StatusOK, StatusDown)
}

// Redis pings the cache, for example with (*cache.Client).Ping: StatusOK or
// StatusDegraded. A nil ping means Redis is not configured and always
// reports StatusDisabled.
func Redis(ping func(context.Context) error) Checker {
	if ping == nil {
		return Func(NameRedis, func(context.Context) Status { return StatusDisabled })
	}
	return Ping(NameRedis, ping, StatusOK, StatusDegraded)
}

// RabbitMQ pings the broker with the function Person C's publisher
// provides: StatusOK or StatusDegraded.
func RabbitMQ(ping func(context.Context) error) Checker {
	return Ping(NameRabbitMQ, ping, StatusOK, StatusDegraded)
}

// CreditResults reports Person C's credit-result consumer: StatusRunning or
// StatusStopped.
func CreditResults(running func() bool) Checker {
	if running == nil {
		panic("readiness: running function is required for " + NameCreditResults)
	}
	return Func(NameCreditResults, func(context.Context) Status {
		if running() {
			return StatusRunning
		}
		return StatusStopped
	})
}

// BreakerState is what Breaker reads; *resilience.Breaker implements it.
type BreakerState interface {
	State() resilience.State
}

// Breaker reports a client's circuit breaker, for example
// (*supplierclient.Client).Breaker(): StatusClosed, StatusOpen or
// StatusHalfOpen.
func Breaker(name string, breaker BreakerState) Checker {
	if breaker == nil {
		panic("readiness: breaker is required for " + name)
	}
	return Func(name, func(context.Context) Status {
		switch breaker.State() {
		case resilience.StateClosed:
			return StatusClosed
		case resilience.StateOpen:
			return StatusOpen
		case resilience.StateHalfOpen:
			return StatusHalfOpen
		}
		return StatusDegraded
	})
}
