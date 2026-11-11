// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package places holds a requester's saved delivery and pickup places, used
// only to fill in errand requests (references/requester-enhancements-n2h.md,
// "Saved places"). This file declares the Store port and its types;
// requesterrepo implements it and internal/testfakes has an in-memory fake.
package places

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
)

// Kind is the stored saved_places.kind.
type Kind string

const (
	KindDelivery Kind = "DELIVERY"
	KindPickup   Kind = "PICKUP"
)

// Kinds lists every kind in declaration order.
func Kinds() []Kind { return []Kind{KindDelivery, KindPickup} }

const (
	// MaxPerKind is how many places of one kind a user may save.
	MaxPerKind = 20
	// MaxLabelLength bounds a label, in characters.
	MaxLabelLength = 40
)

// Place is one saved place. A DELIVERY place has DeliveryBuildingID and
// DeliveryDetail and no SupplierID; a PICKUP place has only SupplierID.
// Unused fields are "". Supplier and building IDs are supplier-service UUIDs
// that may have been deleted or retired since the place was saved.
type Place struct {
	ID                 string
	UserID             string
	Kind               Kind
	Label              string
	SupplierID         string
	DeliveryBuildingID string
	DeliveryDetail     string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewPlace is a place to create, with the same per-kind fields as Place.
// The caller has already validated it; the database CHECK constraints are
// only a backstop.
type NewPlace struct {
	UserID             string
	Kind               Kind
	Label              string
	SupplierID         string
	DeliveryBuildingID string
	DeliveryDetail     string
}

// PlacePatch changes a place. A nil field keeps the stored value. Kind,
// supplier and building cannot change: delete the place and save another.
type PlacePatch struct {
	Label          *string
	DeliveryDetail *string
}

// Store persists saved places. Every method is scoped to userID, the
// token's subject (NFR3.3.1): another user's place behaves exactly like a
// missing one.
//
// Errors: unless a method says otherwise, a failure the caller should show
// to a client is an *apperror.Error with one of the codes named in the
// method's comment; anything else (a lost connection, a CHECK constraint
// rejecting an unvalidated place) is a plain error that callers treat as
// INTERNAL.
type Store interface {
	// List returns the user's places of kind, or of every kind when kind is
	// "", newest first.
	List(ctx context.Context, userID string, kind Kind) ([]Place, error)

	// Create saves p and returns it as stored. In one transaction it takes
	// an advisory lock on the user, counts their places of p.Kind and
	// inserts p, so concurrent creates never exceed MaxPerKind.
	//
	// Errors:
	//   - PLACE_LIMIT_REACHED: the user already has MaxPerKind places of
	//     p.Kind.
	//   - VALIDATION_FAILED on label: the user already has a place of p.Kind
	//     with the same label, ignoring case.
	//   - VALIDATION_FAILED on supplierId: the user already saved
	//     p.SupplierID.
	//   - INVALID_ARGUMENT: a malformed supplier or building UUID.
	Create(ctx context.Context, p NewPlace) (Place, error)

	// Update applies patch to the user's place and returns it as stored.
	// An empty patch returns the place unchanged.
	//
	// Errors:
	//   - PLACE_NOT_FOUND: the user has no place with placeID, or placeID
	//     is not a UUID.
	//   - VALIDATION_FAILED on deliveryDetail: patch.DeliveryDetail is set
	//     on a PICKUP place.
	//   - VALIDATION_FAILED on label: the new label is taken, as in Create.
	Update(ctx context.Context, userID, placeID string, patch PlacePatch) (Place, error)

	// Delete removes the user's place.
	//
	// Errors: PLACE_NOT_FOUND as in Update.
	Delete(ctx context.Context, userID, placeID string) error
}

// The Store errors, shared by every implementation so they report the same
// codes, fields and messages. Messages name no user data.

// NotFound is PLACE_NOT_FOUND.
func NotFound() error {
	return &apperror.Error{Code: apperror.PlaceNotFound, Message: "saved place not found"}
}

// LimitReached is PLACE_LIMIT_REACHED for kind.
func LimitReached(kind Kind) error {
	return &apperror.Error{
		Code:    apperror.PlaceLimitReached,
		Message: fmt.Sprintf("at most %d %s places can be saved", MaxPerKind, strings.ToLower(string(kind))),
	}
}

// DuplicateLabel is VALIDATION_FAILED on label.
func DuplicateLabel() error {
	return invalid("label", "must be unique among your saved places of this kind")
}

// DuplicateSupplier is VALIDATION_FAILED on supplierId.
func DuplicateSupplier() error {
	return invalid("supplierId", "is already a saved place")
}

// DetailOnPickup is VALIDATION_FAILED on deliveryDetail.
func DetailOnPickup() error {
	return invalid("deliveryDetail", "only a delivery place has a delivery detail")
}

func invalid(field, message string) error {
	return &apperror.Error{
		Code:    apperror.ValidationFailed,
		Message: "place is invalid",
		Fields:  []apperror.Field{{Field: field, Message: message}},
	}
}
