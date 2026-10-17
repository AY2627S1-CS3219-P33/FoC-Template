package walletrepo_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/walletrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testdb"
)

const (
	alice  = "00000000-0000-0000-0000-00000000a11c"
	bob    = "00000000-0000-0000-0000-0000000000b0"
	order1 = "00000000-0000-0000-0000-0000000000e1"
	order2 = "00000000-0000-0000-0000-0000000000e2"
)

func ptr(s string) *string { return &s }

func TestCreateAndReadWallet(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)

	created, err := walletrepo.CreateWallet(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)
	require.Equal(t, alice, created.OwnerID)
	require.Zero(t, created.Available)
	require.Zero(t, created.Reserved)
	require.Equal(t, int64(1), created.Version)

	byOwner, err := walletrepo.GetWalletByOwner(ctx, pool, alice)
	require.NoError(t, err)
	require.Equal(t, created.ID, byOwner.ID)
	byID, err := walletrepo.GetWallet(ctx, pool, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, byID.ID)

	require.Equal(t, "auth0|alice", created.Auth0Subject)

	bySubject, err := walletrepo.GetWalletByAuth0Subject(ctx, pool, "auth0|alice")
	require.NoError(t, err)
	require.Equal(t, created.ID, bySubject.ID)
	_, err = walletrepo.GetWalletByAuth0Subject(ctx, pool, "auth0|nobody")
	require.ErrorIs(t, err, walletrepo.ErrNotFound)

	_, err = walletrepo.CreateWallet(ctx, pool, alice, "auth0|other")
	require.ErrorIs(t, err, walletrepo.ErrAlreadyExists)
	_, err = walletrepo.CreateWallet(ctx, pool, bob, "auth0|alice")
	require.ErrorIs(t, err, walletrepo.ErrSubjectTaken)
	_, err = walletrepo.CreateWallet(ctx, pool, bob, " ")
	require.Error(t, err, "blank subject is rejected")
	_, err = walletrepo.GetWalletByOwner(ctx, pool, bob)
	require.ErrorIs(t, err, walletrepo.ErrNotFound)
}

func TestInsertWalletIfAbsentIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)

	first, created, err := walletrepo.InsertWalletIfAbsent(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)
	require.True(t, created)

	_, created, err = walletrepo.InsertWalletIfAbsent(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)
	require.False(t, created)
	_, created, err = walletrepo.InsertWalletIfAbsent(ctx, pool, alice, "auth0|different")
	require.NoError(t, err)
	require.False(t, created, "an existing owner changes nothing, whatever the subject")

	_, _, err = walletrepo.InsertWalletIfAbsent(ctx, pool, bob, "auth0|alice")
	require.ErrorIs(t, err, walletrepo.ErrSubjectTaken)

	got, err := walletrepo.GetWalletByOwner(ctx, pool, alice)
	require.NoError(t, err)
	require.Equal(t, first.ID, got.ID)
	require.Equal(t, "auth0|alice", got.Auth0Subject)
}

func TestApplyDeltasUpdatesBalancesAndVersion(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	w, err := walletrepo.CreateWallet(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)

	w, err = walletrepo.ApplyDeltas(ctx, pool, w.ID, 100, 0)
	require.NoError(t, err)
	require.Equal(t, int64(100), w.Available)
	require.Equal(t, int64(2), w.Version)

	w, err = walletrepo.ApplyDeltas(ctx, pool, w.ID, -30, 30)
	require.NoError(t, err)
	require.Equal(t, int64(70), w.Available)
	require.Equal(t, int64(30), w.Reserved)
}

func TestApplyDeltasRejectsNegativeBalances(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	w, err := walletrepo.CreateWallet(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)

	_, err = walletrepo.ApplyDeltas(ctx, pool, w.ID, -1, 0)
	require.ErrorIs(t, err, walletrepo.ErrNegativeBalance)
	_, err = walletrepo.ApplyDeltas(ctx, pool, w.ID, 0, -1)
	require.ErrorIs(t, err, walletrepo.ErrNegativeBalance)

	unchanged, err := walletrepo.GetWallet(ctx, pool, w.ID)
	require.NoError(t, err)
	require.Zero(t, unchanged.Available)
	require.Equal(t, int64(1), unchanged.Version)
}

func TestReservationLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	w, err := walletrepo.CreateWallet(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)

	r, err := walletrepo.CreateReservation(ctx, pool, w.ID, order1, 40)
	require.NoError(t, err)
	require.Equal(t, walletrepo.ReservationActive, r.Status)
	require.Equal(t, int64(40), r.Amount)

	_, err = walletrepo.CreateReservation(ctx, pool, w.ID, order1, 10)
	require.ErrorIs(t, err, walletrepo.ErrAlreadyExists)
	_, err = walletrepo.CreateReservation(ctx, pool, w.ID, order2, 0)
	require.Error(t, err, "amount must be positive")

	got, err := walletrepo.GetReservationByOrder(ctx, pool, order1, false)
	require.NoError(t, err)
	require.Equal(t, r.ID, got.ID)
	_, err = walletrepo.GetReservationByOrder(ctx, pool, order2, false)
	require.ErrorIs(t, err, walletrepo.ErrNotFound)

	r, err = walletrepo.UpdateReservation(ctx, pool, r.ID, 55, walletrepo.ReservationActive)
	require.NoError(t, err)
	require.Equal(t, int64(55), r.Amount)
	r, err = walletrepo.UpdateReservation(ctx, pool, r.ID, 55, walletrepo.ReservationTransferred)
	require.NoError(t, err)
	require.Equal(t, walletrepo.ReservationTransferred, r.Status)
}

func TestLedgerEntriesAreIdempotentAndAppendOnly(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	w, err := walletrepo.CreateWallet(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)

	entry := walletrepo.LedgerEntry{
		WalletID: w.ID, Type: walletrepo.EntryAllocate,
		AvailableDelta: 100, IdempotencyKey: "alloc-1",
	}
	first, err := walletrepo.InsertLedgerEntry(ctx, pool, entry)
	require.NoError(t, err)
	require.Nil(t, first.OrderID)

	_, err = walletrepo.InsertLedgerEntry(ctx, pool, entry)
	require.ErrorIs(t, err, walletrepo.ErrDuplicateOperation)
	replay, err := walletrepo.GetLedgerEntryByKey(ctx, pool, w.ID, "alloc-1")
	require.NoError(t, err)
	require.Equal(t, first.ID, replay.ID)

	// The same key is allowed on a different wallet (both halves of a transfer).
	other, err := walletrepo.CreateWallet(ctx, pool, bob, "auth0|bob")
	require.NoError(t, err)
	entry.WalletID = other.ID
	_, err = walletrepo.InsertLedgerEntry(ctx, pool, entry)
	require.NoError(t, err)

	_, err = walletrepo.InsertLedgerEntry(ctx, pool, walletrepo.LedgerEntry{
		WalletID: w.ID, Type: walletrepo.EntryAllocate, IdempotencyKey: "zero",
	})
	require.Error(t, err, "an entry must change a balance")

	_, err = pool.Exec(ctx, `UPDATE ledger_entries SET available_delta = 1`)
	require.ErrorContains(t, err, "append-only")
	_, err = pool.Exec(ctx, `DELETE FROM ledger_entries`)
	require.ErrorContains(t, err, "append-only")
}

func TestListLedgerEntriesPages(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	w, err := walletrepo.CreateWallet(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)
	for _, key := range []string{"a", "b", "c"} {
		_, err = walletrepo.InsertLedgerEntry(ctx, pool, walletrepo.LedgerEntry{
			WalletID: w.ID, Type: walletrepo.EntryAllocate, AvailableDelta: 1, IdempotencyKey: key,
		})
		require.NoError(t, err)
	}

	page1, err := walletrepo.ListLedgerEntries(ctx, pool, w.ID, 0, 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	page2, err := walletrepo.ListLedgerEntries(ctx, pool, w.ID, page1[1].ID, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Equal(t, "c", page2[0].IdempotencyKey)
}

func TestLockWalletsByOwner(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	a, err := walletrepo.CreateWallet(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)
	b, err := walletrepo.CreateWallet(ctx, pool, bob, "auth0|bob")
	require.NoError(t, err)

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Duplicates collapse, and the order asked for does not matter.
		locked, err := walletrepo.LockWalletsByOwner(ctx, tx, bob, alice, alice)
		require.NoError(t, err)
		require.Len(t, locked, 2)
		require.ElementsMatch(t, []string{a.ID, b.ID}, []string{locked[0].ID, locked[1].ID})
		require.Less(t, locked[0].ID, locked[1].ID)

		_, err = walletrepo.LockWalletsByOwner(ctx, tx, alice, "00000000-0000-0000-0000-00000000dead")
		require.ErrorIs(t, err, walletrepo.ErrNotFound)
		return nil
	})
	require.NoError(t, err)
}

// A reserve done as one transaction: wallet change, reservation and ledger
// entry commit together, and the ledger deltas add up to the balances.
func TestReserveInOneTransactionKeepsLedgerConsistent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	w, err := walletrepo.CreateWallet(ctx, pool, alice, "auth0|alice")
	require.NoError(t, err)
	_, err = walletrepo.ApplyDeltas(ctx, pool, w.ID, 100, 0)
	require.NoError(t, err)
	_, err = walletrepo.InsertLedgerEntry(ctx, pool, walletrepo.LedgerEntry{
		WalletID: w.ID, Type: walletrepo.EntryAllocate, AvailableDelta: 100, IdempotencyKey: "alloc",
	})
	require.NoError(t, err)

	reserve := func(amount int64, key string) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			locked, err := walletrepo.LockWalletsByOwner(ctx, tx, alice)
			if err != nil {
				return err
			}
			if _, err = walletrepo.InsertLedgerEntry(ctx, tx, walletrepo.LedgerEntry{
				WalletID: locked[0].ID, Type: walletrepo.EntryReserve,
				AvailableDelta: -amount, ReservedDelta: amount,
				OrderID: ptr(order1), IdempotencyKey: key,
			}); err != nil {
				return err
			}
			if _, err = walletrepo.ApplyDeltas(ctx, tx, locked[0].ID, -amount, amount); err != nil {
				return err
			}
			_, err = walletrepo.CreateReservation(ctx, tx, locked[0].ID, order1, amount)
			return err
		})
	}

	require.NoError(t, reserve(60, "reserve-1"))
	require.ErrorIs(t, reserve(10, "reserve-2"), walletrepo.ErrAlreadyExists, "order already reserved")
	require.ErrorIs(t, reserve(60, "reserve-3"), walletrepo.ErrNegativeBalance, "only 40 available")
	require.ErrorIs(t, reserve(60, "reserve-1"), walletrepo.ErrDuplicateOperation)

	got, err := walletrepo.GetWallet(ctx, pool, w.ID)
	require.NoError(t, err)
	require.Equal(t, int64(40), got.Available)
	require.Equal(t, int64(60), got.Reserved)

	var availableSum, reservedSum int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(available_delta),0), COALESCE(SUM(reserved_delta),0)
		 FROM ledger_entries WHERE wallet_id = $1::uuid`, w.ID).Scan(&availableSum, &reservedSum))
	require.Equal(t, got.Available, availableSum)
	require.Equal(t, got.Reserved, reservedSum)
}
