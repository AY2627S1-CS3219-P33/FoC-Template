package service

import (
	"context"
	"errors"
	"testing"

	"user-service/internal/auth"
	"user-service/internal/repository"
)

type auth0RepositorySpy struct {
	user      *repository.User
	findErr   error
	createErr error
	created   repository.CreateAuth0User
	creates   int
}

func (r *auth0RepositorySpy) FindByAuth0Subject(context.Context, string) (*repository.User, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.user, nil
}

func (r *auth0RepositorySpy) CreateAuth0(_ context.Context, input repository.CreateAuth0User) (*repository.User, bool, error) {
	r.creates++
	r.created = input
	if r.createErr != nil {
		return nil, false, r.createErr
	}
	return &repository.User{
		ID: "account-id", Auth0Subject: input.Subject, Username: input.Username,
		Email: input.Email, DisplayName: input.DisplayName, Role: "USER", Active: true,
	}, true, nil
}

type userInfoStub struct {
	profile auth.UserInfo
	err     error
	calls   int
}

func (s *userInfoStub) Get(context.Context, string) (auth.UserInfo, error) {
	s.calls++
	return s.profile, s.err
}

func TestAuth0ProvisionCreatesEligibleAccount(t *testing.T) {
	repo := &auth0RepositorySpy{findErr: repository.ErrNotFound}
	profiles := &userInfoStub{profile: auth.UserInfo{
		Subject: "auth0|student", Email: " Student@U.NUS.EDU.SG ", EmailVerified: true,
		Nickname: " Campus Friend ", Name: " Student Name ",
	}}
	result, created, err := NewAuth0Provisioner(repo, profiles).Provision(context.Background(), "auth0|student", "token")
	if err != nil || !created {
		t.Fatalf("provision failed: created=%v err=%v", created, err)
	}
	if result.Username != "Campus Friend" || result.Email != "student@u.nus.edu.sg" || result.DisplayName != "Student Name" || !result.Active {
		t.Fatalf("incorrect profile: %+v", result)
	}
	if repo.created.Subject != "auth0|student" || repo.creates != 1 || profiles.calls != 1 {
		t.Fatal("trusted identity was not persisted exactly once")
	}
}

func TestAuth0ProvisionUsesEmailFallback(t *testing.T) {
	repo := &auth0RepositorySpy{findErr: repository.ErrNotFound}
	profiles := &userInfoStub{profile: auth.UserInfo{
		Subject: "auth0|student", Email: "student@nus.edu.sg", EmailVerified: true,
		Nickname: "bad\x00nickname",
	}}
	result, _, err := NewAuth0Provisioner(repo, profiles).Provision(context.Background(), "auth0|student", "token")
	if err != nil || result.Username != "student" {
		t.Fatalf("fallback failed: profile=%+v err=%v", result, err)
	}
}

func TestAuth0ProvisionRejectsUntrustedIdentity(t *testing.T) {
	tests := []struct {
		name    string
		profile auth.UserInfo
		want    error
	}{
		{name: "subject mismatch", profile: auth.UserInfo{Subject: "auth0|other", Email: "student@nus.edu.sg", EmailVerified: true}, want: ErrIdentityMismatch},
		{name: "unverified", profile: auth.UserInfo{Subject: "auth0|student", Email: "student@nus.edu.sg"}, want: ErrIdentityUnverified},
		{name: "not NUS", profile: auth.UserInfo{Subject: "auth0|student", Email: "student@notnus.edu.sg", EmailVerified: true}, want: ErrIdentityIneligible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &auth0RepositorySpy{findErr: repository.ErrNotFound}
			profiles := &userInfoStub{profile: test.profile}
			_, _, err := NewAuth0Provisioner(repo, profiles).Provision(context.Background(), "auth0|student", "token")
			if !errors.Is(err, test.want) || repo.creates != 0 {
				t.Fatalf("got err=%v creates=%d", err, repo.creates)
			}
		})
	}
}

func TestAuth0ProvisionReturnsExistingAccountWithoutUserInfo(t *testing.T) {
	repo := &auth0RepositorySpy{user: &repository.User{
		ID: "account-id", Auth0Subject: "auth0|student", Username: "student",
		Email: "student@nus.edu.sg", Role: "USER", Active: true,
	}}
	profiles := &userInfoStub{err: errors.New("must not be called")}
	result, created, err := NewAuth0Provisioner(repo, profiles).Provision(context.Background(), "auth0|student", "token")
	if err != nil || created || result.ID != "account-id" || profiles.calls != 0 {
		t.Fatalf("existing account lookup failed: profile=%+v created=%v calls=%d err=%v", result, created, profiles.calls, err)
	}
}
