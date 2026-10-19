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

// Job names, used in logs, Stats and RunOnce. A deadline job is named after
// its DueKind.
const (
	JobAcceptanceExpiry     = string(errand.AcceptanceExpiry)
	JobPickupDeadline       = string(errand.PickupDeadline)
	JobDeliveryDeadline     = string(errand.DeliveryDeadline)
	JobVerificationLapse    = string(errand.VerificationLapse)
	JobVerificationReminder = "verification_reminder"
	JobPickupReminder       = "pickup_reminder"
)

// Defaults for Config.
const (
	DefaultAcceptanceExpiryInterval     = 5 * time.Second
	DefaultPickupDeadlineInterval       = 15 * time.Second
	DefaultDeliveryDeadlineInterval     = 15 * time.Second
	DefaultVerificationLapseInterval    = 5 * time.Second
	DefaultVerificationReminderInterval = 5 * time.Second
	DefaultPickupReminderInterval       = 5 * time.Second
	DefaultPickupReminderLead           = 10 * time.Minute
	DefaultJitter                       = 0.2
	DefaultBatchSize                    = 100
)

// Config tunes a Scheduler. The zero value is usable and runs the deadline
// jobs only.
type Config struct {
	// Intervals between two runs of each job. Zero means the job's default.
	AcceptanceExpiryInterval     time.Duration
	PickupDeadlineInterval       time.Duration
	DeliveryDeadlineInterval     time.Duration
	VerificationLapseInterval    time.Duration
	VerificationReminderInterval time.Duration
	PickupReminderInterval       time.Duration
	// PickupReminderLead is how long before the pickup deadline the courier
	// is reminded. Zero means DefaultPickupReminderLead. The verification
	// reminder's timing comes from Settings instead.
	PickupReminderLead time.Duration
	// Jitter is the fraction each interval is randomly shortened or
	// lengthened by. Zero means DefaultJitter; a negative value disables
	// jitter. Values of 1 or more are capped just below 1.
	Jitter float64
	// BatchSize is the number of errands one batch handles. Zero means
	// DefaultBatchSize.
	BatchSize int

	// Reminders and Settings enable the reminder jobs (B4b). Set both or
	// neither.
	Reminders ReminderStore
	Settings  errand.Settings

	// Logger receives one line per applied transition, per reminder and per
	// failure. nil means slog.Default().
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
	orDefault(&c.VerificationReminderInterval, DefaultVerificationReminderInterval)
	orDefault(&c.PickupReminderInterval, DefaultPickupReminderInterval)
	orDefault(&c.PickupReminderLead, DefaultPickupReminderLead)
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

// Scheduler runs the deadline jobs and, when configured, the reminder jobs.
// Create it with New and start it with Run. It is safe for concurrent use.
type Scheduler struct {
	store     errand.Store
	clock     errand.Clock
	reminders ReminderStore
	settings  errand.Settings
	cfg       Config
	logger    *slog.Logger
	jobs      []*job
}

// job is one periodic job and its counters. run handles one batch.
type job struct {
	name     string
	interval time.Duration
	// limit is the job's batch size; a batch that saw limit errands is full.
	limit int
	run   func(ctx context.Context, j *job) (BatchResult, error)
	stats counters
}

// New returns a Scheduler over store, reading the time from clock. The
// reminder jobs are added when cfg.Reminders and cfg.Settings are set.
func New(store errand.Store, clock errand.Clock, cfg Config) *Scheduler {
	if store == nil {
		panic("scheduler: store is required")
	}
	if clock == nil {
		panic("scheduler: clock is required")
	}
	if (cfg.Reminders == nil) != (cfg.Settings == nil) {
		panic("scheduler: reminders and settings must be set together")
	}
	cfg = cfg.withDefaults()
	s := &Scheduler{
		store:     store,
		clock:     clock,
		reminders: cfg.Reminders,
		settings:  cfg.Settings,
		cfg:       cfg,
		logger:    cfg.Logger,
	}
	s.addDeadlineJob(errand.AcceptanceExpiry, errand.EventAcceptanceDeadlinePassed, cfg.AcceptanceExpiryInterval)
	s.addDeadlineJob(errand.PickupDeadline, errand.EventPickupDeadlinePassed, cfg.PickupDeadlineInterval)
	s.addDeadlineJob(errand.DeliveryDeadline, errand.EventDeliveryDeadlinePassed, cfg.DeliveryDeadlineInterval)
	s.addDeadlineJob(errand.VerificationLapse, errand.EventVerificationLapsed, cfg.VerificationLapseInterval)
	if s.reminders != nil {
		s.addReminderJob(verificationReminder, cfg.VerificationReminderInterval)
		s.addReminderJob(pickupReminder(cfg.PickupReminderLead), cfg.PickupReminderInterval)
	}
	return s
}

// Jobs lists the names of the jobs the scheduler runs.
func (s *Scheduler) Jobs() []string {
	names := make([]string, len(s.jobs))
	for i, j := range s.jobs {
		names[i] = j.name
	}
	return names
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

// drain runs batches of j while they come back full. A batch is full when it
// saw the job's limit of errands, skipped ones included. A full batch in which
// nothing was applied stops the drain, because the next batch would select
// the same errands again.
func (s *Scheduler) drain(ctx context.Context, j *job) {
	for ctx.Err() == nil {
		res, err := s.runBatch(ctx, j)
		if err != nil || res.Seen < j.limit || res.Applied == 0 {
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
	// Seen is the number of errands the batch considered: decide calls for
	// a deadline job, candidates for a reminder job.
	Seen int
	// Applied is the number of transitions committed, or reminder rows
	// written.
	Applied int
}

// RunOnce runs one batch of the named job at the clock's current time. Run
// calls it on every tick; it is exported for tests and operational tooling.
// A panic inside the batch is recovered and returned as an error.
func (s *Scheduler) RunOnce(ctx context.Context, name string) (BatchResult, error) {
	for _, j := range s.jobs {
		if j.name == name {
			return s.runBatch(ctx, j)
		}
	}
	return BatchResult{}, fmt.Errorf("scheduler: no job named %q", name)
}

// runBatch runs one batch of j, recovering a panic and keeping the counters.
func (s *Scheduler) runBatch(ctx context.Context, j *job) (res BatchResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			j.stats.panics.Add(1)
			s.logger.LogAttrs(ctx, slog.LevelError, "scheduler job panicked",
				slog.String("job", j.name),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())),
			)
			res, err = BatchResult{}, fmt.Errorf("scheduler: job %s panicked: %v", j.name, r)
		}
	}()

	res, err = j.run(ctx, j)
	j.stats.batches.Add(1)
	if err != nil {
		j.stats.failures.Add(1)
		s.logger.LogAttrs(ctx, slog.LevelError, "scheduler batch failed",
			slog.String("job", j.name),
			slog.String("error", err.Error()),
		)
		return BatchResult{}, err
	}
	j.stats.seen.Add(uint64(res.Seen))
	j.stats.applied.Add(uint64(res.Applied))
	j.stats.skipped.Add(uint64(res.Seen - res.Applied))
	return res, nil
}

// JobStats are one job's counters since the Scheduler was created.
type JobStats struct {
	// Batches counts batches run, Failures those that returned an error and
	// Panics those that panicked.
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

// Stats returns every job's counters, keyed by job name.
func (s *Scheduler) Stats() map[string]JobStats {
	out := make(map[string]JobStats, len(s.jobs))
	for _, j := range s.jobs {
		out[j.name] = j.stats.snapshot()
	}
	return out
}
