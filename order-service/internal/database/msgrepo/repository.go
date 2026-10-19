// Package msgrepo stores outbox delivery state. It never inserts business messages.
package msgrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msggen"
	"github.com/jackc/pgx/v5/pgtype"
)

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

type Repository struct{ queries *msggen.Queries }

// db may be a pool or a transaction; credit-result handlers must use the same
// transaction for the errand transition and its outbox confirmation.
func New(db msggen.DBTX) *Repository { return &Repository{queries: msggen.New(db)} }

func (r *Repository) Claim(ctx context.Context, worker string, credit bool, limit int, lease time.Duration) ([]Row, error) {
	if worker == "" || limit < 1 || limit > 500 || lease < time.Microsecond {
		return nil, fmt.Errorf("msgrepo: invalid claim parameters")
	}
	rows, err := r.queries.ClaimOutbox(ctx, msggen.ClaimOutboxParams{Worker: pgtype.Text{String: worker, Valid: true}, Credit: credit, BatchSize: int32(limit), LeaseMicroseconds: lease.Microseconds()})
	if err != nil {
		return nil, err
	}
	result := make([]Row, 0, len(rows))
	for _, row := range rows {
		id, _ := row.ErrandID.Value()
		result = append(result, Row{ID: row.ID, Kind: row.Kind, IdempotencyKey: row.IdempotencyKey, ErrandID: id.(string), LeaseOwner: row.LeaseOwner.String, Payload: row.Payload, Attempts: row.Attempts, CreatedAt: row.CreatedAt.Time})
	}
	return result, nil
}

func (r *Repository) MarkSent(ctx context.Context, row Row) error {
	_, err := r.queries.MarkOutboxSent(ctx, msggen.MarkOutboxSentParams{ID: row.ID, Worker: pgtype.Text{String: row.LeaseOwner, Valid: true}, Attempts: row.Attempts})
	return err
}

func (r *Repository) Reschedule(ctx context.Context, row Row, delay time.Duration, code string) error {
	if delay < 0 || code == "" {
		return fmt.Errorf("msgrepo: invalid retry parameters")
	}
	_, err := r.queries.RescheduleOutbox(ctx, msggen.RescheduleOutboxParams{ID: row.ID, Worker: pgtype.Text{String: row.LeaseOwner, Valid: true}, Attempts: row.Attempts, DelayMicroseconds: delay.Microseconds(), FailureCode: pgtype.Text{String: code, Valid: true}})
	return err
}

func (r *Repository) MarkFailed(ctx context.Context, row Row, code string) error {
	if code == "" {
		return fmt.Errorf("msgrepo: failure code is required")
	}
	_, err := r.queries.MarkOutboxFailed(ctx, msggen.MarkOutboxFailedParams{ID: row.ID, Worker: pgtype.Text{String: row.LeaseOwner, Valid: true}, Attempts: row.Attempts, FailureCode: pgtype.Text{String: code, Valid: true}})
	return err
}

func (r *Repository) ConfirmByKey(ctx context.Context, key string) error {
	if key == "" {
		return fmt.Errorf("msgrepo: command key is required")
	}
	_, err := r.queries.ConfirmOutboxByKey(ctx, key)
	return err
}

func (r *Repository) RequeueUnconfirmed(ctx context.Context, timeout time.Duration) error {
	if timeout < time.Microsecond {
		return fmt.Errorf("msgrepo: confirmation timeout must be positive")
	}
	_, err := r.queries.RequeueUnconfirmedOutbox(ctx, timeout.Microseconds())
	return err
}
