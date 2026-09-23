// Package middleware reserves HTTP authentication and authorization contracts.
package middleware

import "net/http"

// Authentication will authenticate a request before invoking the next handler.
type Authentication func(next http.Handler) http.Handler

// Authorization will restrict a handler to the specified account roles.
type Authorization func(roles ...string) func(next http.Handler) http.Handler

// TODO: Implement these contracts when identity context and access policies are defined.
// No pass-through middleware is provided.
