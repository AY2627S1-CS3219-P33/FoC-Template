package supplierrepo

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// observe intentionally logs no arguments, identifiers, record fields, or
// underlying error text. PostgreSQL errors can contain connection details or
// SQL values, while application error codes and SQLSTATE classes are safe.
func (r *Repository) observe(operation string, started time.Time, operationError *error) {
	if r.logger == nil {
		return
	}
	attributes := []any{
		"component", "supplier_repository",
		"operation", operation,
		"duration_ms", time.Since(started).Milliseconds(),
	}
	if *operationError == nil {
		r.logger.Debug("supplier repository operation completed", attributes...)
		return
	}
	r.logger.Error(
		"supplier repository operation failed",
		append(attributes, "error_kind", repositoryErrorKind(*operationError))...,
	)
}

func repositoryErrorKind(err error) string {
	var validationError *supplier.ValidationError
	if errors.As(err, &validationError) {
		return "validation"
	}
	var applicationError *apperror.Error
	if errors.As(err, &applicationError) {
		return string(applicationError.Code)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return "postgres_" + postgresError.Code
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "context_deadline_exceeded"
	}
	return "internal"
}
