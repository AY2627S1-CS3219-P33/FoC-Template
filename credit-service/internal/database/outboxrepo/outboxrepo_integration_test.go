package outboxrepo_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/outboxrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testdb"
)

func enqueue(t *testing.T, pool *pgxpool.Pool, n int) []string {
	t.Helper()
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("E%d:reserve:credit.reserved", i)
		require.NoError(t, outboxrepo.Enqueue(context.Background(), pool, keys[i], "credit.reserved", []byte(`{"n":1}`)))
	}
	return keys
}

type state struct {
	Status   string
	Attempts int
	Owner    *string
	Failure  *string
	Sent     bool
}

func rowState(t *testing.T, pool *pgxpool.Pool, key string) state {
	t.Helper()
	var s state
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status, attempts, lease_owner, last_failure_code, sent_at IS NOT NULL
		 FROM outbox WHERE idempotency_key = $1`, key).Scan(&s.Status, &s.Attempts, &s.Owner, &s.Failure, &s.Sent))
	return s
}

func TestClaimLeasesDueRowsOnce(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	keys := enqueue(t, pool, 3)

	claimed, err := outboxrepo.Claim(ctx, pool, "w1", 2, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 2, "limited to the batch size")
	require.Equal(t, keys[0], claimed[0].Key, "oldest first")
	require.Equal(t, 1, claimed[0].Attempts)
	require.Equal(t, "credit.reserved", claimed[0].RoutingKey)
	require.JSONEq(t, `{"n":1}`, string(claimed[0].Payload))
	s := rowState(t, pool, keys[0])
	require.Equal(t, "w1", *s.Owner)

	// Leased rows are not claimed again; the third row is.
	again, err := outboxrepo.Claim(ctx, pool, "w2", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, again, 1)
	require.Equal(t, keys[2], again[0].Key)
}

func TestClaimTakesARowBackAfterItsLeaseExpires(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	keys := enqueue(t, pool, 1)
	_, err := outboxrepo.Claim(ctx, pool, "dead-worker", 10, time.Minute)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE outbox SET lease_expires_at = clock_timestamp() - interval '1 second'`)
	require.NoError(t, err)

	claimed, err := outboxrepo.Claim(ctx, pool, "w2", 10, time.Minute)

	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, 2, claimed[0].Attempts)
	require.Equal(t, "w2", *rowState(t, pool, keys[0]).Owner)

	// The dead worker cannot change the row any more.
	applied, err := outboxrepo.MarkSent(ctx, pool, claimed[0].ID, "dead-worker")
	require.NoError(t, err)
	require.False(t, applied)
}

func TestClaimSkipsRowsNotDue(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	keys := enqueue(t, pool, 4)
	for i, update := range []string{
		`next_attempt_at = clock_timestamp() + interval '1 hour'`,
		`status = 'SENT'`,
		`status = 'FAILED'`,
	} {
		_, err := pool.Exec(ctx, `UPDATE outbox SET `+update+` WHERE idempotency_key = $1`, keys[i])
		require.NoError(t, err)
	}

	claimed, err := outboxrepo.Claim(ctx, pool, "w1", 10, time.Minute)

	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, keys[3], claimed[0].Key)
}

func TestConcurrentClaimsNeverShareARow(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	enqueue(t, pool, 60)

	var mu sync.Mutex
	seen := map[int64]string{}
	var wg sync.WaitGroup
	for w := range 6 {
		worker := fmt.Sprintf("w%d", w)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				claimed, err := outboxrepo.Claim(ctx, pool, worker, 5, time.Minute)
				if err != nil || len(claimed) == 0 {
					return
				}
				mu.Lock()
				for _, r := range claimed {
					if other, dup := seen[r.ID]; dup {
						t.Errorf("row %d claimed by %s and %s", r.ID, other, worker)
					}
					seen[r.ID] = worker
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	require.Len(t, seen, 60, "every row was claimed")
}

func TestOutcomesNeedTheLease(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	keys := enqueue(t, pool, 3)
	claimed, err := outboxrepo.Claim(ctx, pool, "w1", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 3)

	applied, err := outboxrepo.MarkSent(ctx, pool, claimed[0].ID, "other")
	require.NoError(t, err)
	require.False(t, applied, "not the lease owner")
	require.Equal(t, "PENDING", rowState(t, pool, keys[0]).Status)

	applied, err = outboxrepo.MarkSent(ctx, pool, claimed[0].ID, "w1")
	require.NoError(t, err)
	require.True(t, applied)
	s := rowState(t, pool, keys[0])
	require.Equal(t, "SENT", s.Status)
	require.True(t, s.Sent)
	require.Nil(t, s.Owner)

	applied, err = outboxrepo.Reschedule(ctx, pool, claimed[1].ID, "w1", time.Hour, "UNROUTABLE")
	require.NoError(t, err)
	require.True(t, applied)
	s = rowState(t, pool, keys[1])
	require.Equal(t, "PENDING", s.Status)
	require.Equal(t, "UNROUTABLE", *s.Failure)
	require.Nil(t, s.Owner)
	none, err := outboxrepo.Claim(ctx, pool, "w1", 10, time.Minute)
	require.NoError(t, err)
	require.Empty(t, none, "the rescheduled row is not due for an hour")

	applied, err = outboxrepo.MarkFailed(ctx, pool, claimed[2].ID, "w1", "TIMEOUT")
	require.NoError(t, err)
	require.True(t, applied)
	s = rowState(t, pool, keys[2])
	require.Equal(t, "FAILED", s.Status)
	require.Equal(t, "TIMEOUT", *s.Failure)

	// A finished row cannot be finished again.
	applied, err = outboxrepo.MarkFailed(ctx, pool, claimed[0].ID, "w1", "X")
	require.NoError(t, err)
	require.False(t, applied)
}

func TestEnqueueBringsAFailedRowBack(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	keys := enqueue(t, pool, 1)
	claimed, err := outboxrepo.Claim(ctx, pool, "w1", 10, time.Minute)
	require.NoError(t, err)
	_, err = outboxrepo.MarkFailed(ctx, pool, claimed[0].ID, "w1", "UNROUTABLE")
	require.NoError(t, err)

	require.NoError(t, outboxrepo.Enqueue(ctx, pool, keys[0], "credit.reserved", []byte(`{"n":2}`)))

	s := rowState(t, pool, keys[0])
	require.Equal(t, "PENDING", s.Status)
	require.Zero(t, s.Attempts)
	require.Nil(t, s.Failure)
	again, err := outboxrepo.Claim(ctx, pool, "w1", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, again, 1)
	require.JSONEq(t, `{"n":1}`, string(again[0].Payload), "the original payload is kept")
}

func TestRoutingKeyIsRestricted(t *testing.T) {
	pool := testdb.Pool(t)
	err := outboxrepo.Enqueue(context.Background(), pool, "k", "credit.reserve", []byte(`{}`))
	require.Error(t, err, "a command key is not a result")
}
