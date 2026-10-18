package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Defaults for Config.
const (
	DefaultAcceptanceExpiryInterval  = 5 * time.Second
	DefaultPickupDeadlineInterval    = 15 * time.Second
	DefaultDeliveryDeadlineInterval  = 15 * time.Second
	DefaultVerificationLapseInterval = 5 * time.Second
	DefaultJitter                    = 0.2
	DefaultBatchSize                 = 100
)

// Config tunes a Scheduler. The zero value is usable.
type Config struct {
	// Intervals between two runs of each job. Zero means the job's default.
	AcceptanceExpiryInterval  time.Duration
	PickupDeadlineInterval    time.Duration
	DeliveryDeadlineInterval  time.Duration
	VerificationLapseInterval time.Duration
	// Jitter is the fraction each interval is randomly shortened or
	// lengthened by. Zero means DefaultJitter; a negative value disables
	// jitter. Values of 1 or more are capped just below 1.
	Jitter float64
	// BatchSize is the limit passed to ClaimDue. Zero means
	// DefaultBatchSize.
	BatchSize int
	// Logger receives one line per applied transition and per failure. nil
	// means slog.Default().
	Logger *slog.Logger
	// Rand returns a uniform value in [0, n) for n > 0. Tests inject a
	// deterministic source; nil means math/rand/v2.Int64N.
	Rand func(n int64) int64
}

func (c Config) withDefaults() Config {
	orDefault := func(d *time.Duration, def time.Duration) {
		if *d <= 0 {
			*d = def
		}
	}
	orDefault(&c.AcceptanceExpiryInterval, DefaultAcceptanceExpiryInterval)
	orDefault(&c.PickupDeadlineInterval, DefaultPickupDeadlineInterval)
	orDefault(&c.DeliveryDeadlineInterval, DefaultDeliveryDeadlineInterval)
	orDefault(&c.VerificationLapseInterval, DefaultVerificationLapseInterval)
	switch {
	case c.Jitter == 0:
		c.Jitter = DefaultJitter
	case c.Jitter < 0:
		c.Jitter = 0
	case c.Jitter >= 1:
		c.Jitter = 0.99
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Rand == nil {
		c.Rand = rand.Int64N
	}
	return c
}

// Scheduler runs the deadline jobs. Create it with New and start it with
// Run. It is safe for concurrent use.
type Scheduler struct {
	store  errand.Store
	clock  errand.Clock
	cfg    Config
	logger *slog.Logger
	jobs   []*job
}

// job is one deadline job and its counters.
type job struct {
	kind     errand.DueKind
	event    errand.Event
	interval time.Duration
	stats    counters
}

// New returns a Scheduler over store, reading the time from clock.
func New(store errand.Store, clock errand.Clock, cfg Config) *Scheduler {
	if store == nil {
		panic("scheduler: store is required")
	}
	if clock == nil {
		panic("scheduler: clock is required")
	}
	cfg = cfg.withDefaults()
	return &Scheduler{
		store:  store,
		clock:  clock,
		cfg:    cfg,
		logger: cfg.Logger,
		jobs: []*job{
			{kind: errand.AcceptanceExpiry, event: errand.EventAcceptanceDeadlinePassed, interval: cfg.AcceptanceExpiryInterval},
			{kind: errand.PickupDeadline, event: errand.EventPickupDeadlinePassed, interval: cfg.PickupDeadlineInterval},
			{kind: errand.DeliveryDeadline, event: errand.EventDeliveryDeadlinePassed, interval: cfg.DeliveryDeadlineInterval},
			{kind: errand.VerificationLapse, event: errand.EventVerificationLapsed, interval: cfg.VerificationLapseInterval},
		},
	}
}

// Kinds lists the DueKinds the scheduler runs, one job each.
func (s *Scheduler) Kinds() []errand.DueKind {
	kinds := make([]errand.DueKind, len(s.jobs))
	for i, j := range s.jobs {
		kinds[i] = j.kind
	}
	return kinds
}

// Run starts one goroutine per job and blocks until ctx is cancelled and
// every job has finished its current batch.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, j := range s.jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.loop(ctx, j)
		}()
	}
	wg.Wait()
}

// loop runs j at once, then once per jittered interval.
func (s *Scheduler) loop(ctx context.Context, j *job) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.drain(ctx, j)
		timer.Reset(s.delay(j.interval))
	}
}

// drain runs batches of j while they come back full. A batch is full when
// decide saw BatchSize errands, skipped ones included. A full batch in which
// nothing was applied stops the drain, because the next batch would select
// the same errands again.
func (s *Scheduler) drain(ctx context.Context, j *job) {
	for ctx.Err() == nil {
		res, err := s.runBatch(ctx, j)
		if err != nil || res.Seen < s.cfg.BatchSize || res.Applied == 0 {
			return
		}
	}
}

// delay returns interval shortened or lengthened by up to Jitter of itself.
func (s *Scheduler) delay(interval time.Duration) time.Duration {
	spread := int64(float64(interval) * s.cfg.Jitter)
	if spread <= 0 {
		return interval
	}
	return interval - time.Duration(spread) + time.Duration(s.cfg.Rand(2*spread+1))
}

// BatchResult is what one batch of a job did.
type BatchResult struct {
	// Seen is the number of errands decide was called for.
	Seen int
	// Applied is the number of transitions ClaimDue committed.
	Applied int
}

// RunOnce runs one batch of the job for kind at the clock's current time.
// Run calls it on every tick; it is exported for tests and operational
// tooling. A panic inside the batch is recovered and returned as an error.
func (s *Scheduler) RunOnce(ctx context.Context, kind errand.DueKind) (BatchResult, error) {
	for _, j := range s.jobs {
		if j.kind == kind {
			return s.runBatch(ctx, j)
		}
	}
	return BatchResult{}, fmt.Errorf("scheduler: no job for due kind %q", kind)
}

func (s *Scheduler) runBatch(ctx context.Context, j *job) (res BatchResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			j.stats.panics.Add(1)
			s.logger.LogAttrs(ctx, slog.LevelError, "scheduler job panicked",
				slog.String("job", string(j.kind)),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())),
			)
			res, err = BatchResult{}, fmt.Errorf("scheduler: job %s panicked: %v", j.kind, r)
		}
	}()

	now := s.clock.Now()
	var decided []errand.Transition
	seen := 0
	decide := func(e errand.Errand) (errand.Transition, bool, error) {
		seen++
		t, ok := s.decide(ctx, j, e, now)
		if ok {
			decided = append(decided, t)
		}
		return t, ok, nil
	}

	applied, err := s.store.ClaimDue(ctx, j.kind, now, s.cfg.BatchSize, decide)
	j.stats.batches.Add(1)
	if err != nil {
		j.stats.failures.Add(1)
		s.logger.LogAttrs(ctx, slog.LevelError, "scheduler batch failed",
			slog.String("job", string(j.kind)),
			slog.String("error", err.Error()),
		)
		return BatchResult{}, err
	}

	j.stats.seen.Add(uint64(seen))
	j.stats.applied.Add(uint64(applied))
	j.stats.skipped.Add(uint64(seen - len(decided)))
	for _, t := range decided {
		s.logger.LogAttrs(ctx, slog.LevelInfo, "scheduler applied transition",
			slog.String("job", string(j.kind)),
			slog.String("errand_id", t.ErrandID),
			slog.String("event", string(t.Event)),
			slog.String("from", string(t.From)),
			slog.String("to", string(t.To)),
			slog.Int("version", t.NewVersion),
		)
	}
	return BatchResult{Seen: seen, Applied: applied}, nil
}

// decide builds j's transition for the locked errand e at now, or reports a
// skip. The errand is skipped when it is no longer due for j's kind (its
// deadline was extended or it changed state after it was selected) or when
// Next refuses the event; neither aborts the batch.
func (s *Scheduler) decide(ctx context.Context, j *job, e errand.Errand, now time.Time) (errand.Transition, bool) {
	if !j.kind.Due(e, now) {
		s.logger.LogAttrs(ctx, slog.LevelDebug, "scheduler skipped errand that is no longer due",
			slog.String("job", string(j.kind)),
			slog.String("errand_id", e.ID),
			slog.String("status", string(e.Status)),
		)
		return errand.Transition{}, false
	}
	t, err := errand.Next(e, j.event, errand.SystemActor(), errand.EventInput{}, now)
	if err != nil {
		s.logger.LogAttrs(ctx, slog.LevelWarn, "scheduler skipped errand refused by the state machine",
			slog.String("job", string(j.kind)),
			slog.String("errand_id", e.ID),
			slog.String("status", string(e.Status)),
			slog.String("error", err.Error()),
		)
		return errand.Transition{}, false
	}
	return t, true
}

// JobStats are one job's counters since the Scheduler was created.
type JobStats struct {
	// Batches counts ClaimDue calls, Failures those that returned an error
	// and Panics the batches that panicked.
	Batches  uint64
	Failures uint64
	Panics   uint64
	// Seen, Applied and Skipped count errands over the successful batches.
	Seen    uint64
	Applied uint64
	Skipped uint64
}

type counters struct {
	batches, failures, panics, seen, applied, skipped atomic.Uint64
}

func (c *counters) snapshot() JobStats {
	return JobStats{
		Batches:  c.batches.Load(),
		Failures: c.failures.Load(),
		Panics:   c.panics.Load(),
		Seen:     c.seen.Load(),
		Applied:  c.applied.Load(),
		Skipped:  c.skipped.Load(),
	}
}

// Stats returns every job's counters, keyed by its DueKind.
func (s *Scheduler) Stats() map[errand.DueKind]JobStats {
	out := make(map[errand.DueKind]JobStats, len(s.jobs))
	for _, j := range s.jobs {
		out[j.kind] = j.stats.snapshot()
	}
	return out
}
