package wallet_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/walletrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testdb"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

const (
	alice = "00000000-0000-0000-0000-00000000a11c"
	bob   = "00000000-0000-0000-0000-0000000000b0"
)

func TestCreateWalletAllocatesInitialCreditOnce(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	svc := wallet.NewService(pool, 100)

	w, created, err := svc.CreateWallet(ctx, alice, "auth0|alice")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, int64(100), w.Available)
	require.Zero(t, w.Reserved)

	// Repeating the call returns the same wallet and gives no second allocation.
	again, created, err := svc.CreateWallet(ctx, alice, "auth0|alice")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, int64(100), again.Available)

	row, err := walletrepo.GetWalletByOwner(ctx, pool, alice)
	require.NoError(t, err)
	entries, err := walletrepo.ListLedgerEntries(ctx, pool, row.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, walletrepo.EntryAllocate, entries[0].Type)
	require.Equal(t, int64(100), entries[0].AvailableDelta)
	require.Equal(t, wallet.InitialAllocationKey, entries[0].IdempotencyKey)
}

func TestCreateWalletWithZeroInitialCreditWritesNoLedgerEntry(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)

	w, created, err := wallet.NewService(pool, 0).CreateWallet(ctx, alice, "auth0|alice")
	require.NoError(t, err)
	require.True(t, created)
	require.Zero(t, w.Available)

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries`).Scan(&count))
	require.Zero(t, count)
}

func TestCreateWalletConflicts(t *testing.T) {
	ctx := context.Background()
	svc := wallet.NewService(testdb.Pool(t), 100)
	_, _, err := svc.CreateWallet(ctx, alice, "auth0|alice")
	require.NoError(t, err)

	_, _, err = svc.CreateWallet(ctx, alice, "auth0|someone-else")
	require.ErrorIs(t, err, wallet.ErrConflict, "an owner's subject never changes")
	_, _, err = svc.CreateWallet(ctx, bob, "auth0|alice")
	require.ErrorIs(t, err, wallet.ErrConflict, "one identity cannot own two wallets")

	_, err = svc.GetByOwner(ctx, bob)
	require.ErrorIs(t, err, wallet.ErrNotFound, "a rejected create leaves nothing behind")
}

func TestCreateWalletValidatesInput(t *testing.T) {
	ctx := context.Background()
	svc := wallet.NewService(testdb.Pool(t), 100)
	for name, c := range map[string]struct{ owner, subject string }{
		"owner not a uuid": {"alice", "auth0|alice"},
		"empty owner":      {"", "auth0|alice"},
		"empty subject":    {alice, ""},
		"padded subject":   {alice, " auth0|alice"},
		"control chars":    {alice, "auth0|al\nice"},
	} {
		_, _, err := svc.CreateWallet(ctx, c.owner, c.subject)
		require.ErrorIs(t, err, wallet.ErrInvalid, name)
	}
}

func TestConcurrentCreatesAllocateOnce(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	svc := wallet.NewService(pool, 100)

	var wg sync.WaitGroup
	results := make(chan bool, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := svc.CreateWallet(ctx, alice, "auth0|alice")
			if err == nil {
				results <- created
			}
		}()
	}
	wg.Wait()
	close(results)

	creators := 0
	for created := range results {
		if created {
			creators++
		}
	}
	require.Equal(t, 1, creators, "exactly one request creates the wallet")

	got, err := svc.GetByOwner(ctx, alice)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.Available)
}

func TestReads(t *testing.T) {
	ctx := context.Background()
	svc := wallet.NewService(testdb.Pool(t), 100)
	_, _, err := svc.CreateWallet(ctx, alice, "auth0|alice")
	require.NoError(t, err)

	byOwner, err := svc.GetByOwner(ctx, alice)
	require.NoError(t, err)
	bySubject, err := svc.GetBySubject(ctx, "auth0|alice")
	require.NoError(t, err)
	require.Equal(t, byOwner, bySubject)

	_, err = svc.GetByOwner(ctx, bob)
	require.ErrorIs(t, err, wallet.ErrNotFound)
	_, err = svc.GetBySubject(ctx, "auth0|nobody")
	require.ErrorIs(t, err, wallet.ErrNotFound)
	_, err = svc.GetByOwner(ctx, "not-a-uuid")
	require.ErrorIs(t, err, wallet.ErrInvalid)
}
