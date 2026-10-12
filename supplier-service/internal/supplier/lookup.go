package supplier

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// LookupKind selects one of the two managed lookup tables. Both have the same
// shape and rules; only their name bounds and error codes differ.
type LookupKind string

const (
	BuildingLookup     LookupKind = "building"
	SupplierTypeLookup LookupKind = "supplier type"
)

// MaximumLookupListSize caps a lookup list. Lists are campus scale, so they
// are returned unpaginated; the cap only bounds the response.
const MaximumLookupListSize = 500

// LookupID is a system-generated UUID of a building or supplier-type row.
type LookupID string

// Lookup is a building or supplier-type row. Its name never changes; a
// retired row stays referenced by existing versions but cannot be assigned.
type Lookup struct {
	ID      LookupID
	Name    string
	Retired bool
}

// ErrLookupNotFound means no row of the requested kind has the given ID.
var ErrLookupNotFound = errors.New("lookup not found")

// LookupConflictError means a live row already has the new name's lookup key.
// Existing.Name is empty if that row was retired before it could be read.
type LookupConflictError struct {
	Existing Lookup
}

func (e *LookupConflictError) Error() string {
	if e.Existing.Name == "" {
		return "a live lookup with the same lookup key already exists"
	}
	return fmt.Sprintf("lookup %q has the same lookup key", e.Existing.Name)
}

// LookupInUseError means the current version of at least one non-deleted
// supplier references the row, so it cannot be retired.
type LookupInUseError struct {
	Lookup    Lookup
	Suppliers int
}

func (e *LookupInUseError) Error() string {
	return fmt.Sprintf("lookup %q is used by %d suppliers", e.Lookup.Name, e.Suppliers)
}

// LookupStore manages the building and supplier-type lists.
//
// ListLookups orders rows by lookup key then ID and returns at most limit
// rows; retired rows are included only when requested. CreateLookup inserts
// an already validated name and returns *LookupConflictError when a live row
// has the same key; the database's live-key unique index decides, not a
// prior read. RetireLookup locks the row, returns ErrLookupNotFound for an
// unknown ID and *LookupInUseError while a current supplier version uses it,
// and treats an already retired row as success. Writers that assign a lookup
// must lock the resolved row for share so they serialise with retirement.
type LookupStore interface {
	ListLookups(ctx context.Context, kind LookupKind, includeRetired bool, limit int) ([]Lookup, error)
	CreateLookup(ctx context.Context, kind LookupKind, name string, now time.Time) (Lookup, error)
	RetireLookup(ctx context.Context, kind LookupKind, id LookupID, now time.Time) error
}
