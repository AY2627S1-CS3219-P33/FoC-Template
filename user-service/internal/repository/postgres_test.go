package repository_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"user-service/internal/auth"
	"user-service/internal/repository"
	"user-service/internal/service"
)

// Every test owns a randomly named schema. No existing schema or database is
// reset, and no production DATABASE_URL fallback is used.
func testDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := repository.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "user_service_test_" + rand.Text()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal("create test schema failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("remove isolated test schema failed")
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("parse test configuration failed")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = quoted
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("create test pool failed")
	}
	t.Cleanup(pool.Close)
	sql, err := os.ReadFile("../../migrations/000001_accounts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return pool
}

func newInput(t *testing.T, username string) repository.CreateUser {
	t.Helper()
	hash, err := (auth.Argon2Hasher{}).Hash("Student9!")
	if err != nil {
		t.Fatal(err)
	}
	return repository.CreateUser{Username: username, Email: strings.ToLower(username) + "@nus.edu.sg", PasswordHash: hash}
}

func activateFixture(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	// Only integration fixtures bypass verification. No activation API exists.
	if _, err := pool.Exec(context.Background(), "UPDATE accounts SET active = true WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAccountCRUD(t *testing.T) {
	pool := testDatabase(t)
	repo := repository.NewPostgres(pool)
	ctx := context.Background()
	input := newInput(t, "Student")
	u, err := repo.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == "" || u.Active || u.Role != "USER" || u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatalf("incorrect defaults: %+v", u)
	}
	read, err := repo.FindByID(ctx, u.ID)
	if err != nil || read.Username != input.Username {
		t.Fatalf("read failed: %v", err)
	}
	var storedHash string
	if err := pool.QueryRow(ctx, "SELECT password_hash FROM accounts WHERE id = $1", u.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if err := (auth.Argon2Hasher{}).Verify("Student9!", storedHash); err != nil {
		t.Fatal(err)
	}
	name := "Student's new name'; DROP TABLE accounts; --"
	if _, err := repo.UpdateProfile(ctx, u.ID, repository.ProfilePatch{DisplayName: &name}); !errors.Is(err, repository.ErrInactive) {
		t.Fatalf("inactive update: %v", err)
	}
	activateFixture(t, pool, u.ID)
	mobile := "+65 9123 4567"
	updated, err := repo.UpdateProfile(ctx, u.ID, repository.ProfilePatch{DisplayName: &name, Mobile: &mobile})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != name || updated.Mobile != mobile || updated.UpdatedAt.Before(u.UpdatedAt) {
		t.Fatal("update values/timestamp incorrect")
	}
	empty := ""
	updated, err = repo.UpdateProfile(ctx, u.ID, repository.ProfilePatch{DisplayName: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "" || updated.Mobile != mobile || updated.Role != "USER" || updated.Email != u.Email || updated.Username != u.Username {
		t.Fatal("partial update affected protected or omitted fields")
	}
	if err := repo.SoftDelete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindByID(ctx, u.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleted account read: %v", err)
	}
	if _, err := repo.UpdateProfile(ctx, u.ID, repository.ProfilePatch{Mobile: &empty}); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleted update: %v", err)
	}
	if err := repo.SoftDelete(ctx, u.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	var active, deleted bool
	if err := pool.QueryRow(ctx, "SELECT active, deleted_at IS NOT NULL FROM accounts WHERE id = $1", u.ID).Scan(&active, &deleted); err != nil {
		t.Fatal(err)
	}
	if active || !deleted {
		t.Fatal("soft deletion did not retain an inactive tombstone")
	}
	for _, duplicate := range []repository.CreateUser{
		{Username: "student", Email: "different@nus.edu.sg", PasswordHash: input.PasswordHash},
		{Username: "different", Email: input.Email, PasswordHash: input.PasswordHash},
	} {
		if _, err := repo.Create(ctx, duplicate); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("deleted identity reused: %v", err)
		}
	}
	for _, id := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if _, err := repo.FindByID(ctx, id); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing read: %v", err)
		}
		if _, err := repo.UpdateProfile(ctx, id, repository.ProfilePatch{DisplayName: &name}); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing update: %v", err)
		}
		if err := repo.SoftDelete(ctx, id); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing delete: %v", err)
		}
	}
}

func TestPostgresConcurrentUniqueness(t *testing.T) {
	pool := testDatabase(t)
	repo := repository.NewPostgres(pool)
	input := newInput(t, "unused")
	for _, dimension := range []string{"username", "email"} {
		t.Run(dimension, func(t *testing.T) {
			const count = 8
			results := make(chan error, count)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < count; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					candidate := input
					candidate.Username = fmt.Sprintf("%s_%d", dimension, i)
					candidate.Email = fmt.Sprintf("%s_%d@nus.edu.sg", dimension, i)
					if dimension == "username" {
						candidate.Username = "SameStudent"
						if i%2 == 0 {
							candidate.Username = strings.ToLower(candidate.Username)
						}
					} else {
						candidate.Email = "same@nus.edu.sg"
					}
					<-start
					_, err := repo.Create(context.Background(), candidate)
					results <- err
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			success, conflicts := 0, 0
			for err := range results {
				if err == nil {
					success++
				} else if errors.Is(err, repository.ErrConflict) {
					conflicts++
				} else {
					t.Errorf("unexpected error: %v", err)
				}
			}
			if success != 1 || conflicts != count-1 {
				t.Fatalf("got %d successes and %d conflicts", success, conflicts)
			}
		})
	}
}

func TestPostgresServiceFlow(t *testing.T) {
	pool := testDatabase(t)
	repo := repository.NewPostgres(pool)
	svc := service.NewUserService(repo, nil)
	ctx := context.Background()
	input := service.Registration{Username: " Student ", Email: " STUDENT@U.NUS.EDU.SG ", Password: "Student9!", PasswordConfirmation: "Student9!"}
	u, err := svc.Register(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "Student" || u.Email != "student@u.nus.edu.sg" || u.Active || u.Role != "USER" {
		t.Fatal("registration normalization/defaults incorrect")
	}
	if _, err := svc.GetProfile(ctx, u.ID); !errors.Is(err, service.ErrInactive) {
		t.Fatalf("inactive read: %v", err)
	}
	input.Username = "student"
	if _, err := svc.Register(ctx, input); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("duplicate registration: %v", err)
	}
	activateFixture(t, pool, u.ID)
	name := "New name"
	profile, err := svc.UpdateProfile(ctx, u.ID, service.ProfileUpdate{DisplayName: &name})
	if err != nil {
		t.Fatal(err)
	}
	if profile.DisplayName != name || profile.CreditBalance != nil {
		t.Fatal("profile fields incorrect")
	}
	if err := svc.DeleteAccount(ctx, u.ID, false); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("confirmation: %v", err)
	}
	if err := svc.DeleteAccount(ctx, u.ID, true); !errors.Is(err, service.ErrUnavailable) {
		t.Fatalf("deletion must be blocked: %v", err)
	}
	if _, err := svc.GetProfile(ctx, u.ID); err != nil {
		t.Fatalf("blocked deletion modified account: %v", err)
	}
	if err := repo.SoftDelete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetProfile(ctx, u.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("deleted profile: %v", err)
	}
}

func TestPostgresMigrationRollback(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	down, err := os.ReadFile("../../migrations/000001_accounts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../migrations/000001_accounts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.NewPostgres(pool).Create(ctx, newInput(t, "after_rollback")); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDoesNotExposeCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, url := range []string{"", "postgres://user:do-not-expose@host:badport/db", "postgres://user:do-not-expose@localhost:1/db"} {
		pool, err := repository.Open(ctx, url)
		if pool != nil {
			pool.Close()
		}
		if err == nil {
			t.Fatal("invalid connection succeeded")
		}
		if strings.Contains(err.Error(), "do-not-expose") {
			t.Fatal("credentials leaked")
		}
	}
}
