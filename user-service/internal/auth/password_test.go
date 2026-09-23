package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	hasher := Argon2Hasher{}
	password := "A student password 9!"
	first, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, password) {
		t.Fatal("hash must be salted and must not contain plaintext")
	}
	if err := hasher.Verify(password, first); err != nil {
		t.Fatal(err)
	}
	if err := hasher.Verify(password+" ", first); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("wrong password: %v", err)
	}
	for _, hash := range []string{
		"", "plaintext", strings.Replace(first, "v=19", "v=18", 1),
		strings.Replace(first, "m=19456", "m=999999999", 1),
		first + "$extra", first[:len(first)-1],
		passwordPrefix + strings.Repeat("!", 22) + "$" + strings.Repeat("A", 43),
		passwordPrefix + strings.Repeat("A", 22) + "$" + strings.Repeat("!", 43),
	} {
		if err := hasher.Verify(password, hash); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("malformed hash accepted: %v", err)
		}
	}
	for _, password := range []string{"", strings.Repeat("x", MaxPasswordBytes+1)} {
		if _, err := hasher.Hash(password); err == nil {
			t.Error("invalid hash input accepted")
		}
		if err := hasher.Verify(password, first); !errors.Is(err, ErrPasswordMismatch) {
			t.Errorf("invalid verification input: %v", err)
		}
	}
}
