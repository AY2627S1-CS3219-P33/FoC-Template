package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"user-service/internal/auth"
	"user-service/internal/repository"
)

// These spies exist only in tests; there is no in-memory production repository.
type repositorySpy struct {
	user                      repository.User
	created                   repository.CreateUser
	createErr                 error
	updateErr                 error
	creates, updates, deletes int
}

func (r *repositorySpy) Create(_ context.Context, input repository.CreateUser) (*repository.User, error) {
	r.creates++
	r.created = input
	if r.createErr != nil {
		return nil, r.createErr
	}
	r.user = repository.User{ID: "test-identity", Username: input.Username, Email: input.Email, Role: "USER"}
	return &r.user, nil
}
func (r *repositorySpy) FindByID(_ context.Context, id string) (*repository.User, error) {
	if id != r.user.ID {
		return nil, repository.ErrNotFound
	}
	return &r.user, nil
}
func (r *repositorySpy) UpdateProfile(_ context.Context, id string, patch repository.ProfilePatch) (*repository.User, error) {
	r.updates++
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	if id != r.user.ID {
		return nil, repository.ErrNotFound
	}
	if patch.DisplayName != nil {
		r.user.DisplayName = *patch.DisplayName
	}
	if patch.Mobile != nil {
		r.user.Mobile = *patch.Mobile
	}
	return &r.user, nil
}
func (r *repositorySpy) SoftDelete(context.Context, string) error { r.deletes++; return nil }

func registration() Registration {
	return Registration{Username: "student", Email: "student@u.nus.edu.sg", Password: "Student9!", PasswordConfirmation: "Student9!"}
}

func TestRegistrationValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Registration)
	}{
		{"empty username", func(r *Registration) { r.Username = "  " }},
		{"long username", func(r *Registration) { r.Username = strings.Repeat("x", 65) }},
		{"username control", func(r *Registration) { r.Username = "student\x00" }},
		{"invalid UTF8", func(r *Registration) { r.Username = "\xff" }},
		{"suffix spoof", func(r *Registration) { r.Email = "a@notnus.edu.sg" }},
		{"trailing domain", func(r *Registration) { r.Email = "a@nus.edu.sg.attacker.test" }},
		{"empty label", func(r *Registration) { r.Email = "a@.nus.edu.sg" }},
		{"bad label", func(r *Registration) { r.Email = "a@-u.nus.edu.sg" }},
		{"display address", func(r *Registration) { r.Email = "Student <a@nus.edu.sg>" }},
		{"multiple addresses", func(r *Registration) { r.Email = "a@nus.edu.sg,b@nus.edu.sg" }},
		{"mismatch", func(r *Registration) { r.PasswordConfirmation += " " }},
		{"short", func(r *Registration) { r.Password = "Aa1!"; r.PasswordConfirmation = r.Password }},
		{"no upper", func(r *Registration) { r.Password = "student9!"; r.PasswordConfirmation = r.Password }},
		{"no lower", func(r *Registration) { r.Password = "STUDENT9!"; r.PasswordConfirmation = r.Password }},
		{"no number", func(r *Registration) { r.Password = "Student!!"; r.PasswordConfirmation = r.Password }},
		{"no symbol", func(r *Registration) { r.Password = "Student99 "; r.PasswordConfirmation = r.Password }},
		{"oversized password", func(r *Registration) {
			r.Password = "Aa1!" + strings.Repeat("x", 1024)
			r.PasswordConfirmation = r.Password
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := registration()
			tc.change(&input)
			repo := &repositorySpy{}
			_, err := NewUserService(repo, nil).Register(context.Background(), input)
			var validation *ValidationError
			if !errors.Is(err, ErrValidation) || !errors.As(err, &validation) {
				t.Fatalf("expected validation error, got %v", err)
			}
			if repo.creates != 0 {
				t.Fatal("invalid registration persisted")
			}
		})
	}
	for _, email := range []string{"a@nus.edu.sg", "a@u.nus.edu.sg", "a@alumni.nus.edu.sg"} {
		input := registration()
		input.Email = email
		if err := validateRegistration(input); err != nil {
			t.Errorf("valid NUS email rejected: %v", err)
		}
	}
}

func TestRegisterNormalizesAndHashes(t *testing.T) {
	repo := &repositorySpy{}
	svc := NewUserService(repo, nil)
	input := registration()
	input.Username = " Student "
	input.Email = " STUDENT@U.NUS.EDU.SG "
	result, err := svc.Register(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Username != "Student" || result.Email != "student@u.nus.edu.sg" || result.Role != "USER" || result.Active {
		t.Fatalf("unexpected profile: %+v", result)
	}
	if err := (auth.Argon2Hasher{}).Verify(input.Password, repo.created.PasswordHash); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "argon2") {
		t.Fatal("credentials leaked in public profile")
	}
	if result.CreditBalance != nil {
		t.Fatal("credit balance invented")
	}
	if _, err := svc.GetProfile(context.Background(), result.ID); !errors.Is(err, ErrInactive) {
		t.Fatalf("pending account read: %v", err)
	}
	repo.createErr = repository.ErrConflict
	if _, err := svc.Register(context.Background(), input); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict lost: %v", err)
	}
}

type balanceStub struct {
	value string
	err   error
}

func (b balanceStub) Balance(context.Context, string) (string, error) { return b.value, b.err }

func TestProfileAndDeletionBoundaries(t *testing.T) {
	repo := &repositorySpy{user: repository.User{ID: "self", Username: "student", Email: "student@nus.edu.sg", Role: "USER", Active: true, Mobile: "12345678"}}
	svc := NewUserService(repo, nil)
	ctx := context.Background()
	name := "New display name"
	result, err := svc.UpdateProfile(ctx, "self", ProfileUpdate{DisplayName: &name})
	if err != nil {
		t.Fatal(err)
	}
	if result.DisplayName != name || result.Mobile != "12345678" || result.Role != "USER" || result.Email != repo.user.Email {
		t.Fatal("partial update modified other fields")
	}
	if result.CreditBalance != nil {
		t.Fatal("missing credit adapter fabricated balance")
	}
	for _, provider := range []balanceStub{{value: "12.50"}, {err: errors.New("offline")}} {
		profile, err := NewUserService(repo, provider).GetProfile(ctx, "self")
		if err != nil {
			t.Fatal(err)
		}
		if provider.err != nil && profile.CreditBalance != nil {
			t.Fatal("unavailable credit balance was replaced")
		}
		if provider.err == nil && (profile.CreditBalance == nil || *profile.CreditBalance != provider.value) {
			t.Fatal("credit result not returned")
		}
	}
	if err := svc.DeleteAccount(ctx, "self", false); !errors.Is(err, ErrValidation) {
		t.Fatalf("confirmation: %v", err)
	}
	if err := svc.DeleteAccount(ctx, "self", true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("deletion must remain blocked: %v", err)
	}
	if repo.deletes != 0 {
		t.Fatal("business deletion reached persistence")
	}
	if _, err := svc.GetProfile(ctx, ""); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing identity: %v", err)
	}
	if _, err := svc.GetProfile(ctx, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
	repo.user.Active = false
	if _, err := svc.UpdateProfile(ctx, "self", ProfileUpdate{DisplayName: &name}); !errors.Is(err, ErrInactive) {
		t.Fatalf("inactive update: %v", err)
	}
	if repo.updates != 1 {
		t.Fatal("inactive update reached repository")
	}
	repo.user.Active = true
	repo.updateErr = repository.ErrInactive
	if _, err := svc.UpdateProfile(ctx, "self", ProfileUpdate{DisplayName: &name}); !errors.Is(err, ErrInactive) {
		t.Fatalf("concurrent deactivation error lost: %v", err)
	}
}

func TestProfileValidation(t *testing.T) {
	repo := &repositorySpy{user: repository.User{ID: "self", Active: true}}
	svc := NewUserService(repo, nil)
	name := strings.Repeat("a", 101)
	mobile := strings.Repeat("1", 33)
	control := "a\x00b"
	for _, patch := range []ProfileUpdate{{}, {DisplayName: &name}, {Mobile: &mobile}, {DisplayName: &control}} {
		if _, err := svc.UpdateProfile(context.Background(), "self", patch); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid profile: %v", err)
		}
	}
	if repo.updates != 0 {
		t.Fatal("invalid update persisted")
	}
	empty := ""
	if _, err := svc.UpdateProfile(context.Background(), "self", ProfileUpdate{Mobile: &empty}); err != nil {
		t.Fatalf("explicit clearing failed: %v", err)
	}
}
