package auth

import (
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
