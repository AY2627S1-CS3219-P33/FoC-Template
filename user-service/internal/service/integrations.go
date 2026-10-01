package service

import "context"

// CreditBalanceReader obtains an exact decimal balance from the credit service.
// No adapter or substitute balance is provided in this slice.
type CreditBalanceReader interface {
	Balance(ctx context.Context, userID string) (string, error)
}

// DeletionCoordinator is a future integration boundary, not a snapshot check.
// It must exclude reserved credits and active courier participation and fence
// new participation/reservations through finalization. Its distributed failure
// and recovery protocol must be designed with the owning services before use.
// DeleteAccount remains disabled even if an implementation exists elsewhere.
type DeletionCoordinator interface {
	DeleteWhenEligible(ctx context.Context, userID string, finalize func(context.Context) error) error
}

// SessionRevoker must terminate every session and prevent concurrent session
// issuance for an account being deleted. No implementation is provided yet.
type SessionRevoker interface {
	RevokeAll(ctx context.Context, userID string) error
}
