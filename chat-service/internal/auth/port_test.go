// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-18
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrincipalPermissionsAreExplicit_NFR3_3(t *testing.T) {
	user := Principal{Subject: "user-1", Kind: UserToken, Roles: []Role{RoleUser}, Permissions: []Permission{UseChats}}
	service := Principal{Subject: "client@clients", Kind: ServiceToken, Permissions: []Permission{UseChats}}

	require.True(t, user.Has(UseChats))
	require.False(t, user.Has(Permission("orders:internal")), "unknown permissions are never granted")
	require.True(t, service.Has(UseChats), "Has does not check the token kind; RequirePermission does")
	require.False(t, (Principal{}).Has(UseChats))
}

func TestPrincipalTokenKinds(t *testing.T) {
	require.True(t, Principal{Kind: UserToken}.IsUser())
	require.False(t, Principal{Kind: UserToken}.IsService())
	require.True(t, Principal{Kind: ServiceToken}.IsService())
	require.False(t, Principal{Kind: ServiceToken}.IsUser())
	require.False(t, Principal{}.IsUser())
	require.False(t, Principal{}.IsService())
}

func TestPrincipalContextRoundTrip(t *testing.T) {
	principal := Principal{Subject: "admin-1", Kind: UserToken, Roles: []Role{RoleAdministrator}, Permissions: []Permission{UseChats}}
	ctx := WithPrincipal(context.Background(), principal)

	actual, ok := PrincipalFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, principal, actual)

	_, ok = PrincipalFromContext(context.Background())
	require.False(t, ok)
}

func TestAuthenticationFailuresHaveStableKinds(t *testing.T) {
	tests := []FailureKind{InvalidCredential, AccountDisabled, VerifierUnavailable}
	for _, kind := range tests {
		err := &AuthenticationError{Kind: kind}
		var authenticationError *AuthenticationError
		require.True(t, errors.As(err, &authenticationError))
		require.Equal(t, kind, authenticationError.Kind)
		require.NotContains(t, err.Error(), "secret-token")
	}
}

func TestRolesAndUnknownPermissionsNeverGrantAccess_NFR3_3(t *testing.T) {
	roleOnly := Principal{Subject: "auth0|admin", Kind: UserToken, Roles: []Role{RoleAdministrator}}
	require.False(t, roleOnly.Has(UseChats))
	require.False(t, (Principal{Permissions: []Permission{UseChats}}).Has(UseChats))
	require.False(t, (Principal{Subject: "user", Permissions: []Permission{"unknown"}}).Has("unknown"))
	require.False(t, (Principal{Subject: "user", Permissions: []Permission{"suppliers:manage"}}).Has("suppliers:manage"))
}
