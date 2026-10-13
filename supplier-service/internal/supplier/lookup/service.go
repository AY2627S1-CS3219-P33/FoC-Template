package lookup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// Kind describes one managed list: its route, name bound, and the stable
// error codes that identify it to clients.
type Kind struct {
	lookup        supplier.LookupKind
	path          string
	idParameter   string
	maxNameLength int
	notFound      apperror.Code
	conflict      apperror.Code
	inUse         apperror.Code
}

// The name bounds match the buildings.name and supplier_types.name columns
// and the building and type bounds of supplier writes.
var (
	Buildings = Kind{
		lookup: supplier.BuildingLookup, path: "/buildings", idParameter: "buildingId", maxNameLength: 120,
		notFound: apperror.BuildingNotFound, conflict: apperror.BuildingConflict, inUse: apperror.BuildingInUse,
	}
	SupplierTypes = Kind{
		lookup: supplier.SupplierTypeLookup, path: "/supplier-types", idParameter: "supplierTypeId", maxNameLength: 64,
		notFound: apperror.SupplierTypeNotFound, conflict: apperror.SupplierTypeConflict, inUse: apperror.SupplierTypeInUse,
	}
)

type Service struct {
	store supplier.LookupStore
}

func NewService(store supplier.LookupStore) Service {
	if store == nil {
		panic("lookup: store is required")
	}
	return Service{store: store}
}

// List returns live rows, and retired rows too when includeRetired is set,
// ordered by lookup key then ID and capped at supplier.MaximumLookupListSize.
func (s Service) List(ctx context.Context, principal auth.Principal, kind Kind, includeRetired bool) ([]supplier.Lookup, error) {
	if err := requireRead(principal); err != nil {
		return nil, err
	}
	lookups, err := s.store.ListLookups(ctx, kind.lookup, includeRetired, supplier.MaximumLookupListSize)
	return lookups, kind.mapError(err)
}

// Create stores a trimmed name. A live row with the same lookup key is a
// conflict even when the spelling differs, so "Com2" conflicts with "Com 2".
func (s Service) Create(ctx context.Context, principal auth.Principal, kind Kind, name string, now time.Time) (supplier.Lookup, error) {
	if err := requireManage(principal); err != nil {
		return supplier.Lookup{}, err
	}
	name, err := kind.validateName(name)
	if err != nil {
		return supplier.Lookup{}, err
	}
	created, err := s.store.CreateLookup(ctx, kind.lookup, name, now)
	return created, kind.mapError(err)
}

// Retire hides a row from new assignments. Existing versions keep resolving
// it. Retiring an already retired row succeeds without changing it.
func (s Service) Retire(ctx context.Context, principal auth.Principal, kind Kind, id supplier.LookupID, now time.Time) error {
	if err := requireManage(principal); err != nil {
		return err
	}
	if !supplier.ValidLookupID(id) {
		return &apperror.Error{Code: apperror.InvalidArgument, Message: kind.idParameter + " must be a UUID"}
	}
	return kind.mapError(s.store.RetireLookup(ctx, kind.lookup, id, now))
}

func (k Kind) validateName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	message := ""
	switch {
	case trimmed == "":
		message = "is required"
	case utf8.RuneCountInString(trimmed) > k.maxNameLength:
		message = "is too long"
	case supplier.LookupKey(trimmed) == "":
		message = "must contain a letter or digit"
	}
	if message != "" {
		return "", &supplier.ValidationError{Violations: []supplier.FieldViolation{{Field: "name", Message: message}}}
	}
	return trimmed, nil
}

// mapError converts kind-neutral store errors to this kind's stable codes.
func (k Kind) mapError(err error) error {
	noun := string(k.lookup)
	var conflict *supplier.LookupConflictError
	var inUse *supplier.LookupInUseError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, supplier.ErrLookupNotFound):
		return &apperror.Error{Code: k.notFound, Message: noun + " not found"}
	case errors.As(err, &conflict):
		message := fmt.Sprintf("a %s with the same lookup key already exists", noun)
		if conflict.Existing.Name != "" {
			message = fmt.Sprintf("%s %q (%s) already exists with the same lookup key", noun, conflict.Existing.Name, conflict.Existing.ID)
		}
		return &apperror.Error{Code: k.conflict, Message: message}
	case errors.As(err, &inUse):
		return &apperror.Error{Code: k.inUse, Message: fmt.Sprintf(
			"%s %q is used by %d supplier(s); move them to another %s before retiring it",
			noun, inUse.Lookup.Name, inUse.Suppliers, noun)}
	}
	return err
}

func requireRead(principal auth.Principal) error {
	if principal.Subject == "" {
		return &apperror.Error{Code: apperror.Unauthenticated, Message: "authentication is required"}
	}
	if !principal.Has(auth.ReadSuppliers) {
		return forbidden()
	}
	return nil
}

func requireManage(principal auth.Principal) error {
	if err := requireRead(principal); err != nil {
		return err
	}
	if !principal.Has(auth.ManageSuppliers) {
		return forbidden()
	}
	return nil
}

func forbidden() error {
	return &apperror.Error{Code: apperror.Forbidden, Message: "insufficient permissions for this operation"}
}
