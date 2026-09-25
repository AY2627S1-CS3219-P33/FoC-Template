package auth

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrincipalPermissionsComeFromTrustedRoles(t *testing.T) {
	user := Principal{Subject: "user-1", Roles: []Role{RoleUser}}
	admin := Principal{Subject: "admin-1", Roles: []Role{RoleAdministrator}}

	require.True(t, user.Has(ReadSuppliers))
	require.False(t, user.Has(ManageSuppliers))
	require.True(t, admin.Has(ReadSuppliers))
	require.True(t, admin.Has(ManageSuppliers))
	require.False(t, (Principal{}).Has(ReadSuppliers))
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
