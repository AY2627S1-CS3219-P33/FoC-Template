package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/orderclient"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/serviceauth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
	deletefeature "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/delete"
)

// UnavailableDeletionFence keeps the registered deletion workflow fail-closed
// when ORDER_SERVICE_URL is not configured. It must never pretend an
// active-errand check succeeded.
type UnavailableDeletionFence struct{}

var errFenceUnavailable = errors.New("order-service deletion fence is not configured")

var _ deletefeature.OrderDeletionFence = UnavailableDeletionFence{}

func (UnavailableDeletionFence) Acquire(context.Context, supplier.DeletionOperationID, supplier.SupplierID) (deletefeature.Fence, error) {
	return deletefeature.Fence{}, errFenceUnavailable
}

func (UnavailableDeletionFence) Inspect(context.Context, supplier.DeletionOperationID) (deletefeature.Fence, error) {
	return deletefeature.Fence{}, errFenceUnavailable
}

func (UnavailableDeletionFence) Commit(context.Context, supplier.DeletionOperationID) error {
	return errFenceUnavailable
}

func (UnavailableDeletionFence) Release(context.Context, supplier.DeletionOperationID) error {
	return errFenceUnavailable
}

// newDeletionFence returns the order-service fence client, or
// UnavailableDeletionFence when order-service is not configured. The client
// authenticates with supplier-service's own M2M token for ORDER_AUDIENCE.
func newDeletionFence(cfg config.Config, logger *slog.Logger) (deletefeature.OrderDeletionFence, error) {
	if !cfg.OrderService.Enabled() {
		logger.Warn("ORDER_SERVICE_URL is not set; supplier deletion fails closed with DELETION_FENCE_UNAVAILABLE")
		return UnavailableDeletionFence{}, nil
	}
	tokens, err := serviceauth.NewClientCredentials(serviceauth.Config{
		Issuer:       cfg.Auth.Issuer,
		ClientID:     cfg.OrderService.ClientID,
		ClientSecret: cfg.OrderService.ClientSecret,
		Audience:     cfg.OrderService.Audience,
	}, nil)
	if err != nil {
		return nil, err
	}
	return orderclient.New(orderclient.Config{
		BaseURL:     cfg.OrderService.URL,
		Timeout:     cfg.OrderService.Timeout,
		MaxAttempts: cfg.OrderService.MaxAttempts,
	}, serviceauth.NewTransport(nil, tokens), logger)
}
