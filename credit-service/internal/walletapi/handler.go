// Package walletapi exposes the wallet use cases over HTTP.
//
//	GET /v1/wallet/me              user    credits:read:own
//	GET /internal/wallets/{owner}  service credits:read
//	PUT /internal/wallets/{owner}  service credits:wallets:create
//
// A user is never given a way to name another user's wallet: /v1/wallet/me
// takes no identifier and resolves the wallet from the verified token.
package walletapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

const maxBodyBytes = 4 << 10

// Service is the use-case boundary the handlers depend on.
type Service interface {
	CreateWallet(ctx context.Context, ownerID, auth0Subject string) (wallet.Wallet, bool, error)
	GetByOwner(ctx context.Context, ownerID string) (wallet.Wallet, error)
	GetBySubject(ctx context.Context, auth0Subject string) (wallet.Wallet, error)
}

type Handler struct {
	service Service
	auth    *auth.Middleware
	logger  *slog.Logger
}

func NewHandler(service Service, middleware *auth.Middleware, logger *slog.Logger) Handler {
	if service == nil || middleware == nil {
		panic("walletapi: service and auth middleware are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return Handler{service: service, auth: middleware, logger: logger}
}

func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /v1/wallet/me",
		h.auth.Require(auth.User, auth.ReadOwnWallet)(http.HandlerFunc(h.getMine)))
	router.Handle("GET /internal/wallets/{ownerId}",
		h.auth.Require(auth.Service, auth.ReadWallet)(http.HandlerFunc(h.getByOwner)))
	router.Handle("PUT /internal/wallets/{ownerId}",
		h.auth.Require(auth.Service, auth.CreateWallet)(http.HandlerFunc(h.create)))
}

// Balances are strings so the exact whole-number value survives any JSON
// client and matches user-service's exact-decimal contract.
type walletResponse struct {
	OwnerID   string    `json:"ownerId"`
	Available string    `json:"available"`
	Reserved  string    `json:"reserved"`
	CreatedAt time.Time `json:"createdAt"`
}

func toResponse(w wallet.Wallet) walletResponse {
	return walletResponse{
		OwnerID:   w.OwnerID,
		Available: strconv.FormatInt(w.Available, 10),
		Reserved:  strconv.FormatInt(w.Reserved, 10),
		CreatedAt: w.CreatedAt.UTC(),
	}
}

type createRequest struct {
	Auth0Subject string `json:"auth0Subject"`
}

func (h Handler) getMine(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		h.fail(w, r, errors.New("missing principal"))
		return
	}
	found, err := h.service.GetBySubject(r.Context(), principal.Subject)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(found))
}

func (h Handler) getByOwner(w http.ResponseWriter, r *http.Request) {
	found, err := h.service.GetByOwner(r.Context(), r.PathValue("ownerId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(found))
}

func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body createRequest
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "request body must be JSON with an auth0Subject")
		return
	}
	if decoder.More() {
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "request body must be a single JSON object")
		return
	}

	created, wasCreated, err := h.service.CreateWallet(r.Context(), r.PathValue("ownerId"), body.Auth0Subject)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if wasCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, toResponse(created))
}

// fail maps use-case errors to stable responses. Unexpected errors are logged
// without request data and never shown to the caller.
func (h Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, wallet.ErrInvalid):
		message := strings.TrimPrefix(err.Error(), wallet.ErrInvalid.Error()+": ")
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, message)
	case errors.Is(err, wallet.ErrNotFound):
		writeError(w, http.StatusNotFound, apperror.WalletNotFound, "wallet not found")
	case errors.Is(err, wallet.ErrConflict):
		writeError(w, http.StatusConflict, apperror.WalletConflict, "wallet conflicts with an existing wallet")
	default:
		h.logger.ErrorContext(r.Context(), "wallet request failed", "error", err.Error(), "method", r.Method)
		writeError(w, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code apperror.Code, message string) {
	writeJSON(w, status, apperror.Error{Code: code, Message: message})
}
