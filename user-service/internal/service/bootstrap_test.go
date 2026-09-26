package service

import (
	"context"
	"errors"
	"testing"

	"user-service/internal/repository"
)

type bootstrapStub struct {
	completed, exists, created, marked bool
	stateErr, createErr, markErr       error
	input                              repository.CreateAuth0User
}

func (b *bootstrapStub) WithBootstrapLock(ctx context.Context, fn func(repository.BootstrapTransaction) error) error {
	return fn(b)
}
func (b *bootstrapStub) State(context.Context) (bool, bool, error) {
	return b.completed, b.exists, b.stateErr
}
func (b *bootstrapStub) CreateSuperAdmin(_ context.Context, input repository.CreateAuth0User) error {
	b.created = true
	b.input = input
	return b.createErr
}
func (b *bootstrapStub) MarkCompleted(context.Context) error { b.marked = true; return b.markErr }

func TestBootstrapDecisions(t *testing.T) {
	sentinel := errors.New("failure")
	for _, tc := range []struct {
		name            string
		stub            bootstrapStub
		wantErr         error
		created, marked bool
	}{
		{name: "new", created: true, marked: true},
		{name: "complete", stub: bootstrapStub{completed: true, exists: true}},
		{name: "existing", stub: bootstrapStub{exists: true}, marked: true},
		{name: "recovery", stub: bootstrapStub{completed: true}, wantErr: ErrBootstrapRecovery},
		{name: "state failure", stub: bootstrapStub{stateErr: sentinel}, wantErr: sentinel},
		{name: "insert failure", stub: bootstrapStub{createErr: sentinel}, wantErr: sentinel, created: true},
		{name: "completion failure", stub: bootstrapStub{markErr: sentinel}, wantErr: sentinel, created: true, marked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := repository.CreateAuth0User{Subject: "auth0|admin", Username: "Admin", Email: " ADMIN@EXAMPLE.COM "}
			if tc.stub.exists || tc.stub.completed {
				input = repository.CreateAuth0User{}
			}
			err := BootstrapSuperAdmin(context.Background(), &tc.stub, input)
			if !errors.Is(err, tc.wantErr) || tc.stub.created != tc.created || tc.stub.marked != tc.marked {
				t.Fatalf("unexpected outcome: %v %+v", err, tc.stub)
			}
			if tc.stub.created && tc.stub.input.Email != "admin@example.com" {
				t.Fatal("email not normalized")
			}
		})
	}
}

func TestBootstrapValidation(t *testing.T) {
	for _, input := range []repository.CreateAuth0User{
		{}, {Subject: "invalid", Username: "admin", Email: "a@example.com"},
		{Subject: "auth0|admin", Email: "a@example.com"},
		{Subject: "auth0|admin", Username: "admin", Email: "invalid"},
		{Subject: "auth0|admin", Username: "admin", Email: "Alice <a@example.com>"},
	} {
		stub := &bootstrapStub{}
		if err := BootstrapSuperAdmin(context.Background(), stub, input); !errors.Is(err, ErrValidation) || stub.created || stub.marked {
			t.Fatalf("invalid input accepted: %v", err)
		}
	}
}
