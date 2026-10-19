package testfakes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

const (
	requesterID = "requester-1"
	supplierID  = "supplier-1"
	reward      = 10
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// newErrand is a valid, unsaved errand for Submit at now.
func newErrand(id string, now time.Time) errand.Errand {
	return errand.Errand{
		ID:          id,
		RequesterID: requesterID,
		Details: errand.Details{
			SupplierID:               supplierID,
			SupplierVersionID:        "supplier-version-1",
			DeliveryBuildingID:       "building-1",
			DeliveryBuildingSnapshot: json.RawMessage(`{"name":"COM1"}`),
			DeliveryDetail:           "Level 2 lounge",
			Items:                    json.RawMessage(`[{"description":"iced latte"}]`),
			ContactName:              "Alex Tan",
			ContactMobile:            "+65 9123 4567",
			Reward:                   reward,
			AcceptanceDeadline:       now.Add(time.Hour),
		},
		CreateIdempotencyKey: "create-" + id,
	}
}

func requester() errand.Actor { return errand.Actor{Type: errand.ActorRequester, ID: requesterID} }

func courier(id string) errand.Actor { return errand.Actor{Type: errand.ActorCourier, ID: id} }

func acceptInput() errand.EventInput {
	return errand.EventInput{Deadlines: errand.DefaultDeadlineSettings()}
}

type submitter interface {
	Submit(ctx context.Context, e errand.Errand, now time.Time) (errand.Errand, error)
	Fire(ctx context.Context, id string, ev errand.Event, actor errand.Actor, input errand.EventInput, now time.Time) (errand.Errand, errand.Transition, error)
}

// openErrand submits an errand at now and confirms its reservation.
func openErrand(t *testing.T, s submitter, id string, now time.Time) errand.Errand {
	t.Helper()
	ctx := context.Background()
	_, err := s.Submit(ctx, newErrand(id, now), now)
	require.NoError(t, err)
	e, _, err := s.Fire(ctx, id, errand.EventReservationConfirmed, errand.SystemActor(), errand.EventInput{}, now)
	require.NoError(t, err)
	require.Equal(t, errand.StatusOpen, e.Status)
	return e
}

func errandIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("e-%03d", i)
	}
	return ids
}

func requireCode(t *testing.T, want apperror.Code, err error) {
	t.Helper()
	require.Error(t, err)
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr), "want *apperror.Error, got %T: %v", err, err)
	require.Equal(t, want, appErr.Code, "error: %v", err)
}
