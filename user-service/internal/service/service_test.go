package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"user-service/internal/repository"
)

// These spies exist only in tests; there is no in-memory production repository.
type repositorySpy struct {
	user             repository.User
	updateErr        error
	updates, deletes int
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

type balanceStub struct {
	value string
	err   error
}

func (b balanceStub) Balance(context.Context, string) (string, error) { return b.value, b.err }

func TestProfileAndDeletionBoundaries(t *testing.T) {
	repo := &repositorySpy{user: repository.User{ID: "self", Username: "student", Email: "student@u.nus.edu", Role: "USER", Active: true, Mobile: "12345678"}}
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
