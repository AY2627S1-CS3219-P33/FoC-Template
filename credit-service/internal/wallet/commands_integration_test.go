package wallet_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testdb"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

const (
	order1 = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"
	order2 = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c11"

	aliceSub = "auth0|alice"
	bobSub   = "auth0|bob"
)

// newCommandService returns a service on an empty database with alice and bob,
// each holding 100 credits.
func newCommandService(t *testing.T) (*wallet.Service, context.Context) {
	t.Helper()
	ctx := context.Background()
	svc := wallet.NewService(testdb.Pool(t), 100)
	_, _, err := svc.CreateWallet(ctx, alice, aliceSub)
	require.NoError(t, err)
	_, _, err = svc.CreateWallet(ctx, bob, bobSub)
	require.NoError(t, err)
	return svc, ctx
}

func balances(t *testing.T, svc *wallet.Service, ctx context.Context, owner string) (available, reserved int64) {
	t.Helper()
	w, err := svc.GetByOwner(ctx, owner)
	require.NoError(t, err)
	return w.Available, w.Reserved
}

func reserve(t *testing.T, svc *wallet.Service, ctx context.Context, msg, order string, amount int64) wallet.Result {
	t.Helper()
	res, err := svc.Reserve(ctx, msg, wallet.ReserveCommand{OrderID: order, RequesterSubject: aliceSub, Amount: amount})
	require.NoError(t, err)
	return res
}

func adjust(t *testing.T, svc *wallet.Service, ctx context.Context, msg, order string, newAmount, seq int64) wallet.Result {
	t.Helper()
	res, err := svc.Adjust(ctx, msg, wallet.AdjustCommand{OrderID: order, RequesterSubject: aliceSub, NewAmount: newAmount, Seq: seq})
	require.NoError(t, err)
	return res
}

func release(t *testing.T, svc *wallet.Service, ctx context.Context, msg, order string) wallet.Result {
	t.Helper()
	res, err := svc.Release(ctx, msg, wallet.ReleaseCommand{OrderID: order, RequesterSubject: aliceSub})
	require.NoError(t, err)
	return res
}

func transfer(t *testing.T, svc *wallet.Service, ctx context.Context, msg, order string, amount int64) wallet.Result {
	t.Helper()
	res, err := svc.Transfer(ctx, msg, wallet.TransferCommand{
		OrderID: order, RequesterSubject: aliceSub, CourierSubject: bobSub, Amount: amount})
	require.NoError(t, err)
	return res
}

func TestReserveHoldsCredits(t *testing.T) {
	svc, ctx := newCommandService(t)

	res := reserve(t, svc, ctx, "E1:reserve", order1, 30)
	require.Equal(t, wallet.Result{Type: wallet.ResultReserved, Amount: 30}, res)

	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Equal(t, int64(30), reserved)
}

func TestReserveRefusals(t *testing.T) {
	svc, ctx := newCommandService(t)

	res := reserve(t, svc, ctx, "E1:reserve", order1, 101)
	require.Equal(t, wallet.Result{Type: wallet.ResultReservationRefused, Code: wallet.CodeInsufficientCredits}, res)

	res, err := svc.Reserve(ctx, "E1:reserve-2", wallet.ReserveCommand{OrderID: order1, RequesterSubject: "auth0|nobody", Amount: 5})
	require.NoError(t, err)
	require.Equal(t, wallet.Result{Type: wallet.ResultReservationRefused, Code: wallet.CodeAccountNotFound}, res)

	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(100), available)
	require.Zero(t, reserved)
}

func TestReserveUsesExactBalance(t *testing.T) {
	svc, ctx := newCommandService(t)
	require.Equal(t, wallet.ResultReserved, reserve(t, svc, ctx, "E1:reserve", order1, 100).Type)
	available, reserved := balances(t, svc, ctx, alice)
	require.Zero(t, available)
	require.Equal(t, int64(100), reserved)
}

func TestRepeatedCommandReplaysTheOriginalResult(t *testing.T) {
	svc, ctx := newCommandService(t)

	first := reserve(t, svc, ctx, "E1:reserve", order1, 30)
	again := reserve(t, svc, ctx, "E1:reserve", order1, 30)
	require.Equal(t, first, again)
	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Equal(t, int64(30), reserved)

	// A refusal is replayed too, even when the balance has changed since.
	refused := reserve(t, svc, ctx, "E2:reserve", order2, 90)
	require.Equal(t, wallet.ResultReservationRefused, refused.Type)
	require.Equal(t, wallet.ResultReleased, release(t, svc, ctx, "E1:release", order1).Type)
	require.Equal(t, refused, reserve(t, svc, ctx, "E2:reserve", order2, 90))
	available, _ = balances(t, svc, ctx, alice)
	require.Equal(t, int64(100), available, "the replayed refusal reserved nothing")
}

func TestConcurrentDeliveriesApplyOnce(t *testing.T) {
	svc, ctx := newCommandService(t)

	var wg sync.WaitGroup
	results := make(chan wallet.Result, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := svc.Reserve(ctx, "E1:reserve", wallet.ReserveCommand{OrderID: order1, RequesterSubject: aliceSub, Amount: 30})
			if err == nil {
				results <- res
			}
		}()
	}
	wg.Wait()
	close(results)
	for res := range results {
		require.Equal(t, wallet.Result{Type: wallet.ResultReserved, Amount: 30}, res)
	}
	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Equal(t, int64(30), reserved)
}

func TestAdjust(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)

	res := adjust(t, svc, ctx, "E1:adjust:2", order1, 50, 4)
	require.Equal(t, wallet.Result{Type: wallet.ResultAdjusted, Amount: 50, Seq: 4}, res)
	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(50), available)
	require.Equal(t, int64(50), reserved)

	res = adjust(t, svc, ctx, "E1:adjust-revert:3", order1, 30, 5)
	require.Equal(t, wallet.Result{Type: wallet.ResultAdjusted, Amount: 30, Seq: 5}, res)
	available, reserved = balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Equal(t, int64(30), reserved)
}

func TestAdjustWithSameAmountOnlyRecordsSeq(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	require.Equal(t, int64(30), adjust(t, svc, ctx, "a", order1, 30, 2).Amount)
	// seq 2 is applied, so an older seq is ignored.
	require.Equal(t, int64(30), adjust(t, svc, ctx, "b", order1, 60, 1).Amount)
}

func TestAdjustIgnoresAnOlderSeq(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)

	// The revert (seq 5) overtakes the edit (seq 4).
	require.Equal(t, int64(30), adjust(t, svc, ctx, "E1:adjust-revert:3", order1, 30, 5).Amount)
	late := adjust(t, svc, ctx, "E1:adjust:2", order1, 50, 4)
	require.Equal(t, wallet.Result{Type: wallet.ResultAdjusted, Amount: 30, Seq: 4}, late,
		"acknowledged with the current amount")

	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Equal(t, int64(30), reserved)
}

func TestAdjustRefusals(t *testing.T) {
	svc, ctx := newCommandService(t)

	res := adjust(t, svc, ctx, "a", order1, 10, 2)
	require.Equal(t, wallet.Result{Type: wallet.ResultAdjustRefused, Code: wallet.CodeReservationNotActive, Seq: 2}, res,
		"no reservation")

	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	res = adjust(t, svc, ctx, "b", order1, 131, 2)
	require.Equal(t, wallet.Result{Type: wallet.ResultAdjustRefused, Code: wallet.CodeInsufficientCredits, Seq: 2}, res)
	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Equal(t, int64(30), reserved)

	// A refused adjust does not use up its seq.
	require.Equal(t, int64(100), adjust(t, svc, ctx, "c", order1, 100, 2).Amount)

	release(t, svc, ctx, "E1:release", order1)
	res = adjust(t, svc, ctx, "d", order1, 10, 6)
	require.Equal(t, wallet.Result{Type: wallet.ResultAdjustRefused, Code: wallet.CodeReservationNotActive, Seq: 6}, res,
		"released reservation")
}

func TestRelease(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)

	require.Equal(t, wallet.Result{Type: wallet.ResultReleased}, release(t, svc, ctx, "E1:release", order1))
	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(100), available)
	require.Zero(t, reserved)

	// A different message for the same order changes nothing.
	require.Equal(t, wallet.ResultReleased, release(t, svc, ctx, "E1:release-again", order1).Type)
	available, _ = balances(t, svc, ctx, alice)
	require.Equal(t, int64(100), available)
}

func TestReleaseBeforeReserveLeavesATombstone(t *testing.T) {
	svc, ctx := newCommandService(t)

	require.Equal(t, wallet.ResultReleased, release(t, svc, ctx, "E1:release", order1).Type)
	res := reserve(t, svc, ctx, "E1:reserve", order1, 30)
	require.Equal(t, wallet.Result{Type: wallet.ResultReservationRefused, Code: wallet.CodeReservationReleased}, res)

	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(100), available)
	require.Zero(t, reserved)
}

func TestTransfer(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)

	require.Equal(t, wallet.Result{Type: wallet.ResultTransferred}, transfer(t, svc, ctx, "E1:transfer", order1, 30))
	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Zero(t, reserved)
	available, reserved = balances(t, svc, ctx, bob)
	require.Equal(t, int64(130), available)
	require.Zero(t, reserved)

	// A repeat, by the same or another message, moves nothing more.
	require.Equal(t, wallet.ResultTransferred, transfer(t, svc, ctx, "E1:transfer", order1, 30).Type)
	require.Equal(t, wallet.ResultTransferred, transfer(t, svc, ctx, "E1:transfer-again", order1, 30).Type)
	available, _ = balances(t, svc, ctx, bob)
	require.Equal(t, int64(130), available)
}

func TestTransferUsesTheAdjustedAmount(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	adjust(t, svc, ctx, "E1:adjust:2", order1, 50, 4)

	require.Equal(t, wallet.ResultTransferred, transfer(t, svc, ctx, "E1:transfer", order1, 50).Type)
	available, _ := balances(t, svc, ctx, bob)
	require.Equal(t, int64(150), available)
}

// An edit that order-service dropped has raised the reservation to 50, and
// the revert to 30 has not arrived when the transfer for 30 does.
func TestTransferPaysTheCommandAmountAndReleasesTheSurplus(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	adjust(t, svc, ctx, "E1:adjust:2", order1, 50, 4)

	require.Equal(t, wallet.Result{Type: wallet.ResultTransferred}, transfer(t, svc, ctx, "E1:transfer", order1, 30))
	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available, "100 - 30 paid; the other 20 came back")
	require.Zero(t, reserved)
	available, _ = balances(t, svc, ctx, bob)
	require.Equal(t, int64(130), available)

	// The late revert finds the reservation transferred and changes nothing.
	res := adjust(t, svc, ctx, "E1:adjust-revert:3", order1, 30, 5)
	require.Equal(t, wallet.Result{Type: wallet.ResultAdjustRefused, Code: wallet.CodeReservationNotActive, Seq: 5}, res)
	available, reserved = balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Zero(t, reserved)

	// A repeat of the transfer moves nothing more.
	require.Equal(t, wallet.ResultTransferred, transfer(t, svc, ctx, "E1:transfer", order1, 30).Type)
	available, _ = balances(t, svc, ctx, bob)
	require.Equal(t, int64(130), available)
}

func TestTransferFailures(t *testing.T) {
	svc, ctx := newCommandService(t)

	res := transfer(t, svc, ctx, "a", order1, 30)
	require.Equal(t, wallet.Result{Type: wallet.ResultOperationFailed, Code: wallet.CodeReservationNotFound}, res)

	reserve(t, svc, ctx, "E1:reserve", order1, 100)
	res = transfer(t, svc, ctx, "b", order1, 101)
	require.Equal(t, wallet.Result{Type: wallet.ResultOperationFailed, Code: wallet.CodeInsufficientCredits}, res,
		"1 credit short and nothing available")

	res, err := svc.Transfer(ctx, "c", wallet.TransferCommand{
		OrderID: order1, RequesterSubject: aliceSub, CourierSubject: "auth0|nobody", Amount: 30})
	require.NoError(t, err)
	require.Equal(t, wallet.Result{Type: wallet.ResultOperationFailed, Code: wallet.CodeAccountNotFound}, res)

	available, reserved := balances(t, svc, ctx, alice)
	require.Zero(t, available)
	require.Equal(t, int64(100), reserved, "failed transfers leave the reservation alone")
	available, _ = balances(t, svc, ctx, bob)
	require.Equal(t, int64(100), available)
}

// The errand is worth 30. An edit lowered the reservation to 20, the requester
// spent the freed credits, and the revert back to 30 was refused. The transfer
// for 30 then finds a reservation of 20.
func TestTransferTakesAShortfallFromAvailableCredits(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	adjust(t, svc, ctx, "E1:adjust:2", order1, 20, 4)
	available, _ := balances(t, svc, ctx, alice)
	require.Equal(t, int64(80), available)

	require.Equal(t, wallet.Result{Type: wallet.ResultTransferred}, transfer(t, svc, ctx, "E1:transfer", order1, 30))

	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available, "30 paid: 20 from the reservation and 10 from available")
	require.Zero(t, reserved)
	available, _ = balances(t, svc, ctx, bob)
	require.Equal(t, int64(130), available)
}

func TestTransferShortfallThatCannotBeCoveredFailsAndCanBeRetried(t *testing.T) {
	svc, ctx := newCommandService(t)
	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	adjust(t, svc, ctx, "E1:adjust:2", order1, 20, 4)
	// The requester spends the 80 credits that are now available.
	require.Equal(t, wallet.ResultReserved, reserve(t, svc, ctx, "E2:reserve", order2, 80).Type)
	res := adjust(t, svc, ctx, "E1:adjust-revert:3", order1, 30, 5)
	require.Equal(t, wallet.Result{Type: wallet.ResultAdjustRefused, Code: wallet.CodeInsufficientCredits, Seq: 5}, res)

	res = transfer(t, svc, ctx, "E1:transfer", order1, 30)
	require.Equal(t, wallet.Result{Type: wallet.ResultOperationFailed, Code: wallet.CodeInsufficientCredits}, res)
	available, reserved := balances(t, svc, ctx, alice)
	require.Zero(t, available)
	require.Equal(t, int64(100), reserved, "nothing moved")

	// The cause is fixed: the other errand is released. An administrator sends
	// the same command again, and it is judged anew, not answered from memory.
	release(t, svc, ctx, "E2:release", order2)
	require.Equal(t, wallet.Result{Type: wallet.ResultTransferred}, transfer(t, svc, ctx, "E1:transfer", order1, 30))
	available, reserved = balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Zero(t, reserved)
	available, _ = balances(t, svc, ctx, bob)
	require.Equal(t, int64(130), available)
}

func TestFailedResultIsNotRemembered(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	svc := wallet.NewService(pool, 100)
	_, _, err := svc.CreateWallet(ctx, alice, aliceSub)
	require.NoError(t, err)

	require.Equal(t, wallet.ResultOperationFailed, transfer2(t, svc, ctx, "x").Type)
	reserve(t, svc, ctx, "E1:reserve", order1, 10)
	var stored int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM processed_commands WHERE message_id = 'x'`).Scan(&stored))
	require.Zero(t, stored)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM processed_commands WHERE message_id = 'E1:reserve'`).Scan(&stored))
	require.Equal(t, 1, stored, "other results are remembered")
}

// transfer2 sends a transfer to a courier who has no wallet.
func transfer2(t *testing.T, svc *wallet.Service, ctx context.Context, msg string) wallet.Result {
	t.Helper()
	res, err := svc.Transfer(ctx, msg, wallet.TransferCommand{
		OrderID: order1, RequesterSubject: aliceSub, CourierSubject: "auth0|nobody", Amount: 5})
	require.NoError(t, err)
	return res
}

func TestReleaseAndTransferExcludeEachOther(t *testing.T) {
	svc, ctx := newCommandService(t)

	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	release(t, svc, ctx, "E1:release", order1)
	res := transfer(t, svc, ctx, "E1:transfer", order1, 30)
	require.Equal(t, wallet.Result{Type: wallet.ResultOperationFailed, Code: wallet.CodeReservationAlreadyReleased}, res)

	reserve(t, svc, ctx, "E2:reserve", order2, 30)
	transfer(t, svc, ctx, "E2:transfer", order2, 30)
	res = release(t, svc, ctx, "E2:release", order2)
	require.Equal(t, wallet.Result{Type: wallet.ResultOperationFailed, Code: wallet.CodeReservationAlreadyTransferred}, res)

	available, reserved := balances(t, svc, ctx, alice)
	require.Equal(t, int64(70), available)
	require.Zero(t, reserved)

	// The tombstone also covers a transfer that arrives for an unknown order.
	release(t, svc, ctx, "E3:release", "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c12")
	res = transfer(t, svc, ctx, "E3:transfer", "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c12", 5)
	require.Equal(t, wallet.CodeReservationAlreadyReleased, res.Code)
}

func TestInvalidCommandsReturnErrors(t *testing.T) {
	svc, ctx := newCommandService(t)
	ok := wallet.ReserveCommand{OrderID: order1, RequesterSubject: aliceSub, Amount: 5}

	for name, c := range map[string]wallet.ReserveCommand{
		"order not a uuid": {OrderID: "E1", RequesterSubject: aliceSub, Amount: 5},
		"empty subject":    {OrderID: order1, Amount: 5},
		"zero amount":      {OrderID: order1, RequesterSubject: aliceSub},
		"negative amount":  {OrderID: order1, RequesterSubject: aliceSub, Amount: -5},
	} {
		_, err := svc.Reserve(ctx, "m", c)
		require.ErrorIs(t, err, wallet.ErrInvalid, name)
	}
	_, err := svc.Reserve(ctx, "", ok)
	require.ErrorIs(t, err, wallet.ErrInvalid, "empty message id")

	_, err = svc.Adjust(ctx, "m", wallet.AdjustCommand{OrderID: order1, RequesterSubject: aliceSub, NewAmount: 5})
	require.ErrorIs(t, err, wallet.ErrInvalid, "missing seq")
	_, err = svc.Transfer(ctx, "m", wallet.TransferCommand{OrderID: order1, RequesterSubject: aliceSub, CourierSubject: aliceSub, Amount: 5})
	require.ErrorIs(t, err, wallet.ErrInvalid, "courier is requester")

	// A command for someone else's reservation is a conflict, not a result.
	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	_, err = svc.Release(ctx, "x", wallet.ReleaseCommand{OrderID: order1, RequesterSubject: bobSub})
	require.ErrorIs(t, err, wallet.ErrConflict)
	_, err = svc.GetByOwner(ctx, alice)
	require.NoError(t, err)
}

// TestLedgerMatchesBalances checks invariants 2, 4 and 6 of
// docs/wallet-aggregate.md after a mix of commands.
func TestLedgerMatchesBalances(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)
	svc := wallet.NewService(pool, 100)
	for owner, sub := range map[string]string{alice: aliceSub, bob: bobSub} {
		_, _, err := svc.CreateWallet(ctx, owner, sub)
		require.NoError(t, err)
	}

	reserve(t, svc, ctx, "E1:reserve", order1, 30)
	adjust(t, svc, ctx, "E1:adjust:2", order1, 45, 4)
	transfer(t, svc, ctx, "E1:transfer", order1, 45)
	reserve(t, svc, ctx, "E2:reserve", order2, 20)
	release(t, svc, ctx, "E2:release", order2)
	reserve(t, svc, ctx, "E3:reserve", "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c12", 10)
	reserve(t, svc, ctx, "E4:reserve", "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c13", 20)
	adjust(t, svc, ctx, "E4:adjust:2", "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c13", 25, 4)
	transfer(t, svc, ctx, "E4:transfer", "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c13", 20) // surplus of 5
	reserve(t, svc, ctx, "E5:reserve", "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c14", 10)
	transfer(t, svc, ctx, "E5:transfer", "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c14", 14) // shortfall of 4

	var mismatched int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM wallets w WHERE
		  w.available <> (SELECT COALESCE(SUM(available_delta), 0) FROM ledger_entries WHERE wallet_id = w.id)
		  OR w.reserved <> (SELECT COALESCE(SUM(reserved_delta), 0) FROM ledger_entries WHERE wallet_id = w.id)
		  OR w.reserved <> (SELECT COALESCE(SUM(amount), 0) FROM reservations WHERE wallet_id = w.id AND status = 'ACTIVE')
	`).Scan(&mismatched))
	require.Zero(t, mismatched)

	var total int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT SUM(available + reserved) FROM wallets`).Scan(&total))
	require.Equal(t, int64(200), total, "credits are conserved")

	// Every entry that is not an allocation moves credits between buckets or
	// wallets and creates none: all deltas together equal what was allocated.
	var created, allocated int64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(available_delta + reserved_delta), 0),
		       COALESCE(SUM(available_delta + reserved_delta) FILTER (WHERE entry_type = 'ALLOCATE'), 0)
		FROM ledger_entries`).Scan(&created, &allocated))
	require.Equal(t, allocated, created, "only the allocation creates credits")

	// Each order's transfer entries cancel out across the two wallets.
	var unbalanced int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM (
		  SELECT order_id FROM ledger_entries
		  WHERE entry_type IN ('TRANSFER_OUT', 'TRANSFER_IN')
		  GROUP BY order_id HAVING SUM(available_delta + reserved_delta) <> 0) bad`).Scan(&unbalanced))
	require.Zero(t, unbalanced)
}
