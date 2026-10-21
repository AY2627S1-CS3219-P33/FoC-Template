// Package apperror defines the stable JSON error body returned by the API.
package apperror

type Code string

const (
	InvalidArgument       Code = "INVALID_ARGUMENT"
	Unauthenticated       Code = "UNAUTHENTICATED"
	Forbidden             Code = "FORBIDDEN"
	WalletNotFound        Code = "WALLET_NOT_FOUND"
	WalletConflict        Code = "WALLET_CONFLICT"
	DependencyUnavailable Code = "DEPENDENCY_UNAVAILABLE"
	Internal              Code = "INTERNAL"
)

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }
