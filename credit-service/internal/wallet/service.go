// Package wallet holds the credit use cases. For now: creating a wallet with
// its initial allocation, and reading a wallet balance.
package wallet

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/walletrepo"
)

var (
	ErrNotFound = errors.New("wallet not found")
	// ErrConflict means the request contradicts an existing wallet: the owner
	// already has a different Auth0 subject, or the subject belongs to another
	// owner.
	ErrConflict = errors.New("wallet conflicts with an existing wallet")
	ErrInvalid  = errors.New("invalid argument")
)

// InitialAllocationKey is the fixed idempotency key of a wallet's first
// ledger entry, so the allocation can only ever be recorded once.
const InitialAllocationKey = "initial-allocation"

const maxSubjectLength = 255

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Wallet struct {
	OwnerID      string
	Auth0Subject string
	Available    int64
	Reserved     int64
	CreatedAt    time.Time
}

type Service struct {
	pool          *pgxpool.Pool
	initialCredit int64
	recorder      ResultRecorder
}

// NewService creates the use cases. initialCredit is the number of credits
// every new wallet receives; it comes from configuration, never from callers.
func NewService(pool *pgxpool.Pool, initialCredit int64) *Service {
	if pool == nil {
		panic("wallet: pool is required")
	}
	if initialCredit < 0 {
		panic("wallet: initial credit must not be negative")
	}
	return &Service{pool: pool, initialCredit: initialCredit}
}

// WithResultRecorder makes every order command store its result with the
// recorder, in the command's transaction. It returns the service so it can be
// chained after NewService.
func (s *Service) WithResultRecorder(r ResultRecorder) *Service {
	s.recorder = r
	return s
}

// CreateWallet creates the owner's wallet and credits the initial allocation,
// in one transaction. It is idempotent: if the owner already has a wallet with
// the same subject it is returned unchanged with created == false, and no
// second allocation is made.
func (s *Service) CreateWallet(ctx context.Context, ownerID, auth0Subject string) (w Wallet, created bool, err error) {
	if ownerID, err = validateOwnerID(ownerID); err != nil {
		return Wallet{}, false, err
	}
	if err = validateSubject(auth0Subject); err != nil {
		return Wallet{}, false, err
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, inserted, err := walletrepo.InsertWalletIfAbsent(ctx, tx, ownerID, auth0Subject)
		if errors.Is(err, walletrepo.ErrSubjectTaken) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		// A separate statement so a concurrent creator's committed row is visible.
		row, err := walletrepo.GetWalletByOwner(ctx, tx, ownerID)
		if err != nil {
			return err
		}
		created = inserted
		if !inserted {
			if row.Auth0Subject != auth0Subject {
				return ErrConflict
			}
			w = fromRow(row)
			return nil
		}
		if s.initialCredit > 0 {
			if _, err := walletrepo.InsertLedgerEntry(ctx, tx, walletrepo.LedgerEntry{
				WalletID:       row.ID,
				Type:           walletrepo.EntryAllocate,
				AvailableDelta: s.initialCredit,
				IdempotencyKey: InitialAllocationKey,
			}); err != nil {
				return err
			}
			if row, err = walletrepo.ApplyDeltas(ctx, tx, row.ID, s.initialCredit, 0); err != nil {
				return err
			}
		}
		w = fromRow(row)
		return nil
	})
	if err != nil {
		return Wallet{}, false, err
	}
	return w, created, nil
}

// GetByOwner reads the wallet of the user-service account with this UUID.
func (s *Service) GetByOwner(ctx context.Context, ownerID string) (Wallet, error) {
	ownerID, err := validateOwnerID(ownerID)
	if err != nil {
		return Wallet{}, err
	}
	row, err := walletrepo.GetWalletByOwner(ctx, s.pool, ownerID)
	return result(row, err)
}

// GetBySubject reads the wallet that belongs to an Auth0 subject.
func (s *Service) GetBySubject(ctx context.Context, auth0Subject string) (Wallet, error) {
	if err := validateSubject(auth0Subject); err != nil {
		return Wallet{}, ErrNotFound
	}
	row, err := walletrepo.GetWalletByAuth0Subject(ctx, s.pool, auth0Subject)
	return result(row, err)
}

func result(row walletrepo.Wallet, err error) (Wallet, error) {
	if errors.Is(err, walletrepo.ErrNotFound) {
		return Wallet{}, ErrNotFound
	}
	if err != nil {
		return Wallet{}, err
	}
	return fromRow(row), nil
}

func fromRow(row walletrepo.Wallet) Wallet {
	return Wallet{
		OwnerID:      row.OwnerID,
		Auth0Subject: row.Auth0Subject,
		Available:    row.Available,
		Reserved:     row.Reserved,
		CreatedAt:    row.CreatedAt,
	}
}

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

// validateOwnerID checks the UUID format and returns it in the lowercase form
// the database stores.
func validateOwnerID(ownerID string) (string, error) {
	if !uuidPattern.MatchString(ownerID) {
		return "", invalid("owner id must be a UUID")
	}
	return strings.ToLower(ownerID), nil
}

func validateSubject(subject string) error {
	if subject == "" || strings.TrimSpace(subject) != subject {
		return invalid("auth0 subject must be non-empty without surrounding whitespace")
	}
	if len(subject) > maxSubjectLength {
		return invalid("auth0 subject is too long")
	}
	for _, r := range subject {
		if unicode.IsControl(r) {
			return invalid("auth0 subject contains control characters")
		}
	}
	return nil
}
