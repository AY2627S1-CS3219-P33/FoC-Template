package wallet_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/walletrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testdb"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

// LockWalletsByAuth0Subject orders its locks by wallet ID in SQL, so every
// transaction takes wallet locks in one global order. These tests try to
// provoke a deadlock (PostgreSQL error 40P01) and fail if one happens.

func orderID(i int) string { return fmt.Sprintf("6f1c2b0e-8d4a-4e1b-9c3f-%012d", i) }

// Transfers in opposite directions between the same two wallets, all at once.
func TestOppositeTransfersDoNotDeadlock(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	svc := wallet.NewService(pool, 10000)
	_, _, err := svc.CreateWallet(ctx, alice, aliceSub)
	require.NoError(t, err)
	_, _, err = svc.CreateWallet(ctx, bob, bobSub)
	require.NoError(t, err)

	const perDirection = 100
	for i := 0; i < perDirection; i++ {
		for j, sub := range []string{aliceSub, bobSub} {
			id := orderID(i*2 + j)
			res, err := svc.Reserve(ctx, "reserve:"+id, wallet.ReserveCommand{OrderID: id, RequesterSubject: sub, Amount: 1})
			require.NoError(t, err)
			require.Equal(t, wallet.ResultReserved, res.Type)
		}
	}

	start := make(chan struct{})
	errs := make(chan error, 2*perDirection)
	var wg sync.WaitGroup
	for i := 0; i < perDirection; i++ {
		for j, pair := range [][2]string{{aliceSub, bobSub}, {bobSub, aliceSub}} {
			id := orderID(i*2 + j)
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				res, err := svc.Transfer(ctx, "transfer:"+id, wallet.TransferCommand{
					OrderID: id, RequesterSubject: pair[0], CourierSubject: pair[1], Amount: 1})
				if err == nil && res.Type != wallet.ResultTransferred {
					err = fmt.Errorf("unexpected result %+v", res)
				}
				errs <- err
			}()
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	a, b := mustBalances(t, svc, ctx, alice), mustBalances(t, svc, ctx, bob)
	require.EqualValues(t, 20000, a+b, "credits are conserved")
}

func mustBalances(t *testing.T, svc *wallet.Service, ctx context.Context, owner string) int64 {
	t.Helper()
	w, err := svc.GetByOwner(ctx, owner)
	require.NoError(t, err)
	require.Zero(t, w.Reserved)
	return w.Available
}

// Two transactions ask for the same two wallets in opposite key orders, and
// each holds its locks long enough to overlap. The order of the keys must not
// decide the order of the locks.
func TestLockOrderDoesNotDependOnKeyOrder(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	svc := wallet.NewService(pool, 100)
	_, _, err := svc.CreateWallet(ctx, alice, aliceSub)
	require.NoError(t, err)
	_, _, err = svc.CreateWallet(ctx, bob, bobSub)
	require.NoError(t, err)

	for round := 0; round < 50; round++ {
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		run := func(subjects ...string) {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := walletrepo.LockWalletsByAuth0Subject(ctx, tx, subjects...); err != nil {
				errs <- err
				return
			}
			_, err = tx.Exec(ctx, `SELECT pg_sleep(0.005)`)
			errs <- err
		}
		wg.Add(2)
		go run(aliceSub, bobSub)
		go run(bobSub, aliceSub)
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err, "round %d", round)
		}
	}
}
