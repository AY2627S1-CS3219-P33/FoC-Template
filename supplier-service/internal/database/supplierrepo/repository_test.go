package supplierrepo

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func TestMapWriteErrorMapsLookupConstraintsToFieldErrors(t *testing.T) {
	for _, test := range []struct {
		code, constraint string
		expected         supplier.FieldViolation
	}{
		{"23503", "supplier_versions_building_fkey", supplier.FieldViolation{Field: "building", Message: "must be an existing building"}},
		{"23503", "supplier_versions_supplier_type_fkey", supplier.FieldViolation{Field: "type", Message: "must be an existing supplier type"}},
		{"23514", "buildings_lookup_key_not_blank", supplier.FieldViolation{Field: "building", Message: "must contain a letter or digit"}},
		{"23514", "supplier_types_lookup_key_not_blank", supplier.FieldViolation{Field: "type", Message: "must contain a letter or digit"}},
		{"23514", "buildings_name_not_blank", supplier.FieldViolation{Field: "building", Message: "is required"}},
		{"23514", "supplier_types_name_not_blank", supplier.FieldViolation{Field: "type", Message: "is required"}},
	} {
		err := mapWriteError(&pgconn.PgError{Code: test.code, ConstraintName: test.constraint})
		validation := &supplier.ValidationError{}
		require.ErrorAs(t, err, &validation, test.constraint)
		require.Equal(t, []supplier.FieldViolation{test.expected}, validation.Violations)
	}

	require.ErrorIs(t, mapWriteError(&pgconn.PgError{Code: "23505", ConstraintName: normalizedNameConstraint}), apperror.ErrSupplierNameConflict)
	unrelated := &pgconn.PgError{Code: "23514", ConstraintName: "supplier_versions_hours_differ"}
	require.Same(t, unrelated, mapWriteError(unrelated))
	plain := errors.New("connection reset")
	require.Same(t, plain, mapWriteError(plain))
}
