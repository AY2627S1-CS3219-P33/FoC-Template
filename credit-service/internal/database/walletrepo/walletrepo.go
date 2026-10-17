package walletrepo

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Wallet struct {
	ID           string
	OwnerID      string
	Auth0Subject string
	Available    int64
	Reserved     int64
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const walletColumns = `id::text, owner_id::text, auth0_subject, available, reserved, version, created_at, updated_at`

func scanWallet(row pgx.Row) (Wallet, error) {
	var w Wallet
	err := row.Scan(&w.ID, &w.OwnerID, &w.Auth0Subject, &w.Available, &w.Reserved, &w.Version, &w.CreatedAt, &w.UpdatedAt)
	return w, mapError(err)
}

// CreateWallet inserts an empty wallet. It returns ErrAlreadyExists if the
// owner already has one and ErrSubjectTaken if another wallet has the subject.
func CreateWallet(ctx context.Context, db DBTX, ownerID, auth0Subject string) (Wallet, error) {
	return scanWallet(db.QueryRow(ctx,
		`INSERT INTO wallets (owner_id, auth0_subject) VALUES ($1::uuid, $2)
		 RETURNING `+walletColumns, ownerID, auth0Subject))
}

// InsertWalletIfAbsent creates the owner's wallet unless one exists, in which
// case it changes nothing and reports created == false. Read the winner with
// GetWalletByOwner in a later statement. It returns ErrSubjectTaken if a
// different wallet already uses the subject.
func InsertWalletIfAbsent(ctx context.Context, db DBTX, ownerID, auth0Subject string) (Wallet, bool, error) {
	wallet, err := scanWallet(db.QueryRow(ctx,
		`INSERT INTO wallets (owner_id, auth0_subject) VALUES ($1::uuid, $2)
		 ON CONFLICT (owner_id) DO NOTHING RETURNING `+walletColumns, ownerID, auth0Subject))
	if errors.Is(err, ErrNotFound) {
		return Wallet{}, false, nil
	}
	return wallet, err == nil, err
}

func GetWallet(ctx context.Context, db DBTX, walletID string) (Wallet, error) {
	return scanWallet(db.QueryRow(ctx,
		`SELECT `+walletColumns+` FROM wallets WHERE id = $1::uuid`, walletID))
}

func GetWalletByAuth0Subject(ctx context.Context, db DBTX, auth0Subject string) (Wallet, error) {
	return scanWallet(db.QueryRow(ctx,
		`SELECT `+walletColumns+` FROM wallets WHERE auth0_subject = $1`, auth0Subject))
}

func GetWalletByOwner(ctx context.Context, db DBTX, ownerID string) (Wallet, error) {
	return scanWallet(db.QueryRow(ctx,
		`SELECT `+walletColumns+` FROM wallets WHERE owner_id = $1::uuid`, ownerID))
}

// LockWalletsByOwner locks the owners' wallets for the rest of the
// transaction and returns them. Rows are locked in wallet-ID order whatever
// order the owners are passed in, so two transfers between the same pair of
// wallets cannot deadlock. It returns ErrNotFound if any wallet is missing.
func LockWalletsByOwner(ctx context.Context, db DBTX, ownerIDs ...string) ([]Wallet, error) {
	unique := slices.Compact(slices.Sorted(slices.Values(ownerIDs)))
	rows, err := db.Query(ctx,
		`SELECT `+walletColumns+` FROM wallets
		 WHERE owner_id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`, unique)
	if err != nil {
		return nil, mapError(err)
	}
	wallets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Wallet, error) {
		return scanWallet(row)
	})
	if err != nil {
		return nil, mapError(err)
	}
	if len(wallets) != len(unique) {
		return nil, ErrNotFound
	}
	return wallets, nil
}

// ApplyDeltas adds the signed deltas to a wallet and bumps its version. The
// database CHECK constraints reject a negative result with ErrNegativeBalance.
func ApplyDeltas(ctx context.Context, db DBTX, walletID string, availableDelta, reservedDelta int64) (Wallet, error) {
	return scanWallet(db.QueryRow(ctx,
		`UPDATE wallets
		 SET available = available + $2, reserved = reserved + $3,
		     version = version + 1, updated_at = now()
		 WHERE id = $1::uuid RETURNING `+walletColumns,
		walletID, availableDelta, reservedDelta))
}

type ReservationStatus string

const (
	ReservationActive      ReservationStatus = "ACTIVE"
	ReservationReleased    ReservationStatus = "RELEASED"
	ReservationTransferred ReservationStatus = "TRANSFERRED"
)

type Reservation struct {
	ID        string
	WalletID  string
	OrderID   string
	Amount    int64
	Status    ReservationStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

const reservationColumns = `id::text, wallet_id::text, order_id::text, amount, status, created_at, updated_at`

func scanReservation(row pgx.Row) (Reservation, error) {
	var r Reservation
	err := row.Scan(&r.ID, &r.WalletID, &r.OrderID, &r.Amount, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	return r, mapError(err)
}

// CreateReservation inserts an ACTIVE reservation. It returns ErrAlreadyExists
// if the order already has one.
func CreateReservation(ctx context.Context, db DBTX, walletID, orderID string, amount int64) (Reservation, error) {
	return scanReservation(db.QueryRow(ctx,
		`INSERT INTO reservations (wallet_id, order_id, amount)
		 VALUES ($1::uuid, $2::uuid, $3) RETURNING `+reservationColumns,
		walletID, orderID, amount))
}

// GetReservationByOrder reads an order's reservation. With forUpdate it also
// locks the row for the rest of the transaction.
func GetReservationByOrder(ctx context.Context, db DBTX, orderID string, forUpdate bool) (Reservation, error) {
	query := `SELECT ` + reservationColumns + ` FROM reservations WHERE order_id = $1::uuid`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanReservation(db.QueryRow(ctx, query, orderID))
}

// UpdateReservation changes a reservation's amount and status.
func UpdateReservation(ctx context.Context, db DBTX, reservationID string, amount int64, status ReservationStatus) (Reservation, error) {
	return scanReservation(db.QueryRow(ctx,
		`UPDATE reservations SET amount = $2, status = $3, updated_at = now()
		 WHERE id = $1::uuid RETURNING `+reservationColumns,
		reservationID, amount, string(status)))
}

type EntryType string

const (
	EntryAllocate          EntryType = "ALLOCATE"
	EntryReserve           EntryType = "RESERVE"
	EntryReservationAdjust EntryType = "RESERVATION_ADJUST"
	EntryRelease           EntryType = "RELEASE"
	EntryTransferOut       EntryType = "TRANSFER_OUT"
	EntryTransferIn        EntryType = "TRANSFER_IN"
)

type LedgerEntry struct {
	ID                   int64
	WalletID             string
	Type                 EntryType
	AvailableDelta       int64
	ReservedDelta        int64
	OrderID              *string
	CounterpartyWalletID *string
	IdempotencyKey       string
	CreatedAt            time.Time
}

const ledgerColumns = `id, wallet_id::text, entry_type, available_delta, reserved_delta,
	order_id::text, counterparty_wallet_id::text, idempotency_key, created_at`

func scanLedgerEntry(row pgx.Row) (LedgerEntry, error) {
	var e LedgerEntry
	err := row.Scan(&e.ID, &e.WalletID, &e.Type, &e.AvailableDelta, &e.ReservedDelta,
		&e.OrderID, &e.CounterpartyWalletID, &e.IdempotencyKey, &e.CreatedAt)
	return e, mapError(err)
}

// InsertLedgerEntry appends an entry. It returns ErrDuplicateOperation if the
// wallet already has an entry with the same idempotency key.
func InsertLedgerEntry(ctx context.Context, db DBTX, e LedgerEntry) (LedgerEntry, error) {
	return scanLedgerEntry(db.QueryRow(ctx,
		`INSERT INTO ledger_entries
		   (wallet_id, entry_type, available_delta, reserved_delta, order_id,
		    counterparty_wallet_id, idempotency_key)
		 VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6::uuid, $7)
		 RETURNING `+ledgerColumns,
		e.WalletID, string(e.Type), e.AvailableDelta, e.ReservedDelta, e.OrderID,
		e.CounterpartyWalletID, e.IdempotencyKey))
}

// GetLedgerEntryByKey finds a previously applied operation, e.g. to replay its
// result after an ErrDuplicateOperation.
func GetLedgerEntryByKey(ctx context.Context, db DBTX, walletID, idempotencyKey string) (LedgerEntry, error) {
	return scanLedgerEntry(db.QueryRow(ctx,
		`SELECT `+ledgerColumns+` FROM ledger_entries
		 WHERE wallet_id = $1::uuid AND idempotency_key = $2`, walletID, idempotencyKey))
}

// ListLedgerEntries returns up to limit entries for a wallet in ID order,
// starting after the given ID (0 for the first page).
func ListLedgerEntries(ctx context.Context, db DBTX, walletID string, afterID int64, limit int) ([]LedgerEntry, error) {
	rows, err := db.Query(ctx,
		`SELECT `+ledgerColumns+` FROM ledger_entries
		 WHERE wallet_id = $1::uuid AND id > $2 ORDER BY id LIMIT $3`,
		walletID, afterID, limit)
	if err != nil {
		return nil, mapError(err)
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LedgerEntry, error) {
		return scanLedgerEntry(row)
	})
	return entries, mapError(err)
}
