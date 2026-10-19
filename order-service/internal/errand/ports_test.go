package errand_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// NFR3.6.4: the cached open-list projection carries no requester identity
// and no requester notes.
func TestOpenErrandViewIsRedacted(t *testing.T) {
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for i := range typ.NumField() {
			f := typ.Field(i)
			name := strings.ToLower(f.Name)
			assert.NotContains(t, name, "requester", "field %s.%s", typ.Name(), f.Name)
			assert.NotContains(t, name, "notes", "field %s.%s", typ.Name(), f.Name)
			if f.Type.Kind() == reflect.Struct && f.Type.PkgPath() == typ.PkgPath() {
				walk(f.Type)
			}
		}
	}
	walk(reflect.TypeOf(errand.OpenErrandView{}))

	data, err := json.Marshal(errand.OpenErrandView{})
	require.NoError(t, err)
	assert.NotContains(t, strings.ToLower(string(data)), "requester")
}

func TestDueKindMatchesItsIndexPredicate(t *testing.T) {
	at := t0.Add(time.Hour)
	pendingEdit := errandIn(errand.StatusOpen)
	pendingEdit.PendingEdit = &errand.PendingEdit{Key: "k", At: at}

	tests := []struct {
		kind errand.DueKind
		e    errand.Errand
		at   time.Time
	}{
		{errand.AcceptanceExpiry, errandIn(errand.StatusOpen), acceptanceDeadline},
		{errand.PickupDeadline, errandIn(errand.StatusAssigned), pickupDeadline},
		{errand.DeliveryDeadline, errandIn(errand.StatusPickedUp), deliveryDeadline},
		{errand.VerificationLapse, errandIn(errand.StatusDelivered), verificationDeadline},
		{errand.StuckReservation, errandIn(errand.StatusPendingReservation), t0.Add(-time.Hour)},
		{errand.StuckEdit, pendingEdit, at},
	}
	require.Len(t, tests, len(errand.DueKinds()))
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			got, ok := tt.kind.DueAt(tt.e)
			require.True(t, ok)
			assert.Equal(t, tt.at, got)
			assert.True(t, tt.kind.Due(tt.e, tt.at), "due at the column value")
			assert.False(t, tt.kind.Due(tt.e, tt.at.Add(-time.Nanosecond)))

			// Every other stored state is outside the predicate, except that
			// StuckEdit depends only on the pending edit.
			if tt.kind == errand.StuckEdit {
				return
			}
			for _, s := range errand.Statuses() {
				if s == tt.e.Status {
					continue
				}
				other := tt.e
				other.Status = s
				_, ok := tt.kind.DueAt(other)
				assert.False(t, ok, "status %s", s)
			}
		})
	}
}

func TestEventNamesAndRejectionKeys(t *testing.T) {
	names := errand.EventNames()
	for _, a := range errand.RejectedActions() {
		assert.Contains(t, names, a.EventName())
	}
	assert.Contains(t, names, errand.ErrandAcceptRejected)
	assert.Equal(t, errand.ErrandCancelRejected, errand.RejectedCancel.EventName())
	assert.Equal(t, "e-1:pickup-rejected:req-9", errand.RejectionKey("e-1", errand.RejectedPickup, "req-9"))
}

func TestDefaultSystemSettingsAreValid(t *testing.T) {
	s := errand.DefaultSystemSettings()
	require.NoError(t, s.Validate())
	s.EditTimeout = 0
	s.Deadlines.PickupAfter = 0
	assert.ElementsMatch(t, []string{"editTimeout", "pickupAfter"}, fieldsOf(t, s.Validate()))
}
