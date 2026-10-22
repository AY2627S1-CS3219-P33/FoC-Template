package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/walletrepo"
)

// Order commands. order-service sends them as RabbitMQ messages; this file
// holds the rules and knows nothing about the broker. In order-service an
// order is an "errand", and users are identified by Auth0 subject.
//
// Every command takes the message ID of the message that carried it and is
// idempotent on it: a repeat returns the stored result and changes nothing.
// A business refusal is a Result, not an error. An error means the command
// could not be judged (bad input, conflicting data, database failure).

// Result types. They equal the routing keys of order-service's credit results.
const (
	ResultReserved           = "credit.reserved"
	ResultReservationRefused = "credit.reservation_refused"
	ResultAdjusted           = "credit.adjusted"
	ResultAdjustRefused      = "credit.adjust_refused"
	ResultReleased           = "credit.released"
	ResultTransferred        = "credit.transferred"
	ResultOperationFailed    = "credit.operation_failed"
)

// Codes in Result.Code. The first five are in the order-service contract.
const (
	CodeInsufficientCredits           = "INSUFFICIENT_CREDITS"
	CodeAccountNotFound               = "ACCOUNT_NOT_FOUND"
	CodeReservationReleased           = "RESERVATION_RELEASED"
	CodeReservationNotActive          = "RESERVATION_NOT_ACTIVE"
	CodeReservationAlreadyTransferred = "RESERVATION_ALREADY_TRANSFERRED"
	CodeReservationAlreadyReleased    = "RESERVATION_ALREADY_RELEASED"
	// CodeReservationNotFound only appears in credit.operation_failed, for a
	// state the contract calls impossible.
	CodeReservationNotFound = "RESERVATION_NOT_FOUND"
)

const maxMessageIDLength = 512

// Result is the outcome of a command, and the data of the result message.
type Result struct {
	Type string `json:"type"`
	// Amount is the reservation's amount after the command (reserved,
	// adjusted).
	Amount int64 `json:"amount,omitempty"`
	// Seq echoes the adjust's seq (adjusted, adjust_refused).
	Seq  int64  `json:"seq,omitempty"`
	Code string `json:"code,omitempty"`
}

// Every command has an ErrandVersion. It does not affect the rules; it is
// copied into the result message, which order-service matches to the errand.
// It is zero for a command that did not come from a message.

// Reply says which command a result answers. The ResultRecorder uses it to
// build the result message.
type Reply struct {
	CommandMessageID string
	OrderID          string
	ErrandVersion    int64
}

// ResultRecorder stores a command's result for delivery, inside the
// transaction that applied the command, so a balance change and its result
// message commit together or not at all. It is called for a repeated command
// too, with the stored result.
type ResultRecorder interface {
	RecordResult(ctx context.Context, tx pgx.Tx, reply Reply, result Result) error
}

type ReserveCommand struct {
	OrderID          string
	ErrandVersion    int64
	RequesterSubject string
	Amount           int64
}

type AdjustCommand struct {
	OrderID          string
	ErrandVersion    int64
	RequesterSubject string
	// NewAmount is the absolute amount the reservation should hold.
	NewAmount int64
	// Seq orders adjusts for one order; an adjust that is not newer than the
	// last applied one is acknowledged and ignored.
	Seq int64
}

type ReleaseCommand struct {
	OrderID          string
	ErrandVersion    int64
	RequesterSubject string
}

type TransferCommand struct {
	OrderID          string
	ErrandVersion    int64
	RequesterSubject string
	CourierSubject   string
	Amount           int64
}

// Reserve holds credits from the requester's available balance for an order.
func (s *Service) Reserve(ctx context.Context, messageID string, c ReserveCommand) (Result, error) {
	orderID, err := validateOrderID(c.OrderID)
	if err != nil {
		return Result{}, err
	}
	if err := validateSubject(c.RequesterSubject); err != nil {
		return Result{}, err
	}
	if c.Amount <= 0 {
		return Result{}, invalid("amount must be positive")
	}
	return s.execute(ctx, messageID, Reply{OrderID: orderID, ErrandVersion: c.ErrandVersion}, func(tx pgx.Tx) (Result, error) {
		released, err := walletrepo.IsOrderReleased(ctx, tx, orderID)
		if err != nil {
			return Result{}, err
		}
		if released {
			return Result{Type: ResultReservationRefused, Code: CodeReservationReleased}, nil
		}
		existing, err := walletrepo.GetReservationByOrder(ctx, tx, orderID, false)
		if err == nil {
			return Result{Type: ResultReserved, Amount: existing.Amount}, nil
		}
		if !errors.Is(err, walletrepo.ErrNotFound) {
			return Result{}, err
		}

		wallets, err := walletrepo.LockWalletsByAuth0Subject(ctx, tx, c.RequesterSubject)
		if errors.Is(err, walletrepo.ErrNotFound) {
			return Result{Type: ResultReservationRefused, Code: CodeAccountNotFound}, nil
		}
		if err != nil {
			return Result{}, err
		}
		w := wallets[0]
		if w.Available < c.Amount {
			return Result{Type: ResultReservationRefused, Code: CodeInsufficientCredits}, nil
		}
		if _, err := walletrepo.InsertLedgerEntry(ctx, tx, walletrepo.LedgerEntry{
			WalletID:       w.ID,
			Type:           walletrepo.EntryReserve,
			AvailableDelta: -c.Amount,
			ReservedDelta:  c.Amount,
			OrderID:        &orderID,
			IdempotencyKey: "reserve:" + orderID,
		}); err != nil {
			return Result{}, err
		}
		if _, err := walletrepo.ApplyDeltas(ctx, tx, w.ID, -c.Amount, c.Amount); err != nil {
			return Result{}, err
		}
		if _, err := walletrepo.CreateReservation(ctx, tx, w.ID, orderID, c.Amount); err != nil {
			return Result{}, err
		}
		return Result{Type: ResultReserved, Amount: c.Amount}, nil
	})
}

// Adjust changes the amount of an active reservation to c.NewAmount.
func (s *Service) Adjust(ctx context.Context, messageID string, c AdjustCommand) (Result, error) {
	orderID, err := validateOrderID(c.OrderID)
	if err != nil {
		return Result{}, err
	}
	if err := validateSubject(c.RequesterSubject); err != nil {
		return Result{}, err
	}
	if c.NewAmount <= 0 {
		return Result{}, invalid("new amount must be positive")
	}
	if c.Seq <= 0 {
		return Result{}, invalid("seq must be positive")
	}
	refused := func(code string) Result {
		return Result{Type: ResultAdjustRefused, Code: code, Seq: c.Seq}
	}
	return s.execute(ctx, messageID, Reply{OrderID: orderID, ErrandVersion: c.ErrandVersion}, func(tx pgx.Tx) (Result, error) {
		res, w, err := lockReservation(ctx, tx, orderID, c.RequesterSubject)
		if errors.Is(err, walletrepo.ErrNotFound) {
			return refused(CodeReservationNotActive), nil
		}
		if err != nil {
			return Result{}, err
		}
		if res.Status != walletrepo.ReservationActive {
			return refused(CodeReservationNotActive), nil
		}
		if c.Seq <= res.LastAdjustSeq {
			return Result{Type: ResultAdjusted, Amount: res.Amount, Seq: c.Seq}, nil
		}
		delta := c.NewAmount - res.Amount
		if delta > w.Available {
			return refused(CodeInsufficientCredits), nil
		}
		if delta != 0 {
			if _, err := walletrepo.InsertLedgerEntry(ctx, tx, walletrepo.LedgerEntry{
				WalletID:       w.ID,
				Type:           walletrepo.EntryReservationAdjust,
				AvailableDelta: -delta,
				ReservedDelta:  delta,
				OrderID:        &orderID,
				IdempotencyKey: fmt.Sprintf("adjust:%s:%d", orderID, c.Seq),
			}); err != nil {
				return Result{}, err
			}
			if _, err := walletrepo.ApplyDeltas(ctx, tx, w.ID, -delta, delta); err != nil {
				return Result{}, err
			}
		}
		if _, err := walletrepo.AdjustReservation(ctx, tx, res.ID, c.NewAmount, c.Seq); err != nil {
			return Result{}, err
		}
		return Result{Type: ResultAdjusted, Amount: c.NewAmount, Seq: c.Seq}, nil
	})
}

// Release returns the reservation to the requester's available balance. A
// release for an order with no reservation leaves a tombstone, so a reserve
// that arrives later is refused.
func (s *Service) Release(ctx context.Context, messageID string, c ReleaseCommand) (Result, error) {
	orderID, err := validateOrderID(c.OrderID)
	if err != nil {
		return Result{}, err
	}
	if err := validateSubject(c.RequesterSubject); err != nil {
		return Result{}, err
	}
	return s.execute(ctx, messageID, Reply{OrderID: orderID, ErrandVersion: c.ErrandVersion}, func(tx pgx.Tx) (Result, error) {
		res, w, err := lockReservation(ctx, tx, orderID, c.RequesterSubject)
		if errors.Is(err, walletrepo.ErrNotFound) {
			if err := walletrepo.InsertReleasedOrder(ctx, tx, orderID); err != nil {
				return Result{}, err
			}
			return Result{Type: ResultReleased}, nil
		}
		if err != nil {
			return Result{}, err
		}
		switch res.Status {
		case walletrepo.ReservationReleased:
			return Result{Type: ResultReleased}, nil
		case walletrepo.ReservationTransferred:
			return Result{Type: ResultOperationFailed, Code: CodeReservationAlreadyTransferred}, nil
		}
		if _, err := walletrepo.InsertLedgerEntry(ctx, tx, walletrepo.LedgerEntry{
			WalletID:       w.ID,
			Type:           walletrepo.EntryRelease,
			AvailableDelta: res.Amount,
			ReservedDelta:  -res.Amount,
			OrderID:        &orderID,
			IdempotencyKey: "release:" + orderID,
		}); err != nil {
			return Result{}, err
		}
		if _, err := walletrepo.ApplyDeltas(ctx, tx, w.ID, res.Amount, -res.Amount); err != nil {
			return Result{}, err
		}
		if _, err := walletrepo.UpdateReservation(ctx, tx, res.ID, res.Amount, walletrepo.ReservationReleased); err != nil {
			return Result{}, err
		}
		return Result{Type: ResultReleased}, nil
	})
}

// Transfer pays the courier c.Amount. The amount in the command is the
// errand's reward in order-service, so it is the amount to pay. It comes from
// the reservation:
//   - If the reservation holds more (an adjust that order-service dropped has
//     not been reverted yet), the surplus goes back to the requester's
//     available balance.
//   - If it holds less (a revert was refused because the requester had spent
//     the credits an earlier, lower adjust freed), the shortfall is taken from
//     the requester's available balance. If that cannot cover it, nothing
//     moves and the result is credit.operation_failed with
//     INSUFFICIENT_CREDITS.
//
// Both wallets change in one transaction.
func (s *Service) Transfer(ctx context.Context, messageID string, c TransferCommand) (Result, error) {
	orderID, err := validateOrderID(c.OrderID)
	if err != nil {
		return Result{}, err
	}
	if err := validateSubject(c.RequesterSubject); err != nil {
		return Result{}, err
	}
	if err := validateSubject(c.CourierSubject); err != nil {
		return Result{}, err
	}
	if c.CourierSubject == c.RequesterSubject {
		return Result{}, invalid("courier and requester must differ")
	}
	if c.Amount <= 0 {
		return Result{}, invalid("amount must be positive")
	}
	failed := func(code string) Result { return Result{Type: ResultOperationFailed, Code: code} }
	return s.execute(ctx, messageID, Reply{OrderID: orderID, ErrandVersion: c.ErrandVersion}, func(tx pgx.Tx) (Result, error) {
		res, err := walletrepo.GetReservationByOrder(ctx, tx, orderID, false)
		if errors.Is(err, walletrepo.ErrNotFound) {
			released, err := walletrepo.IsOrderReleased(ctx, tx, orderID)
			if err != nil {
				return Result{}, err
			}
			if released {
				return failed(CodeReservationAlreadyReleased), nil
			}
			return failed(CodeReservationNotFound), nil
		}
		if err != nil {
			return Result{}, err
		}

		// Both wallets are locked in one statement, in wallet-ID order.
		wallets, err := walletrepo.LockWalletsByAuth0Subject(ctx, tx, c.RequesterSubject, c.CourierSubject)
		if errors.Is(err, walletrepo.ErrNotFound) {
			return failed(CodeAccountNotFound), nil
		}
		if err != nil {
			return Result{}, err
		}
		var requester, courier walletrepo.Wallet
		for _, w := range wallets {
			if w.Auth0Subject == c.RequesterSubject {
				requester = w
			} else {
				courier = w
			}
		}
		if requester.ID != res.WalletID {
			return Result{}, fmt.Errorf("%w: reservation belongs to another wallet", ErrConflict)
		}
		if res, err = walletrepo.GetReservationByOrder(ctx, tx, orderID, true); err != nil {
			return Result{}, err
		}
		switch res.Status {
		case walletrepo.ReservationTransferred:
			return Result{Type: ResultTransferred}, nil
		case walletrepo.ReservationReleased:
			return failed(CodeReservationAlreadyReleased), nil
		}
		// The courier is paid from the reservation first. Any shortfall comes
		// from available credits; any surplus returns to them.
		fromReservation := min(res.Amount, c.Amount)
		shortfall := c.Amount - fromReservation
		surplus := res.Amount - fromReservation
		if shortfall > requester.Available {
			return failed(CodeInsufficientCredits), nil
		}

		if _, err := walletrepo.InsertLedgerEntry(ctx, tx, walletrepo.LedgerEntry{
			WalletID:             requester.ID,
			Type:                 walletrepo.EntryTransferOut,
			AvailableDelta:       -shortfall,
			ReservedDelta:        -fromReservation,
			OrderID:              &orderID,
			CounterpartyWalletID: &courier.ID,
			IdempotencyKey:       "transfer:" + orderID,
		}); err != nil {
			return Result{}, err
		}
		if _, err := walletrepo.InsertLedgerEntry(ctx, tx, walletrepo.LedgerEntry{
			WalletID:             courier.ID,
			Type:                 walletrepo.EntryTransferIn,
			AvailableDelta:       c.Amount,
			OrderID:              &orderID,
			CounterpartyWalletID: &requester.ID,
			IdempotencyKey:       "transfer:" + orderID,
		}); err != nil {
			return Result{}, err
		}
		if surplus > 0 {
			if _, err := walletrepo.InsertLedgerEntry(ctx, tx, walletrepo.LedgerEntry{
				WalletID:       requester.ID,
				Type:           walletrepo.EntryRelease,
				AvailableDelta: surplus,
				ReservedDelta:  -surplus,
				OrderID:        &orderID,
				IdempotencyKey: "transfer-surplus:" + orderID,
			}); err != nil {
				return Result{}, err
			}
		}
		if _, err := walletrepo.ApplyDeltas(ctx, tx, requester.ID, surplus-shortfall, -res.Amount); err != nil {
			return Result{}, err
		}
		if _, err := walletrepo.ApplyDeltas(ctx, tx, courier.ID, c.Amount, 0); err != nil {
			return Result{}, err
		}
		if _, err := walletrepo.UpdateReservation(ctx, tx, res.ID, res.Amount, walletrepo.ReservationTransferred); err != nil {
			return Result{}, err
		}
		return Result{Type: ResultTransferred}, nil
	})
}

// execute runs one command in a transaction. If the message was applied
// before, it uses the stored result and runs nothing. Otherwise it runs op and
// stores op's result (except a failure) in the same transaction as op's
// balance changes. Either
// way, the result goes to the recorder, if there is one, in that transaction.
func (s *Service) execute(ctx context.Context, messageID string, reply Reply, op func(tx pgx.Tx) (Result, error)) (Result, error) {
	if messageID == "" || len(messageID) > maxMessageIDLength {
		return Result{}, invalid("message id must be 1 to 512 characters")
	}
	reply.CommandMessageID = messageID
	var result Result
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := walletrepo.LockCommand(ctx, tx, messageID); err != nil {
			return err
		}
		stored, err := walletrepo.GetProcessedCommand(ctx, tx, messageID)
		switch {
		case err == nil:
			if err := json.Unmarshal(stored.Result, &result); err != nil {
				return err
			}
		case errors.Is(err, walletrepo.ErrNotFound):
			if result, err = op(tx); err != nil {
				return err
			}
			// A failure is not remembered: it means a state that should be
			// impossible, and an administrator may fix the cause and send the
			// same command again, which must be judged anew. No operation
			// writes anything before it returns one.
			if result.Type != ResultOperationFailed {
				raw, err := json.Marshal(result)
				if err != nil {
					return err
				}
				if err := walletrepo.InsertProcessedCommand(ctx, tx, walletrepo.ProcessedCommand{
					MessageID: messageID, ResultType: result.Type, Result: raw,
				}); err != nil {
					return err
				}
			}
		default:
			return err
		}
		if s.recorder == nil {
			return nil
		}
		return s.recorder.RecordResult(ctx, tx, reply, result)
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// lockReservation locks the requester's wallet, then the order's reservation,
// following the rule that wallets are locked first. It returns
// walletrepo.ErrNotFound if the order has no reservation, and ErrConflict if
// the reservation belongs to a different wallet than the requester's.
func lockReservation(ctx context.Context, tx pgx.Tx, orderID, requesterSubject string) (walletrepo.Reservation, walletrepo.Wallet, error) {
	if _, err := walletrepo.GetReservationByOrder(ctx, tx, orderID, false); err != nil {
		return walletrepo.Reservation{}, walletrepo.Wallet{}, err
	}
	wallets, err := walletrepo.LockWalletsByAuth0Subject(ctx, tx, requesterSubject)
	if errors.Is(err, walletrepo.ErrNotFound) {
		return walletrepo.Reservation{}, walletrepo.Wallet{}, fmt.Errorf("%w: requester has no wallet", ErrConflict)
	}
	if err != nil {
		return walletrepo.Reservation{}, walletrepo.Wallet{}, err
	}
	res, err := walletrepo.GetReservationByOrder(ctx, tx, orderID, true)
	if err != nil {
		return walletrepo.Reservation{}, walletrepo.Wallet{}, err
	}
	if res.WalletID != wallets[0].ID {
		return walletrepo.Reservation{}, walletrepo.Wallet{}, fmt.Errorf("%w: reservation belongs to another wallet", ErrConflict)
	}
	return res, wallets[0], nil
}

func validateOrderID(orderID string) (string, error) {
	if !uuidPattern.MatchString(orderID) {
		return "", invalid("order id must be a UUID")
	}
	return strings.ToLower(orderID), nil
}
