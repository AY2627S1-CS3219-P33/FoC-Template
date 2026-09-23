package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open connects and checks reachability. Errors deliberately omit connection
// strings and driver diagnostics, which can contain deployment credentials.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("PostgreSQL connection failed")
	}
	return pool, nil
}

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

var _ UserRepository = (*Postgres)(nil)

const columns = `id::text, username, email, role, active, display_name, mobile_number, created_at, updated_at`

func (p *Postgres) Create(ctx context.Context, input CreateUser) (*User, error) {
	return scanUser(p.pool.QueryRow(ctx, `
		INSERT INTO accounts (username, email, password_hash)
		VALUES ($1, $2, $3) RETURNING `+columns,
		input.Username, input.Email, input.PasswordHash))
}

func (p *Postgres) FindByID(ctx context.Context, id string) (*User, error) {
	uuid, err := parseID(id)
	if err != nil {
		return nil, err
	}
	return scanUser(p.pool.QueryRow(ctx,
		`SELECT `+columns+` FROM accounts WHERE id = $1 AND deleted_at IS NULL`, uuid))
}

func (p *Postgres) UpdateProfile(ctx context.Context, id string, patch ProfilePatch) (*User, error) {
	uuid, err := parseID(id)
	if err != nil {
		return nil, err
	}
	u, err := scanUser(p.pool.QueryRow(ctx, `
		UPDATE accounts SET display_name = COALESCE($2, display_name),
		mobile_number = COALESCE($3, mobile_number), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL AND active
		RETURNING `+columns, uuid, patch.DisplayName, patch.Mobile))
	if errors.Is(err, ErrNotFound) {
		// The UPDATE predicate prevents changes after concurrent deactivation/deletion.
		existing, lookupErr := p.FindByID(ctx, id)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if !existing.Active {
			return nil, ErrInactive
		}
	}
	return u, err
}

func (p *Postgres) SoftDelete(ctx context.Context, id string) error {
	uuid, err := parseID(id)
	if err != nil {
		return err
	}
	result, err := p.pool.Exec(ctx, `
		UPDATE accounts SET deleted_at = now(), updated_at = now(), active = false
		WHERE id = $1 AND deleted_at IS NULL`, uuid)
	if err != nil {
		return translateError(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func parseID(id string) (pgtype.UUID, error) {
	var uuid pgtype.UUID
	if err := uuid.Scan(id); err != nil || !uuid.Valid {
		return uuid, ErrNotFound
	}
	return uuid, nil
}

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.Active,
		&u.DisplayName, &u.Mobile, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, translateError(err)
	}
	return &u, nil
}

func translateError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "accounts_username_unique" || pgErr.ConstraintName == "accounts_email_unique") {
		return ErrConflict
	}
	// PostgreSQL detail fields can contain submitted account data.
	return errors.New("account persistence operation failed")
}
