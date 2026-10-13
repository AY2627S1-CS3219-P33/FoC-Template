package supplierrepo

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	db "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/generated"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

var (
	unknownBuilding     = supplier.FieldViolation{Field: "building", Message: "must be an existing building"}
	unknownSupplierType = supplier.FieldViolation{Field: "type", Message: "must be an existing supplier type"}
)

// validateAndResolve validates details and resolves their building and type
// inside the caller's transaction. Lookup violations are reported alongside
// field violations in one *supplier.ValidationError, so a request learns
// every problem at once.
func validateAndResolve(ctx context.Context, queries *db.Queries, details supplier.Details) (supplier.Details, error) {
	var violations []supplier.FieldViolation
	if err := supplier.ValidateDetails(details); err != nil {
		validation := &supplier.ValidationError{}
		if !errors.As(err, &validation) {
			return supplier.Details{}, err
		}
		violations = validation.Violations
	}
	details, lookupViolations, err := resolveLookups(ctx, queries, details, violations)
	if err != nil {
		return supplier.Details{}, err
	}
	violations = append(violations, lookupViolations...)
	if len(violations) > 0 {
		return supplier.Details{}, &supplier.ValidationError{Violations: violations}
	}
	return details, nil
}

// resolveLookups sets details.Building/BuildingID and Type/TypeID to the live
// lookup rows whose keys match supplier.LookupKey of the given names. The
// returned names are canonical, so "com2" resolves to an existing "Com 2".
// Administrators may only assign existing, unretired rows; a name with no
// live row is returned as a violation, never created.
//
// Each resolved row stays locked FOR SHARE until the caller's transaction
// ends, which serialises the write with RetireLookup's FOR UPDATE.
//
// A field whose ID is already set is left unchanged; Update relies on this to
// keep an unpatched, possibly retired, lookup. A field that already has a
// violation in invalid is not resolved.
func resolveLookups(
	ctx context.Context,
	queries *db.Queries,
	details supplier.Details,
	invalid []supplier.FieldViolation,
) (supplier.Details, []supplier.FieldViolation, error) {
	var violations []supplier.FieldViolation
	if details.BuildingID == "" && !hasViolation(invalid, unknownBuilding.Field) {
		row, err := queries.ResolveBuildingByKey(ctx, details.Building)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			violations = append(violations, unknownBuilding)
		case err != nil:
			return supplier.Details{}, nil, fmt.Errorf("resolve building: %w", err)
		default:
			details.BuildingID, details.Building = row.ID, row.Name
		}
	}
	if details.TypeID == "" && !hasViolation(invalid, unknownSupplierType.Field) {
		row, err := queries.ResolveSupplierTypeByKey(ctx, details.Type)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			violations = append(violations, unknownSupplierType)
		case err != nil:
			return supplier.Details{}, nil, fmt.Errorf("resolve supplier type: %w", err)
		default:
			details.TypeID, details.Type = row.ID, row.Name
		}
	}
	return details, violations, nil
}

func hasViolation(violations []supplier.FieldViolation, field string) bool {
	return slices.ContainsFunc(violations, func(violation supplier.FieldViolation) bool {
		return violation.Field == field
	})
}
