package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrincipalPermissionsAreExplicit_NFR3_3(t *testing.T) {
	user := Principal{Subject: "user-1", Roles: []Role{RoleUser}, Permissions: []Permission{ReadSuppliers}}
	admin := Principal{Subject: "admin-1", Roles: []Role{RoleAdministrator}, Permissions: []Permission{ReadSuppliers, ManageSuppliers}}

	require.True(t, user.Has(ReadSuppliers))
	require.False(t, user.Has(ManageSuppliers))
	require.True(t, admin.Has(ReadSuppliers))
	require.True(t, admin.Has(ManageSuppliers))
	require.False(t, (Principal{}).Has(ReadSuppliers))
}

func TestPrincipalContextRoundTrip(t *testing.T) {
	principal := Principal{Subject: "admin-1", Roles: []Role{RoleAdministrator}, Permissions: []Permission{ReadSuppliers, ManageSuppliers}}
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
	roleOnly := Principal{Subject: "auth0|admin", Roles: []Role{RoleAdministrator}}
	require.False(t, roleOnly.Has(ReadSuppliers))
	require.False(t, roleOnly.Has(ManageSuppliers))
	require.False(t, (Principal{Permissions: []Permission{ReadSuppliers}}).Has(ReadSuppliers))
	require.False(t, (Principal{Subject: "user", Permissions: []Permission{"unknown"}}).Has("unknown"))
}
