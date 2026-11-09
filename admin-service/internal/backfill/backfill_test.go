package backfill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/orderclient"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/resilience"
)

const errandID = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"

var logger = slog.New(slog.NewTextHandler(io.Discard, nil))

func transition(id int64, eventType string, version int, from, to string) orderclient.Transition {
	return orderclient.Transition{
		ID: id, MessageID: fmt.Sprintf("%s:%s:%d", errandID, eventType[len("errand."):], version), Type: eventType,
		ErrandID: errandID, ErrandVersion: version, From: facts.Status(from), To: facts.Status(to), Reward: 8,
		OccurredAt: time.Date(2026, 3, 2, 9, version, 0, 0, time.UTC),
	}
}

// fakeSource serves items in pages of the requested size, like the export.
type fakeSource struct {
	items    []orderclient.Transition
	failures map[int]error // call number (1-based) → error
	calls    []string
	cursor   func(last int64) string
}

func (f *fakeSource) Transitions(_ context.Context, after string, limit int) (orderclient.TransitionPage, error) {
	f.calls = append(f.calls, after)
	if err := f.failures[len(f.calls)]; err != nil {
		return orderclient.TransitionPage{}, err
	}
	var afterID int64
	if after != "" {
		afterID, _ = strconv.ParseInt(after, 10, 64)
	}
	var page orderclient.TransitionPage
	for _, item := range f.items {
		if item.ID > afterID {
			if len(page.Items) == limit {
				page.NextCursor = strconv.FormatInt(page.Items[limit-1].ID, 10)
				if f.cursor != nil {
					page.NextCursor = f.cursor(page.Items[limit-1].ID)
				}
				break
			}
			page.Items = append(page.Items, item)
		}
	}
	return page, nil
}

type memoryRecorder struct {
	stored  map[string]facts.Event
	order   []string
	failOn  string
	failErr error
}

func (m *memoryRecorder) Apply(_ context.Context, e facts.Event) (bool, error) {
	if m.failOn == e.MessageID {
		return false, m.failErr
	}
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", facts.ErrInvalid, err)
	}
	if m.stored == nil {
		m.stored = map[string]facts.Event{}
	}
	if _, ok := m.stored[e.MessageID]; ok {
		return false, nil
	}
	m.stored[e.MessageID] = e
	m.order = append(m.order, e.MessageID)
	return true, nil
}

func lifecycle() []orderclient.Transition {
	return []orderclient.Transition{
		transition(10, "errand.placed", 1, "", "PENDING_RESERVATION"),
		transition(20, "errand.opened", 2, "PENDING_RESERVATION", "OPEN"),
		transition(30, "errand.accepted", 3, "OPEN", "ASSIGNED"),
		transition(40, "errand.picked_up", 4, "ASSIGNED", "PICKED_UP"),
		transition(50, "errand.delivered", 5, "PICKED_UP", "DELIVERED"),
		transition(60, "errand.completed", 6, "DELIVERED", "COMPLETED"),
	}
}

func TestRunLoadsEveryPageOldestFirst(t *testing.T) {
	source := &fakeSource{items: lifecycle()}
	recorder := &memoryRecorder{}

	result, err := Run(context.Background(), source, recorder, logger, Config{PageSize: 4})

	require.NoError(t, err)
	require.Equal(t, Result{Pages: 2, Events: 6, Applied: 6}, result)
	require.Equal(t, []string{"", "40"}, source.calls, "the second page continues after the first page's last ID")
	require.Equal(t, []string{
		errandID + ":placed:1", errandID + ":opened:2", errandID + ":accepted:3",
		errandID + ":picked_up:4", errandID + ":delivered:5", errandID + ":completed:6",
	}, recorder.order)
}

func TestRunTwiceChangesNothingTheSecondTime(t *testing.T) {
	source := &fakeSource{items: lifecycle()}
	recorder := &memoryRecorder{}

	first, err := Run(context.Background(), source, recorder, logger, Config{PageSize: 2})
	require.NoError(t, err)
	second, err := Run(context.Background(), source, recorder, logger, Config{PageSize: 2})
	require.NoError(t, err)

	require.Equal(t, 6, first.Applied)
	require.Equal(t, Result{Pages: 3, Events: 6, Applied: 0}, second, "every event was already there")
	require.Len(t, recorder.stored, 6)
}

func TestRunSkipsEventsTheStreamAlreadyBrought(t *testing.T) {
	source := &fakeSource{items: lifecycle()}
	recorder := &memoryRecorder{}
	for _, item := range lifecycle()[2:4] { // the stream delivered two of them
		_, err := recorder.Apply(context.Background(), item.FactsEvent())
		require.NoError(t, err)
	}

	result, err := Run(context.Background(), source, recorder, logger, Config{})

	require.NoError(t, err)
	require.Equal(t, Result{Pages: 1, Events: 6, Applied: 4}, result)
}

func TestRunEmptyExport(t *testing.T) {
	result, err := Run(context.Background(), &fakeSource{}, &memoryRecorder{}, logger, Config{})

	require.NoError(t, err)
	require.Equal(t, Result{Pages: 1}, result)
}

func TestRunRetriesWhileOrderServiceIsUnavailable(t *testing.T) {
	source := &fakeSource{items: lifecycle(), failures: map[int]error{
		1: fmt.Errorf("%w: status 503", orderclient.ErrUnavailable),
		2: fmt.Errorf("%w: status 503", orderclient.ErrUnavailable),
	}}
	var slept []time.Duration

	result, err := Run(context.Background(), source, &memoryRecorder{}, logger, Config{
		Backoff: resilience.BackoffPolicy{Base: time.Second, Cap: 4 * time.Second, Rand: func(n int64) int64 { return n - 1 }},
		Sleep:   func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
	})

	require.NoError(t, err)
	require.Equal(t, 6, result.Applied)
	require.Len(t, slept, 2, "two failures, two waits")
	require.Less(t, slept[0], slept[1]+1, "the wait grows")
}

func TestRunGivesUpAfterTheAttempts(t *testing.T) {
	down := fmt.Errorf("%w: status 503", orderclient.ErrUnavailable)
	source := &fakeSource{items: lifecycle(), failures: map[int]error{1: down, 2: down, 3: down}}

	result, err := Run(context.Background(), source, &memoryRecorder{}, logger, Config{
		Attempts: 3, Sleep: func(context.Context, time.Duration) error { return nil },
	})

	require.ErrorIs(t, err, orderclient.ErrUnavailable)
	require.ErrorContains(t, err, "giving up after 3 attempts")
	require.Zero(t, result.Events)
	require.Len(t, source.calls, 3)
}

func TestRunDoesNotRetryARefusal(t *testing.T) {
	source := &fakeSource{items: lifecycle(), failures: map[int]error{1: errors.New("orderclient: refused: status 403")}}

	_, err := Run(context.Background(), source, &memoryRecorder{}, logger, Config{})

	require.ErrorContains(t, err, "status 403")
	require.Len(t, source.calls, 1, "a 403 is a configuration problem, not an outage")
}

func TestRunStopsWhenCancelledDuringBackoff(t *testing.T) {
	source := &fakeSource{failures: map[int]error{1: orderclient.ErrUnavailable}}
	ctx, cancel := context.WithCancel(context.Background())

	_, err := Run(ctx, source, &memoryRecorder{}, logger, Config{
		Sleep: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() },
	})

	require.ErrorIs(t, err, context.Canceled)
}

func TestRunSkipsRefusedEventsAndReportsThem(t *testing.T) {
	items := lifecycle()
	items[2].To = "LOST" // an event the read model refuses
	recorder := &memoryRecorder{}

	result, err := Run(context.Background(), &fakeSource{items: items}, recorder, logger, Config{})

	require.ErrorIs(t, err, ErrRejected)
	require.Equal(t, Result{Pages: 1, Events: 6, Applied: 5, Rejected: 1}, result, "the rest were still loaded")
}

func TestRunStopsOnADatabaseFailure(t *testing.T) {
	recorder := &memoryRecorder{failOn: errandID + ":accepted:3", failErr: errors.New("connection reset")}

	result, err := Run(context.Background(), &fakeSource{items: lifecycle()}, recorder, logger, Config{})

	require.ErrorContains(t, err, "connection reset")
	require.Equal(t, 3, result.Events, "stopped at the failing event, so a rerun resumes cleanly")
}

func TestRunRefusesACursorThatDoesNotAdvance(t *testing.T) {
	source := &fakeSource{items: lifecycle(), cursor: func(int64) string { return "10" }}

	_, err := Run(context.Background(), source, &memoryRecorder{}, logger, Config{PageSize: 1})

	require.ErrorContains(t, err, "does not advance")
	require.LessOrEqual(t, len(source.calls), 3, "it does not loop")

	source = &fakeSource{items: lifecycle(), cursor: func(int64) string { return "abc" }}
	_, err = Run(context.Background(), source, &memoryRecorder{}, logger, Config{PageSize: 1})
	require.ErrorContains(t, err, "does not advance")
}

func TestRunValidatesItsArguments(t *testing.T) {
	_, err := Run(context.Background(), nil, &memoryRecorder{}, logger, Config{})
	require.Error(t, err)
	_, err = Run(context.Background(), &fakeSource{}, nil, logger, Config{})
	require.Error(t, err)
	_, err = Run(context.Background(), &fakeSource{}, &memoryRecorder{}, nil, Config{})
	require.Error(t, err)
	_, err = Run(context.Background(), &fakeSource{}, &memoryRecorder{}, logger, Config{PageSize: 501})
	require.Error(t, err)
	_, err = Run(context.Background(), &fakeSource{}, &memoryRecorder{}, logger, Config{Attempts: -1})
	require.Error(t, err)
}
