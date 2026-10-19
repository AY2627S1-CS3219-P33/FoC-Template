package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
)

// leakyError carries the kind of detail the response must never show.
var leakyError = errors.New("dial tcp: postgres://order:s3cret@db.internal:5432/order refused")

// fakeChecker reports a fixed status.
type fakeChecker struct {
	name   string
	status Status
}

func (c fakeChecker) Name() string                 { return c.name }
func (c fakeChecker) Check(context.Context) Status { return c.status }

type breakerStub struct{ state resilience.State }

func (b breakerStub) State() resilience.State { return b.state }

type readyzBody struct {
	Status       string            `json:"status"`
	Dependencies map[string]Status `json:"dependencies"`
}

func serve(t *testing.T, handler Handler) (*httptest.ResponseRecorder, readyzBody) {
	t.Helper()
	router := http.NewServeMux()
	handler.RegisterRoutes(router)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var body readyzBody
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return recorder, body
}

func ping(up bool) func(context.Context) error {
	return func(context.Context) error {
		if up {
			return nil
		}
		return leakyError
	}
}

func TestReadinessForEveryDependencyCombination_NFR4_1_2(t *testing.T) {
	type redisMode struct {
		name   string
		ping   func(context.Context) error
		status Status
	}
	redisModes := []redisMode{
		{"redis up", ping(true), StatusOK},
		{"redis down", ping(false), StatusDegraded},
		{"redis disabled", nil, StatusDisabled},
	}

	for _, postgresUp := range []bool{true, false} {
		for _, redis := range redisModes {
			for _, rabbitUp := range []bool{true, false} {
				name := map[bool]string{true: "postgres up", false: "postgres down"}[postgresUp] + "/" +
					redis.name + "/" + map[bool]string{true: "rabbitmq up", false: "rabbitmq down"}[rabbitUp]
				t.Run(name, func(t *testing.T) {
					handler := NewHandler(Postgres(ping(postgresUp)),
						Redis(redis.ping),
						RabbitMQ(ping(rabbitUp)),
						fakeChecker{NameCreditResults, StatusRunning},
						fakeChecker{NameSupplier, StatusClosed},
						fakeChecker{NameUser, StatusClosed},
					)
					recorder, body := serve(t, handler)
					require.NotContains(t, recorder.Body.String(), "s3cret")
					require.NotContains(t, recorder.Body.String(), "postgres://")
					require.NotContains(t, recorder.Body.String(), "refused")

					if !postgresUp {
						require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
						require.JSONEq(t, `{"status":"not_ready"}`, recorder.Body.String())
						return
					}

					rabbit := StatusOK
					if !rabbitUp {
						rabbit = StatusDegraded
					}
					want := "ready"
					if redis.status == StatusDegraded || !rabbitUp {
						want = "degraded"
					}
					require.Equal(t, http.StatusOK, recorder.Code)
					require.Equal(t, readyzBody{Status: want, Dependencies: map[string]Status{
						NamePostgres:      StatusOK,
						NameRedis:         redis.status,
						NameRabbitMQ:      rabbit,
						NameCreditResults: StatusRunning,
						NameSupplier:      StatusClosed,
						NameUser:          StatusClosed,
					}}, body)
				})
			}
		}
	}
}

func TestReadinessReportsConsumerAndBreakers(t *testing.T) {
	tests := []struct {
		name     string
		running  bool
		supplier resilience.State
		user     resilience.State
		want     readyzBody
	}{
		{"all healthy", true, resilience.StateClosed, resilience.StateClosed, readyzBody{Status: "ready", Dependencies: map[string]Status{
			NamePostgres: StatusOK, NameCreditResults: StatusRunning, NameSupplier: StatusClosed, NameUser: StatusClosed}}},
		{"consumer stopped", false, resilience.StateClosed, resilience.StateClosed, readyzBody{Status: "degraded", Dependencies: map[string]Status{
			NamePostgres: StatusOK, NameCreditResults: StatusStopped, NameSupplier: StatusClosed, NameUser: StatusClosed}}},
		{"supplier open", true, resilience.StateOpen, resilience.StateClosed, readyzBody{Status: "degraded", Dependencies: map[string]Status{
			NamePostgres: StatusOK, NameCreditResults: StatusRunning, NameSupplier: StatusOpen, NameUser: StatusClosed}}},
		{"user half-open", true, resilience.StateClosed, resilience.StateHalfOpen, readyzBody{Status: "degraded", Dependencies: map[string]Status{
			NamePostgres: StatusOK, NameCreditResults: StatusRunning, NameSupplier: StatusClosed, NameUser: StatusHalfOpen}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHandler(Postgres(ping(true)),
				CreditResults(func() bool { return tt.running }),
				Breaker(NameSupplier, breakerStub{tt.supplier}),
				Breaker(NameUser, breakerStub{tt.user}),
			)
			recorder, body := serve(t, handler)
			require.Equal(t, http.StatusOK, recorder.Code, "only Postgres makes the service not ready")
			require.Equal(t, tt.want, body)
		})
	}
}

// manualClock lets the test move a real breaker from open to half-open.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestBreakerFollowsRealBreakerState(t *testing.T) {
	clock := &manualClock{now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	breaker, err := resilience.NewBreaker(resilience.BreakerConfig{
		Name: "supplier", FailureThreshold: 1, CoolDown: time.Minute, Clock: clock,
	})
	require.NoError(t, err)
	checker := Breaker(NameSupplier, breaker)
	require.Equal(t, NameSupplier, checker.Name())
	require.Equal(t, StatusClosed, checker.Check(context.Background()))

	_ = resilience.Do(context.Background(), breaker, time.Second, func(context.Context) error { return errors.New("boom") })
	require.Equal(t, StatusOpen, checker.Check(context.Background()))

	clock.advance(time.Minute)
	require.Equal(t, StatusHalfOpen, checker.Check(context.Background()), "wire form is half_open, not half-open")
}

func TestUnknownStatusIsReportedAsDegraded(t *testing.T) {
	handler := NewHandler(Postgres(ping(true)),
		fakeChecker{"custom", Status("error: " + leakyError.Error())},
		fakeChecker{"down", StatusDown},
	)
	recorder, body := serve(t, handler)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "s3cret")
	require.Equal(t, readyzBody{Status: "degraded", Dependencies: map[string]Status{
		NamePostgres: StatusOK, "custom": StatusDegraded, "down": StatusDegraded}}, body)
}

func TestPostgresNotOKIsNotReady(t *testing.T) {
	for _, status := range []Status{StatusDown, StatusDegraded, Status("")} {
		recorder, body := serve(t, NewHandler(fakeChecker{NamePostgres, status}, fakeChecker{NameRedis, StatusOK}))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Equal(t, readyzBody{Status: "not_ready"}, body)
	}
}

func TestChecksRunInParallelWithTimeout(t *testing.T) {
	// Each blocking check waits until all of them have started, which only
	// happens if they run in parallel.
	const blocking = 4
	var started sync.WaitGroup
	started.Add(blocking)
	allStarted := make(chan struct{})
	go func() { started.Wait(); close(allStarted) }()

	var deadlines atomic.Int32
	barrier := func(name string) Checker {
		return Func(name, func(ctx context.Context) Status {
			if _, ok := ctx.Deadline(); ok {
				deadlines.Add(1)
			}
			started.Done()
			select {
			case <-allStarted:
				return StatusOK
			case <-ctx.Done():
				return StatusDegraded
			}
		})
	}
	handler := NewHandler(barrier(NamePostgres), barrier(NameRedis), barrier(NameRabbitMQ), barrier("other")).
		WithTimeout(5 * time.Second)

	recorder, body := serve(t, handler)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "ready", body.Status)
	require.EqualValues(t, blocking, deadlines.Load(), "every check gets its own deadline")
}

func TestHungDependencyTimesOut(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	ignoresContext := func(context.Context) error { <-hang; return nil }

	handler := NewHandler(Postgres(ping(true)),
		Redis(ignoresContext),
		RabbitMQ(ignoresContext),
	).WithTimeout(50 * time.Millisecond)

	start := time.Now()
	recorder, body := serve(t, handler)
	require.Less(t, time.Since(start), 2*time.Second, "a hung ping does not hang /readyz")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, readyzBody{Status: "degraded", Dependencies: map[string]Status{
		NamePostgres: StatusOK, NameRedis: StatusDegraded, NameRabbitMQ: StatusDegraded}}, body)

	recorder, _ = serve(t, NewHandler(Postgres(ignoresContext)).WithTimeout(50*time.Millisecond))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "a hung Postgres is not ready")
}

func TestNewHandlerRejectsBadCheckers(t *testing.T) {
	pg := Postgres(ping(true))
	require.Panics(t, func() { NewHandler(nil) })
	require.Panics(t, func() { NewHandler(pg, nil) })
	require.Panics(t, func() { NewHandler(pg, fakeChecker{"", StatusOK}) })
	require.Panics(t, func() { NewHandler(pg, Redis(nil), Redis(nil)) }, "duplicate name")
	require.Panics(t, func() { NewHandler(pg, fakeChecker{NamePostgres, StatusOK}) }, "duplicate of postgres")
	require.Panics(t, func() { NewHandler(pg).WithTimeout(0) })
	require.Panics(t, func() { RabbitMQ(nil) })
	require.Panics(t, func() { CreditResults(nil) })
	require.Panics(t, func() { Breaker(NameSupplier, nil) })
}
