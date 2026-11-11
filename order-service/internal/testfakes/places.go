// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package testfakes

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/places"
)

// FakePlaces is an in-memory places.Store with the same contract as
// requesterrepo: per-user scoping, the per-kind limit, case-insensitive
// label uniqueness, one place per supplier, and the saved_places CHECK
// constraints, which fail with a plain error as they would in PostgreSQL.
// Timestamps come from the clock; places created at the same instant list
// in reverse insertion order.
type FakePlaces struct {
	Faults

	mu     sync.Mutex
	clock  errand.Clock
	places map[string]fakePlace
	seq    int
}

type fakePlace struct {
	places.Place
	seq int
}

var _ places.Store = (*FakePlaces)(nil)

func NewFakePlaces(clock errand.Clock) *FakePlaces {
	return &FakePlaces{clock: clock, places: map[string]fakePlace{}}
}

func (s *FakePlaces) List(_ context.Context, userID string, kind places.Kind) ([]places.Place, error) {
	if err := s.fault("List"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []fakePlace
	for _, p := range s.places {
		if p.UserID == userID && (kind == "" || p.Kind == kind) {
			found = append(found, p)
		}
	}
	slices.SortFunc(found, func(a, b fakePlace) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.seq, a.seq)
	})
	out := make([]places.Place, 0, len(found))
	for _, p := range found {
		out = append(out, p.Place)
	}
	return out, nil
}

func (s *FakePlaces) Create(_ context.Context, p places.NewPlace) (places.Place, error) {
	if err := s.fault("Create"); err != nil {
		return places.Place{}, err
	}
	supplierID, err := placeUUIDArg("supplierId", p.SupplierID)
	if err != nil {
		return places.Place{}, err
	}
	buildingID, err := placeUUIDArg("deliveryBuildingId", p.DeliveryBuildingID)
	if err != nil {
		return places.Place{}, err
	}
	place := places.Place{
		UserID:             p.UserID,
		Kind:               p.Kind,
		Label:              p.Label,
		SupplierID:         supplierID,
		DeliveryBuildingID: buildingID,
		DeliveryDetail:     p.DeliveryDetail,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, other := range s.places {
		if other.UserID == p.UserID && other.Kind == p.Kind {
			count++
		}
	}
	if count >= places.MaxPerKind {
		return places.Place{}, places.LimitReached(p.Kind)
	}
	if err := checkPlace(place); err != nil {
		return places.Place{}, err
	}
	// requesterrepo inserts an empty delivery detail as NULL.
	if place.Kind == places.KindDelivery && place.DeliveryDetail == "" {
		return places.Place{}, fmt.Errorf("%w: delivery columns", errPlaceCheck)
	}
	if err := s.checkUnique(place, ""); err != nil {
		return places.Place{}, err
	}
	now := s.clock.Now()
	place.ID = newPlaceID()
	place.CreatedAt = now
	place.UpdatedAt = now
	s.seq++
	s.places[place.ID] = fakePlace{Place: place, seq: s.seq}
	return place, nil
}

func (s *FakePlaces) Update(_ context.Context, userID, placeID string, patch places.PlacePatch) (places.Place, error) {
	if err := s.fault("Update"); err != nil {
		return places.Place{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.find(userID, placeID)
	if !ok {
		return places.Place{}, places.NotFound()
	}
	if patch.DeliveryDetail != nil && stored.Kind != places.KindDelivery {
		return places.Place{}, places.DetailOnPickup()
	}
	if patch.Label == nil && patch.DeliveryDetail == nil {
		return stored.Place, nil
	}
	updated := stored
	if patch.Label != nil {
		updated.Label = *patch.Label
	}
	if patch.DeliveryDetail != nil {
		updated.DeliveryDetail = *patch.DeliveryDetail
	}
	if err := checkPlace(updated.Place); err != nil {
		return places.Place{}, err
	}
	if err := s.checkUnique(updated.Place, updated.ID); err != nil {
		return places.Place{}, err
	}
	updated.UpdatedAt = s.clock.Now()
	s.places[updated.ID] = updated
	return updated.Place, nil
}

func (s *FakePlaces) Delete(_ context.Context, userID, placeID string) error {
	if err := s.fault("Delete"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.find(userID, placeID)
	if !ok {
		return places.NotFound()
	}
	delete(s.places, stored.ID)
	return nil
}

// find returns the user's place; another user's place, or a malformed ID,
// is not found.
func (s *FakePlaces) find(userID, placeID string) (fakePlace, bool) {
	if !isUUID(placeID) {
		return fakePlace{}, false
	}
	p, ok := s.places[strings.ToLower(placeID)]
	if !ok || p.UserID != userID {
		return fakePlace{}, false
	}
	return p, true
}

// checkUnique models saved_places_label_idx and saved_places_supplier_idx,
// ignoring the place with ID self.
func (s *FakePlaces) checkUnique(p places.Place, self string) error {
	for _, other := range s.places {
		if other.ID == self || other.UserID != p.UserID {
			continue
		}
		if other.Kind == p.Kind && strings.ToLower(other.Label) == strings.ToLower(p.Label) {
			return places.DuplicateLabel()
		}
		if p.Kind == places.KindPickup && other.Kind == places.KindPickup && other.SupplierID == p.SupplierID {
			return places.DuplicateSupplier()
		}
	}
	return nil
}

// errPlaceCheck is the plain error a saved_places CHECK constraint raises.
var errPlaceCheck = errors.New("testfakes: saved_places check constraint violated")

// checkPlace models the saved_places CHECK constraints.
func checkPlace(p places.Place) error {
	if strings.Trim(p.Label, " ") == "" || utf8.RuneCountInString(p.Label) > places.MaxLabelLength {
		return fmt.Errorf("%w: label", errPlaceCheck)
	}
	switch p.Kind {
	case places.KindDelivery:
		if p.SupplierID != "" || p.DeliveryBuildingID == "" {
			return fmt.Errorf("%w: delivery columns", errPlaceCheck)
		}
	case places.KindPickup:
		if p.SupplierID == "" || p.DeliveryBuildingID != "" || p.DeliveryDetail != "" {
			return fmt.Errorf("%w: pickup columns", errPlaceCheck)
		}
	default:
		return fmt.Errorf("%w: kind", errPlaceCheck)
	}
	return nil
}

// placeUUIDArg normalises a UUID argument as PostgreSQL would; a malformed
// one is INVALID_ARGUMENT and "" stays "" (NULL).
func placeUUIDArg(name, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !isUUID(value) {
		return "", &apperror.Error{Code: apperror.InvalidArgument, Message: name + " is not a UUID"}
	}
	return strings.ToLower(value), nil
}

// isUUID reports whether value is a UUID in the canonical 8-4-4-4-12 form.
func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

// newPlaceID returns a random (version 4) UUID, like gen_random_uuid().
func newPlaceID() string {
	value := make([]byte, 16)
	_, _ = rand.Read(value)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
