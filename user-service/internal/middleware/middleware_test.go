package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/auth0/go-jwt-middleware/v3/core"
	"github.com/auth0/go-jwt-middleware/v3/validator"
)

func TestNewAuth0RejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct{ domain, audience string }{
		{"", "audience"}, {"https://example.auth0.com", "audience"}, {"example.auth0.com", ""},
	} {
		if _, err := NewAuth0(test.domain, test.audience); err == nil {
			t.Fatalf("accepted domain=%q audience=%q", test.domain, test.audience)
		}
	}
}

func TestSubjectAndBearerToken(t *testing.T) {
	claims := &validator.ValidatedClaims{RegisteredClaims: validator.RegisteredClaims{Subject: "auth0|student"}}
	ctx := core.SetClaims(context.Background(), claims)
	if subject, ok := Subject(ctx); !ok || subject != "auth0|student" {
		t.Fatalf("subject=%q ok=%v", subject, ok)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer access-token")
	if token, ok := BearerToken(request); !ok || token != "access-token" {
		t.Fatalf("token=%q ok=%v", token, ok)
	}
}
