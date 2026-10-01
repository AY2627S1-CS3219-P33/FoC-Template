package query

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

type Service struct {
	reader supplier.Reader
}

func NewService(reader supplier.Reader) Service {
	if reader == nil {
		panic("query: reader is required")
	}
	return Service{reader: reader}
}

// List implements F2.1, F2.1.2 and F2.5.2 using the current-only repository read.
func (s Service) List(ctx context.Context, principal auth.Principal, filter supplier.ListFilter) (supplier.Page, error) {
	if !principal.Has(auth.ReadSuppliers) {
		return supplier.Page{}, authenticationRequired()
	}
	// OpenAPI length bounds apply before whitespace normalization.
	var violations []supplier.FieldViolation
	if utf8.RuneCountInString(filter.Query) > 120 {
		violations = append(violations, supplier.FieldViolation{Field: "q", Message: "is too long"})
	}
	if utf8.RuneCountInString(filter.Type) > 64 {
		violations = append(violations, supplier.FieldViolation{Field: "type", Message: "is too long"})
	}
	if len(violations) > 0 {
		return supplier.Page{}, &supplier.ValidationError{Violations: violations}
	}
	if err := supplier.ValidateListFilter(filter); err != nil {
		return supplier.Page{}, err
	}
	filter.Query = supplier.NormalizeName(filter.Query)
	filter.Type = strings.ToLower(strings.TrimSpace(filter.Type))
	filter.Limit = filter.PageSize()
	return s.reader.ListAvailable(ctx, filter)
}

func (s Service) Current(ctx context.Context, principal auth.Principal, id supplier.SupplierID) (supplier.Supplier, error) {
	if !principal.Has(auth.ReadSuppliers) {
		return supplier.Supplier{}, authenticationRequired()
	}
	if !supplier.ValidSupplierID(id) {
		return supplier.Supplier{}, &apperror.Error{Code: apperror.InvalidArgument, Message: "supplierId must be a UUID"}
	}
	return s.reader.GetCurrentAvailable(ctx, id)
}

func authenticationRequired() error {
	return &apperror.Error{Code: apperror.Unauthenticated, Message: "authentication is required"}
}
