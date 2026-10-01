package repository_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"user-service/internal/repository"
	"user-service/internal/service"
)

func TestBootstrapConcurrentAndRepeated(t *testing.T) {
	pool := testDatabase(t)
	repo := repository.NewPostgres(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	input := repository.CreateAuth0User{Subject: "auth0|admin", Username: "Admin", Email: "admin@example.com"}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := service.BootstrapSuperAdmin(ctx, repo, input); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var count int
	var before, after time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE role='SUPER_ADMIN' AND active AND auth0_subject='auth0|admin'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("account count %d: %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT completed_at FROM bootstrap_state`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := service.BootstrapSuperAdmin(ctx, repo, repository.CreateAuth0User{}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT completed_at FROM bootstrap_state`).Scan(&after); err != nil || !before.Equal(after) {
		t.Fatal("repeat modified completion", err)
	}
}

func TestBootstrapExistingAndRecovery(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "inactive", true: "deleted"}[deleted], func(t *testing.T) {
			pool := testDatabase(t)
			ctx := context.Background()
			repo := repository.NewPostgres(pool)
			_, err := pool.Exec(ctx, `INSERT INTO accounts(auth0_subject,username,email,role,active,deleted_at)
    VALUES('auth0|existing','existing','existing@example.com','SUPER_ADMIN',false,CASE WHEN $1 THEN now() ELSE NULL END)`, deleted)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.BootstrapSuperAdmin(ctx, repo, repository.CreateAuth0User{}); err != nil {
				t.Fatal(err)
			}
			var completed, active bool
			if err := pool.QueryRow(ctx, `SELECT completed_at IS NOT NULL, (SELECT active FROM accounts) FROM bootstrap_state`).Scan(&completed, &active); err != nil || !completed || active {
				t.Fatal("existing account changed or completion missing", err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM accounts`); err != nil {
				t.Fatal(err)
			}
			if err := service.BootstrapSuperAdmin(ctx, repo, repository.CreateAuth0User{}); !errors.Is(err, service.ErrBootstrapRecovery) {
				t.Fatal("expected recovery error", err)
			}
		})
	}
}

func TestBootstrapConflictAndRollback(t *testing.T) {
	for _, kind := range []string{"subject", "username", "email", "completion", "missing"} {
		t.Run(kind, func(t *testing.T) {
			pool := testDatabase(t)
			ctx := context.Background()
			repo := repository.NewPostgres(pool)
			input := repository.CreateAuth0User{Subject: "auth0|admin", Username: "admin", Email: "admin@example.com"}
			var err error
			switch kind {
			case "completion":
				_, err = pool.Exec(ctx, `ALTER TABLE bootstrap_state ADD CHECK (completed_at IS NULL)`)
			case "missing":
				_, err = pool.Exec(ctx, `DELETE FROM bootstrap_state`)
			default:
				existing := repository.CreateAuth0User{Subject: "auth0|other", Username: "other", Email: "other@example.com"}
				switch kind {
				case "subject":
					existing.Subject = input.Subject
				case "username":
					existing.Username = "ADMIN"
				case "email":
					existing.Email = input.Email
				}
				_, _, err = repo.CreateAuth0(ctx, existing)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := service.BootstrapSuperAdmin(ctx, repo, input); err == nil {
				t.Fatal("expected failure")
			}
			var admins, completed int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM accounts WHERE role='SUPER_ADMIN'),(SELECT count(*) FROM bootstrap_state WHERE completed_at IS NOT NULL)`).Scan(&admins, &completed); err != nil || admins != 0 || completed != 0 {
				t.Fatal("partial bootstrap persisted", err)
			}
			// The pool remains usable after a failed bootstrap.
			if err := pool.Ping(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBootstrapCanceledCallbackReleasesConnection(t *testing.T) {
	base := testDatabase(t)
	cfg := base.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := repository.NewPostgres(pool)
	err = repo.WithBootstrapLock(ctx, func(tx repository.BootstrapTransaction) error {
		if err := tx.CreateSuperAdmin(ctx, repository.CreateAuth0User{Subject: "auth0|cancel", Username: "cancel", Email: "cancel@example.com"}); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("expected canceled callback", err)
	}
	check, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := repo.WithBootstrapLock(check, func(tx repository.BootstrapTransaction) error {
		completed, exists, err := tx.State(check)
		if completed || exists {
			t.Error("canceled transaction retained writes")
		}
		return err
	}); err != nil {
		t.Fatal("connection or row lock was not released", err)
	}
}
