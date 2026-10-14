// Package apperror defines stable, transport-independent application errors.
package apperror

import "net/http"

type Code string

const (
	InvalidArgument Code = "INVALID_ARGUMENT"
	Unauthenticated Code = "UNAUTHENTICATED"
	Forbidden       Code = "FORBIDDEN"
	// ValidationFailed carries per-field details in Error.Fields (NFR2.2.1).
	ValidationFailed Code = "VALIDATION_FAILED"
	ErrandNotFound   Code = "ERRAND_NOT_FOUND"
	// FenceNotFound means no supplier deletion fence has the operation ID.
	FenceNotFound Code = "FENCE_NOT_FOUND"
	// OutboxRowNotFound means no outbox row has the ID (admin replay, M6).
	OutboxRowNotFound Code = "OUTBOX_ROW_NOT_FOUND"
	// ErrandChanged means the caller's expectedVersion is stale (F3.3.7).
	ErrandChanged Code = "ERRAND_CHANGED"
	// ErrandUnavailable means the errand can no longer take this action,
	// for example it was accepted by someone else or is terminal (F3.3.7).
	ErrandUnavailable Code = "ERRAND_UNAVAILABLE"
	// InvalidTransition means the (status, event) pair is not in Diagram 1.
	InvalidTransition Code = "INVALID_TRANSITION"
	// NotEligible means the courier may not accept this errand (F3.3.6).
	NotEligible Code = "NOT_ELIGIBLE"
	// CourierRestricted means the courier is temporarily barred from
	// accepting errands after repeated withdrawals (F3.6.4).
	CourierRestricted Code = "COURIER_RESTRICTED"
	// InsufficientCredits is reported as the reason on a REJECTED errand or
	// a rejected edit, not as an HTTP error.
	InsufficientCredits Code = "INSUFFICIENT_CREDITS"
	// EditPending means a reward change is still waiting for credit-service.
	EditPending Code = "EDIT_PENDING"
	// SupplierUnavailable means the supplier is missing, deleted or has no
	// usable version for a new errand (F3.1.1).
	SupplierUnavailable        Code = "SUPPLIER_UNAVAILABLE"
	SupplierDeletionInProgress Code = "SUPPLIER_DELETION_IN_PROGRESS"
	DependencyUnavailable      Code = "DEPENDENCY_UNAVAILABLE"
	Internal                   Code = "INTERNAL"
)

// Codes lists every code this service may return, in declaration order.
func Codes() []Code {
	return []Code{
		InvalidArgument,
		Unauthenticated,
		Forbidden,
		ValidationFailed,
		ErrandNotFound,
		FenceNotFound,
		OutboxRowNotFound,
		ErrandChanged,
		ErrandUnavailable,
		InvalidTransition,
		NotEligible,
		CourierRestricted,
		InsufficientCredits,
		EditPending,
		SupplierUnavailable,
		SupplierDeletionInProgress,
		DependencyUnavailable,
		Internal,
	}
}

// HTTPStatus is the single mapping from code to HTTP status. Unknown codes
// map to 500 so an unclassified failure never looks like a client error.
func HTTPStatus(code Code) int {
	switch code {
	case InvalidArgument, ValidationFailed:
		return http.StatusBadRequest
	case Unauthenticated:
		return http.StatusUnauthorized
	case Forbidden, NotEligible, CourierRestricted:
		return http.StatusForbidden
	case ErrandNotFound, FenceNotFound, OutboxRowNotFound:
		return http.StatusNotFound
	case ErrandChanged, ErrandUnavailable, InvalidTransition,
		InsufficientCredits, EditPending, SupplierDeletionInProgress:
		return http.StatusConflict
	case SupplierUnavailable:
		return http.StatusUnprocessableEntity
	case DependencyUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

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
