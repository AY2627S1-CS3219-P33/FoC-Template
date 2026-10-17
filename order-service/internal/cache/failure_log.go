package cache

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
)

// failureLog logs cache failures at debug level, at most once per interval.
// Failures in between are counted and reported as "suppressed" on the next
// log, so a Redis outage on a hot path costs one log line per interval
// rather than one per request.
type failureLog struct {
	logger   *slog.Logger
	clock    resilience.Clock
	interval time.Duration

	mu         sync.Mutex
	last       time.Time
	logged     bool
	suppressed int
}

func newFailureLog(logger *slog.Logger, clock resilience.Clock, interval time.Duration) *failureLog {
	return &failureLog{logger: logger, clock: clock, interval: interval}
}

// failure records one failure and logs it unless rate limited. Keys and
// errors are logged; values never are.
func (l *failureLog) failure(ctx context.Context, op, key string, err error) {
	if !l.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	l.mu.Lock()
	now := l.clock.Now()
	if l.logged && now.Sub(l.last) < l.interval {
		l.suppressed++
		l.mu.Unlock()
		return
	}
	suppressed := l.suppressed
	l.last, l.logged, l.suppressed = now, true, 0
	l.mu.Unlock()

	l.logger.LogAttrs(ctx, slog.LevelDebug, "cache unavailable; treating as miss",
		slog.String("op", op),
		slog.String("key", key),
		slog.String("error", err.Error()),
		slog.Int("suppressed", suppressed),
	)
}
