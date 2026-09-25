package auth

import "context"

type Role string

const (
	RoleUser          Role = "user"
	RoleAdministrator Role = "administrator"
)

type Permission string

const (
	ReadSuppliers   Permission = "suppliers:read"
	ManageSuppliers Permission = "suppliers:manage"
)

// Principal is trusted authentication output. Supplier use cases accept this
// value and never accept account IDs or roles from request payloads.
type Principal struct {
	Subject string
	Roles   []Role
}

func (p Principal) Has(permission Permission) bool {
	if p.Subject == "" {
		return false
	}
	if permission == ReadSuppliers {
		return true
	}
	if permission == ManageSuppliers {
		for _, role := range p.Roles {
			if role == RoleAdministrator {
				return true
			}
		}
	}
	return false
}

// Port verifies opaque credentials and returns trusted identity data. A future
// JWT validator, session service, or user-service client can implement it
// without changing supplier use cases.
type Port interface {
	Authenticate(context.Context, string) (Principal, error)
}
