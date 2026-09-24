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
	for _, migration := range []string{"000001_accounts.up.sql", "000002_auth0_identities.up.sql", "000003_auth0_only.up.sql"} {
		sql, err := os.ReadFile("../../migrations/" + migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	return pool
}

func TestPostgresAccountCRUD(t *testing.T) {
	pool := testDatabase(t)
	repo := repository.NewPostgres(pool)
	ctx := context.Background()
	input := repository.CreateAuth0User{
		Subject: "auth0|student", Username: "Student", Email: "student@u.nus.edu",
	}
	u, created, err := repo.CreateAuth0(ctx, input)
	if err != nil || !created {
		t.Fatal(err)
	}
	if u.ID == "" || !u.Active || u.Role != "USER" || u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatalf("incorrect defaults: %+v", u)
	}
	read, err := repo.FindByID(ctx, u.ID)
	if err != nil || read.Username != input.Username {
		t.Fatalf("read failed: %v", err)
	}
	name := "Student's new name'; DROP TABLE accounts; --"
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
	if _, _, err := repo.CreateAuth0(ctx, repository.CreateAuth0User{
		Subject: "auth0|different", Username: "student", Email: "different@u.nus.edu",
	}); err != nil {
		t.Fatalf("duplicate username rejected: %v", err)
	}
	if _, _, err := repo.CreateAuth0(ctx, repository.CreateAuth0User{
		Subject: "auth0|duplicate-email", Username: "different", Email: input.Email,
	}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("deleted email reused: %v", err)
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
	for _, dimension := range []string{"duplicate_username", "email"} {
		t.Run(dimension, func(t *testing.T) {
			const count = 8
			results := make(chan error, count)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < count; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					candidate := repository.CreateAuth0User{
						Subject:  fmt.Sprintf("auth0|%s-%d", dimension, i),
						Username: fmt.Sprintf("%s_%d", dimension, i),
						Email:    fmt.Sprintf("%s_%d@u.nus.edu", dimension, i),
					}
					if dimension == "duplicate_username" {
						candidate.Username = "SameStudent"
						if i%2 == 0 {
							candidate.Username = strings.ToLower(candidate.Username)
						}
					} else {
						candidate.Email = "same@u.nus.edu"
					}
					<-start
					_, _, err := repo.CreateAuth0(context.Background(), candidate)
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
			wantSuccess, wantConflicts := 1, count-1
			if dimension == "duplicate_username" {
				wantSuccess, wantConflicts = count, 0
			}
			if success != wantSuccess || conflicts != wantConflicts {
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
	u, created, err := repo.CreateAuth0(ctx, repository.CreateAuth0User{
		Subject: "auth0|service-flow", Username: "Student", Email: "student@u.nus.edu",
	})
	if err != nil || !created {
		t.Fatal(err)
	}
	if u.Username != "Student" || u.Email != "student@u.nus.edu" || !u.Active || u.Role != "USER" {
		t.Fatal("Auth0 provisioning defaults incorrect")
	}
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
	for _, migration := range []string{"000003_auth0_only.down.sql", "000002_auth0_identities.down.sql", "000001_accounts.down.sql", "000001_accounts.up.sql", "000002_auth0_identities.up.sql", "000003_auth0_only.up.sql"} {
		sql, err := os.ReadFile("../../migrations/" + migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	if _, _, err := repository.NewPostgres(pool).CreateAuth0(ctx, repository.CreateAuth0User{
		Subject: "auth0|after-rollback", Username: "after_rollback", Email: "after_rollback@u.nus.edu",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuth0Provisioning(t *testing.T) {
	pool := testDatabase(t)
	repo := repository.NewPostgres(pool)
	ctx := context.Background()
	input := repository.CreateAuth0User{
		Subject: "auth0|student-1", Username: "Shared Nickname",
		Email: "student1@u.nus.edu", DisplayName: "Student One",
	}
	created, wasCreated, err := repo.CreateAuth0(ctx, input)
	if err != nil || !wasCreated || !created.Active || created.Auth0Subject != input.Subject {
		t.Fatalf("create Auth0 account: created=%v user=%+v err=%v", wasCreated, created, err)
	}
	repeated, wasCreated, err := repo.CreateAuth0(ctx, input)
	if err != nil || wasCreated || repeated.ID != created.ID {
		t.Fatalf("idempotent Auth0 creation failed: created=%v user=%+v err=%v", wasCreated, repeated, err)
	}
	if _, _, err := repo.CreateAuth0(ctx, repository.CreateAuth0User{
		Subject: "auth0|student-2", Username: input.Username, Email: "student2@u.nus.edu",
	}); err != nil {
		t.Fatalf("duplicate Auth0 nickname rejected: %v", err)
	}
	if _, _, err := repo.CreateAuth0(ctx, repository.CreateAuth0User{
		Subject: "auth0|student-3", Username: "Another", Email: input.Email,
	}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("duplicate Auth0 email: %v", err)
	}
	var passwordColumnExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'accounts' AND column_name = 'password_hash'
	)`).Scan(&passwordColumnExists); err != nil {
		t.Fatal(err)
	}
	if passwordColumnExists {
		t.Fatal("local password column still exists")
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
