package reconciler

import (
	"context"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type lookup string

func (l lookup) GetByKey(_ context.Context, key string) (msgrepo.Record, error) {
	if key != "e:reserve" {
		panic("wrong key")
	}
	return msgrepo.Record{Status: string(l)}, nil
}
func TestRecovery(t *testing.T) {
	now := time.Now()
	for _, status := range []string{msgrepo.StatusPending, msgrepo.StatusSent, msgrepo.StatusFailed, msgrepo.StatusConfirmed} {
		t.Run(status, func(t *testing.T) {
			store := testfakes.NewFakeStore()
			store.Put(errand.Errand{ID: "e", Status: errand.StatusPendingReservation, Version: 1, RequesterID: "r", CreatedAt: now.Add(-6 * time.Minute), Details: errand.Details{Reward: 10}})
			runner, err := New(store, lookup(status), testfakes.NewFakeSettings(), testfakes.NewFakeClock(now), Config{})
			require.NoError(t, err)
			n, err := runner.RunOnce(context.Background(), errand.StuckReservation)
			require.NoError(t, err)
			if status == msgrepo.StatusSent || status == msgrepo.StatusFailed {
				require.Equal(t, 1, n)
				e, _ := store.Errand("e")
				require.Equal(t, errand.StatusRejected, e.Status)
				require.Len(t, store.OutboxKinds(errand.KindCreditRelease), 1)
			} else {
				require.Zero(t, n)
			}
		})
	}
	for _, age := range []time.Duration{6 * time.Minute, 4 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			store := testfakes.NewFakeStore()
			store.Put(errand.Errand{ID: "e", Status: errand.StatusOpen, Version: 2, RequesterID: "r", Details: errand.Details{Reward: 10}, PendingEdit: &errand.PendingEdit{At: now.Add(-age), Key: "e:adjust:2", Details: errand.Details{Reward: 20}}})
			runner, _ := New(store, lookup(""), testfakes.NewFakeSettings(), testfakes.NewFakeClock(now), Config{})
			n, err := runner.RunOnce(context.Background(), errand.StuckEdit)
			require.NoError(t, err)
			if age > 5*time.Minute {
				require.Equal(t, 1, n)
				e, _ := store.Errand("e")
				require.Nil(t, e.PendingEdit)
				rows := store.OutboxKinds(errand.KindCreditAdjust)
				require.Len(t, rows, 1)
				cmd := rows[0].Payload.(errand.CreditCommand)
				require.Equal(t, 10, cmd.NewAmount)
				require.Greater(t, cmd.Seq, errand.AdjustSeq(2))
			} else {
				require.Zero(t, n)
			}
		})
	}
}

type movedStore struct {
	errand.Store
	current  errand.Errand
	panicRun bool
	cutoff   time.Time
}

func (s *movedStore) ClaimDue(_ context.Context, _ errand.DueKind, cutoff time.Time, _ int, decide errand.DecideFunc) (int, error) {
	s.cutoff = cutoff
	if s.panicRun {
		panic("injected")
	}
	_, ok, err := decide(s.current)
	if ok {
		return 1, err
	}
	return 0, err
}
func TestMovedRowsSkippedAndPanicRecovered(t *testing.T) {
	now := time.Now()
	s := &movedStore{Store: testfakes.NewFakeStore(), current: errand.Errand{ID: "e", Status: errand.StatusOpen}}
	r, _ := New(s, lookup("SENT"), testfakes.NewFakeSettings(), testfakes.NewFakeClock(now), Config{})
	for _, kind := range []errand.DueKind{errand.StuckReservation, errand.StuckEdit} {
		n, err := r.RunOnce(context.Background(), kind)
		require.NoError(t, err)
		require.Zero(t, n)
		require.Equal(t, now.Add(-5*time.Minute), s.cutoff)
	}
	s.panicRun = true
	_, err := r.RunOnce(context.Background(), errand.StuckEdit)
	require.Error(t, err)
	require.EqualValues(t, 1, r.Stats(errand.StuckEdit).Panics)
	s.panicRun = false
	_, err = r.RunOnce(context.Background(), errand.StuckEdit)
	require.NoError(t, err)
}

func TestRunnerCancellation(t *testing.T) {
	r, err := New(testfakes.NewFakeStore(), lookup(""), testfakes.NewFakeSettings(), testfakes.NewFakeClock(time.Now()), Config{Interval: time.Millisecond})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return r.Stats(errand.StuckReservation).Runs > 0 && r.Stats(errand.StuckEdit).Runs > 0 }, time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}
