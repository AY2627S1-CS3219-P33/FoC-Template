// Package apperror defines stable, transport-independent application errors.
package apperror

type Code string

const (
	InvalidArgument          Code = "INVALID_ARGUMENT"
	Unauthenticated          Code = "UNAUTHENTICATED"
	Forbidden                Code = "FORBIDDEN"
	SupplierNotFound         Code = "SUPPLIER_NOT_FOUND"
	SupplierVersionNotFound  Code = "SUPPLIER_VERSION_NOT_FOUND"
	SupplierNameConflict     Code = "SUPPLIER_NAME_CONFLICT"
	SupplierHasActiveErrands Code = "SUPPLIER_HAS_ACTIVE_ERRANDS"
	DeletionFenceUnavailable Code = "DELETION_FENCE_UNAVAILABLE"
	DependencyUnavailable    Code = "DEPENDENCY_UNAVAILABLE"
	Internal                 Code = "INTERNAL"
)

type Field struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type Error struct {
	Code    Code    `json:"code"`
	Message string  `json:"message"`
	Fields  []Field `json:"fields,omitempty"`
}

func (e *Error) Error() string { return e.Message }
