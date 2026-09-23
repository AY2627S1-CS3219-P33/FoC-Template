// Package handler will adapt HTTP requests to user-service operations.
package handler

import "net/http"

// Handler reserves a place for routing and business-service dependencies.
// It is not wired or registered with a server yet.
type Handler struct {
	Router *http.ServeMux
}

// TODO: Add routes, request validation, and response mapping.
