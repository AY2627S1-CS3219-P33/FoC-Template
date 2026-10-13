package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrincipalPermissionsAreExplicit_NFR3_3(t *testing.T) {
	user := Principal{Subject: "user-1", Kind: UserToken, Roles: []Role{RoleUser}, Permissions: []Permission{UseErrands}}
	admin := Principal{Subject: "admin-1", Kind: UserToken, Roles: []Role{RoleAdministrator}, Permissions: []Permission{UseErrands, ReviewErrands}}
	service := Principal{Subject: "client@clients", Kind: ServiceToken, Permissions: []Permission{InternalAccess}}

	require.True(t, user.Has(UseErrands))
	require.False(t, user.Has(ReviewErrands))
	require.False(t, user.Has(InternalAccess))
	require.True(t, admin.Has(UseErrands))
	require.True(t, admin.Has(ReviewErrands))
	require.True(t, service.Has(InternalAccess))
	require.False(t, service.Has(UseErrands))
	require.False(t, (Principal{}).Has(UseErrands))
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
	principal := Principal{Subject: "admin-1", Kind: UserToken, Roles: []Role{RoleAdministrator}, Permissions: []Permission{UseErrands, ReviewErrands}}
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
	require.False(t, roleOnly.Has(UseErrands))
	require.False(t, roleOnly.Has(ReviewErrands))
	require.False(t, (Principal{Permissions: []Permission{UseErrands}}).Has(UseErrands))
	require.False(t, (Principal{Subject: "user", Permissions: []Permission{"unknown"}}).Has("unknown"))
	require.False(t, (Principal{Subject: "user", Permissions: []Permission{"suppliers:manage"}}).Has("suppliers:manage"))
}
