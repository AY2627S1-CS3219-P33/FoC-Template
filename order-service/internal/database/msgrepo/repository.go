// Package msgrepo stores outbox delivery state. It never inserts business messages.
package msgrepo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msggen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	// ErrLeaseLost means a lease-guarded update changed nothing: the lease
	// expired, another worker claimed the row, or a result already moved it.
	ErrLeaseLost = errors.New("msgrepo: outbox lease lost")
	// ErrNotFound means no outbox row has the given ID or idempotency key.
	ErrNotFound = errors.New("msgrepo: outbox row not found")
	// ErrNotFailed means a requeue targeted a row that is not FAILED.
	ErrNotFailed = errors.New("msgrepo: outbox row is not FAILED")
)

// Row statuses.
const (
	StatusPending   = "PENDING"
	StatusSent      = "SENT"
	StatusConfirmed = "CONFIRMED"
	StatusFailed    = "FAILED"
)

// Audit outcomes for InsertRequeueAudit (admin_audit.outcome).
const (
	AuditSucceeded = "SUCCEEDED"
	AuditRejected  = "REJECTED"
	AuditFailed    = "FAILED"
)

// Limits on batch sizes, so a caller bug cannot lock the whole table.
const maxBatch = 500

// Row is a leased outbox record. Attempts fences writes from an earlier claim,
// even if a restarted worker reuses the same name.
type Row struct {
	ID             int64
	Kind           string
	IdempotencyKey string
	ErrandID       string
	LeaseOwner     string
	Payload        []byte
	Attempts       int32
	CreatedAt      time.Time
}

// Record is an outbox row's delivery state without its payload, which can
// hold user IDs. Optional timestamps are zero when unset.
type Record struct {
	ID              int64
	Kind            string
	IdempotencyKey  string
	ErrandID        string
	Status          string
	Attempts        int32
	LastFailureCode string
	CreatedAt       time.Time
	NextAttemptAt   time.Time
	SentAt          time.Time
	ConfirmedAt     time.Time
}

// Resent is a SENT credit row that the unconfirmed sweep moved back to
// PENDING.
type Resent struct {
	ID             int64
	Kind           string
	IdempotencyKey string
	ErrandID       string
	Attempts       int32
	SentAt         time.Time
}

type Repository struct{ queries *msggen.Queries }

// db may be a pool or a transaction; credit-result handlers must use the same
// transaction for the errand transition and its outbox confirmation, and the
// admin replay for Requeue and InsertRequeueAudit.
func New(db msggen.DBTX) *Repository { return &Repository{queries: msggen.New(db)} }

// Claim leases up to limit due rows of one family (credit.* when credit is
// true, event.* otherwise) and increments their attempts.
func (r *Repository) Claim(ctx context.Context, worker string, credit bool, limit int, lease time.Duration) ([]Row, error) {
	if worker == "" || limit < 1 || limit > maxBatch || lease < time.Microsecond {
		return nil, fmt.Errorf("msgrepo: invalid claim parameters")
	}
	rows, err := r.queries.ClaimOutbox(ctx, msggen.ClaimOutboxParams{Worker: text(worker), Credit: credit, BatchSize: int32(limit), LeaseMicroseconds: lease.Microseconds()})
	if err != nil {
		return nil, err
	}
	result := make([]Row, 0, len(rows))
	for _, row := range rows {
		result = append(result, Row{ID: row.ID, Kind: row.Kind, IdempotencyKey: row.IdempotencyKey, ErrandID: row.ErrandID.String(), LeaseOwner: row.LeaseOwner.String, Payload: row.Payload, Attempts: row.Attempts, CreatedAt: row.CreatedAt.Time})
	}
	return result, nil
}

// MarkSent records a broker-confirmed publish. It returns ErrLeaseLost if
// row's claim no longer holds the lease.
func (r *Repository) MarkSent(ctx context.Context, row Row) error {
	n, err := r.queries.MarkOutboxSent(ctx, msggen.MarkOutboxSentParams{ID: row.ID, Worker: text(row.LeaseOwner), Attempts: row.Attempts})
	return leased(n, err)
}

// Reschedule releases the lease and makes the row due again after delay. It
// returns ErrLeaseLost if row's claim no longer holds the lease.
func (r *Repository) Reschedule(ctx context.Context, row Row, delay time.Duration, code string) error {
	if delay < 0 || code == "" {
		return fmt.Errorf("msgrepo: invalid retry parameters")
	}
	n, err := r.queries.RescheduleOutbox(ctx, msggen.RescheduleOutboxParams{ID: row.ID, Worker: text(row.LeaseOwner), Attempts: row.Attempts, DelayMicroseconds: delay.Microseconds(), FailureCode: text(code)})
	return leased(n, err)
}

// MarkFailed gives up on the row. It returns ErrLeaseLost if row's claim no
// longer holds the lease.
func (r *Repository) MarkFailed(ctx context.Context, row Row, code string) error {
	if code == "" {
		return fmt.Errorf("msgrepo: failure code is required")
	}
	n, err := r.queries.MarkOutboxFailed(ctx, msggen.MarkOutboxFailedParams{ID: row.ID, Worker: text(row.LeaseOwner), Attempts: row.Attempts, FailureCode: text(code)})
	return leased(n, err)
}

// ConfirmByKey moves a PENDING or SENT credit row to CONFIRMED. It reports
// whether a row changed; a repeat, or a key that is not a credit row, changes
// nothing.
func (r *Repository) ConfirmByKey(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, fmt.Errorf("msgrepo: command key is required")
	}
	n, err := r.queries.ConfirmOutboxByKey(ctx, key)
	return n > 0, err
}

// MarkFailedByKey moves a PENDING or SENT credit row to FAILED with code
// (credit.operation_failed). It reports whether a row changed; a CONFIRMED or
// already FAILED row is left alone.
func (r *Repository) MarkFailedByKey(ctx context.Context, key, code string) (bool, error) {
	if key == "" || code == "" {
		return false, fmt.Errorf("msgrepo: command key and failure code are required")
	}
	n, err := r.queries.FailOutboxByKey(ctx, msggen.FailOutboxByKeyParams{IdempotencyKey: key, FailureCode: text(code)})
	return n > 0, err
}

// ResendUnconfirmed moves up to limit SENT credit rows whose result is older
// than timeout back to PENDING with the same key, oldest first.
func (r *Repository) ResendUnconfirmed(ctx context.Context, timeout time.Duration, limit int) ([]Resent, error) {
	if timeout < time.Microsecond || limit < 1 || limit > maxBatch {
		return nil, fmt.Errorf("msgrepo: invalid resend parameters")
	}
	rows, err := r.queries.ResendUnconfirmedOutbox(ctx, msggen.ResendUnconfirmedOutboxParams{TimeoutMicroseconds: timeout.Microseconds(), RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	result := make([]Resent, 0, len(rows))
	for _, row := range rows {
		result = append(result, Resent{ID: row.ID, Kind: row.Kind, IdempotencyKey: row.IdempotencyKey, ErrandID: row.ErrandID.String(), Attempts: row.Attempts, SentAt: row.SentAt.Time})
	}
	return result, nil
}

// GetByKey returns the row with key, or ErrNotFound.
func (r *Repository) GetByKey(ctx context.Context, key string) (Record, error) {
	row, err := r.queries.GetOutboxByKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	return record(row), nil
}

// ListFailed returns up to limit FAILED rows, oldest first, after the row ID
// afterID (0 for the first page). The next cursor is the last row's ID.
func (r *Repository) ListFailed(ctx context.Context, limit int, afterID int64) ([]Record, error) {
	if limit < 1 || limit > maxBatch || afterID < 0 {
		return nil, fmt.Errorf("msgrepo: invalid page parameters")
	}
	rows, err := r.queries.ListFailedOutbox(ctx, msggen.ListFailedOutboxParams{AfterID: afterID, RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(rows))
	for _, row := range rows {
		result = append(result, record(row))
	}
	return result, nil
}

// Requeue resets a FAILED row to PENDING with attempts cleared and its
// idempotency key unchanged (NFR7.4.2). It returns ErrNotFound or
// ErrNotFailed when there is nothing to requeue. Run it in the same
// transaction as InsertRequeueAudit.
func (r *Repository) Requeue(ctx context.Context, id int64) (Record, error) {
	row, err := r.queries.RequeueOutbox(ctx, id)
	if err == nil {
		return record(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, err
	}
	if _, err := r.queries.GetOutboxStatus(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	} else if err != nil {
		return Record{}, err
	}
	return Record{}, ErrNotFailed
}

// InsertRequeueAudit records an admin requeue of outbox row id (F6.5). It is
// the only admin_audit write the messaging stream makes.
func (r *Repository) InsertRequeueAudit(ctx context.Context, actorID string, id int64, reason, outcome string) error {
	switch {
	case actorID == "" || id < 1:
		return fmt.Errorf("msgrepo: audit actor and row are required")
	case outcome != AuditSucceeded && outcome != AuditRejected && outcome != AuditFailed:
		return fmt.Errorf("msgrepo: unknown audit outcome")
	}
	_, err := r.queries.InsertRequeueAudit(ctx, msggen.InsertRequeueAuditParams{ActorID: actorID, TargetID: strconv.FormatInt(id, 10), Reason: pgtype.Text{String: reason, Valid: reason != ""}, Outcome: outcome})
	return err
}

func leased(n int64, err error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

func record(row msggen.Outbox) Record {
	return Record{
		ID: row.ID, Kind: row.Kind, IdempotencyKey: row.IdempotencyKey, ErrandID: row.ErrandID.String(),
		Status: row.Status, Attempts: row.Attempts, LastFailureCode: row.LastFailureCode.String,
		CreatedAt: row.CreatedAt.Time, NextAttemptAt: row.NextAttemptAt.Time, SentAt: row.SentAt.Time, ConfirmedAt: row.ConfirmedAt.Time,
	}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
