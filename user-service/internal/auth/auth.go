// Package auth implements password hashing and defines future authentication contracts.
package auth

import "context"

// PasswordHasher will hash passwords and verify encoded hashes.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(password, encodedHash string) error
}

// Sessions describes the future session lifecycle.
// TODO: Specify expiration, rotation, storage, and error semantics before implementation.
type Sessions interface {
	Create(ctx context.Context, userID string) (sessionID string, err error)
	Resolve(ctx context.Context, sessionID string) (userID string, err error)
	Revoke(ctx context.Context, sessionID string) error
}

// TokenGenerator will provide tokens for future verification and reset workflows.
// TODO: Specify secure generation and lifecycle requirements before implementation.
type TokenGenerator interface {
	Generate() (string, error)
}
