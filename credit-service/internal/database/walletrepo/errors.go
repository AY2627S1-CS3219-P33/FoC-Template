package walletrepo

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists means a wallet for the owner, or a reservation for the
	// order, already exists.
	ErrAlreadyExists = errors.New("already exists")
	// ErrDuplicateOperation means the idempotency key was already recorded for
	// this wallet: the operation has been applied before.
	ErrDuplicateOperation = errors.New("operation already applied")
	// ErrNegativeBalance means the change would make a balance negative.
	ErrNegativeBalance = errors.New("balance would become negative")
)

const (
	codeUniqueViolation = "23505"
	codeCheckViolation  = "23514"

	constraintIdempotency = "ledger_entries_wallet_idempotency_key"
	constraintAvailable   = "wallets_available_nonneg"
	constraintReserved    = "wallets_reserved_nonneg"
)

// mapError translates database errors into the package's typed errors.
// Anything unrecognised is returned unchanged.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case codeUniqueViolation:
			if pgErr.ConstraintName == constraintIdempotency {
				return ErrDuplicateOperation
			}
			return ErrAlreadyExists
		case codeCheckViolation:
			if pgErr.ConstraintName == constraintAvailable || pgErr.ConstraintName == constraintReserved {
				return ErrNegativeBalance
			}
		}
	}
	return err
}
