package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidHash      = errors.New("invalid or unsupported password hash")
	ErrPasswordMismatch = errors.New("password does not match")
)

const (
	// OWASP's Argon2id minimum: 19 MiB, two iterations, one lane.
	passwordMemory     = 19 * 1024
	passwordIterations = 2
	passwordThreads    = 1
	passwordSaltBytes  = 16
	passwordKeyBytes   = 32
	passwordPrefix     = "$argon2id$v=19$m=19456,t=2,p=1$"
	MaxPasswordBytes   = 1024
)

// Argon2Hasher uses one versioned parameter set. Verification rejects unknown
// parameters before allocating memory. Supporting other sets requires an explicit
// future migration policy, not trusting costs read from a stored string.
type Argon2Hasher struct{}

var _ PasswordHasher = Argon2Hasher{}

func (Argon2Hasher) Hash(password string) (string, error) {
	if len(password) == 0 || len(password) > MaxPasswordBytes {
		return "", errors.New("password length is outside hashing limits")
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("password salt generation failed")
	}
	key := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordThreads, passwordKeyBytes)
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func (Argon2Hasher) Verify(password, encodedHash string) error {
	// Bound inputs before splitting/decoding or running the expensive KDF.
	if len(encodedHash) != len(passwordPrefix)+22+1+43 || !strings.HasPrefix(encodedHash, passwordPrefix) {
		return ErrInvalidHash
	}
	parts := strings.Split(strings.TrimPrefix(encodedHash, passwordPrefix), "$")
	if len(parts) != 2 {
		return ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[0])
	if err != nil || len(salt) != passwordSaltBytes {
		return ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(want) != passwordKeyBytes {
		return ErrInvalidHash
	}
	if len(password) == 0 || len(password) > MaxPasswordBytes {
		return ErrPasswordMismatch
	}
	got := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordThreads, passwordKeyBytes)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}
