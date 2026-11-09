package readiness

import "context"

// Status is one dependency's state as /readyz reports it.
type Status string

const (
	// StatusOK: a pinged dependency answered.
	StatusOK Status = "ok"
	// StatusDegraded: an optional pinged dependency did not answer in time.
	StatusDegraded Status = "degraded"
	// StatusRunning and StatusStopped report a background consumer.
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	// StatusDown: Postgres did not answer. It makes the service not ready
	// and is never shown in the response.
	StatusDown Status = "down"
)

// healthy reports whether s counts towards "ready" rather than "degraded".
func (s Status) healthy() bool {
	switch s {
	case StatusOK, StatusRunning:
		return true
	}
	return false
}

// reportable reports whether s may appear in the response. Anything else a
// Checker returns is shown as StatusDegraded, so no free text leaks out.
func (s Status) reportable() bool {
	switch s {
	case StatusOK, StatusDegraded, StatusRunning, StatusStopped:
		return true
	}
	return false
}

// Dependency names, as they appear under "dependencies".
const (
	NamePostgres = "postgres"
	NameEvents   = "events"
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

// Events reports the event consumer: StatusRunning or StatusStopped.
func Events(running func() bool) Checker {
	if running == nil {
		panic("readiness: running function is required for " + NameEvents)
	}
	return Func(NameEvents, func(context.Context) Status {
		if running() {
			return StatusRunning
		}
		return StatusStopped
	})
}
