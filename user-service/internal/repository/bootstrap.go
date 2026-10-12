package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// BootstrapTransaction exposes only the persistence operations needed by bootstrap.
// Implementations hold the coordination row lock for the entire callback.
type BootstrapTransaction interface {
	State(context.Context) (completed, bootstrapAccountExists bool, err error)
	CreateSuperAdmin(context.Context, CreateAuth0User) error
	MarkCompleted(context.Context) error
}

type BootstrapRepository interface {
	WithBootstrapLock(context.Context, func(BootstrapTransaction) error) error
}

type bootstrapTransaction struct{ tx pgx.Tx }

// WithBootstrapLock runs fn in a transaction while holding the bootstrap coordination row lock.
// It commits only when fn succeeds and otherwise rolls back.
func (p *Postgres) WithBootstrapLock(ctx context.Context, fn func(BootstrapTransaction) error) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return errors.New("begin bootstrap transaction failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM bootstrap_state WHERE name = 'bootstrap_is_completed' FOR UPDATE`).Scan(&name); err != nil {
		return errors.New("bootstrap coordination row unavailable; apply migrations before startup")
	}
	if err := fn(bootstrapTransaction{tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("commit bootstrap transaction failed")
	}
	return nil
}

// State reports whether bootstrap completed and its linked account still exists,
// including inactive or soft-deleted accounts.
func (b bootstrapTransaction) State(ctx context.Context) (completed, bootstrapAccountExists bool, err error) {
	err = b.tx.QueryRow(ctx, `SELECT completed_at IS NOT NULL,
 EXISTS (SELECT 1 FROM accounts WHERE auth0_subject = bootstrap_state.bootstrap_subject)
		FROM bootstrap_state WHERE name = 'bootstrap_is_completed'`).Scan(&completed, &bootstrapAccountExists)
	if err != nil {
		err = errors.New("read bootstrap state failed")
	}
	return
}

// CreateSuperAdmin links an active local account and remembers the bootstrap identity.
func (b bootstrapTransaction) CreateSuperAdmin(ctx context.Context, input CreateAuth0User) error {
	_, err := b.tx.Exec(ctx, `INSERT INTO accounts (auth0_subject, username, email, active)
 VALUES ($1, $2, $3, true)`, input.Subject, input.Username, input.Email)
	if err != nil {
		return translateError(err)
	}
	if _, err := b.tx.Exec(ctx, `UPDATE bootstrap_state SET bootstrap_subject = $1
 WHERE name = 'bootstrap_is_completed' AND completed_at IS NULL`, input.Subject); err != nil {
		return errors.New("record bootstrap identity failed")
	}
	return nil
}

// MarkCompleted records bootstrap completion and fails if it was already recorded.
func (b bootstrapTransaction) MarkCompleted(ctx context.Context) error {
	result, err := b.tx.Exec(ctx, `UPDATE bootstrap_state SET completed_at = CURRENT_TIMESTAMP
 WHERE name = 'bootstrap_is_completed' AND completed_at IS NULL`)
	if err != nil || result.RowsAffected() != 1 {
		return errors.New("record bootstrap completion failed")
	}
	return nil
}
