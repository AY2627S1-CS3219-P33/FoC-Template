package errand_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

func TestAcceptanceDeadline_F3_1_3(t *testing.T) {
	t.Run("defaults to one hour", func(t *testing.T) {
		got, err := errand.AcceptanceDeadline(nil, t0)
		require.NoError(t, err)
		require.Equal(t, t0.Add(time.Hour), got)
	})

	cases := []struct {
		name    string
		offset  time.Duration
		message string
	}{
		{"exactly 10 minutes", 10 * time.Minute, ""},
		{"exactly 24 hours", 24 * time.Hour, ""},
		{"just under 10 minutes", 10*time.Minute - time.Nanosecond, "must be at least 10 minutes from now"},
		{"just over 24 hours", 24*time.Hour + time.Nanosecond, "must be at most 24 hours from now"},
		{"now", 0, "must be in the future"},
		{"in the past", -time.Minute, "must be in the future"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requested := t0.Add(tc.offset)
			got, err := errand.AcceptanceDeadline(&requested, t0)
			if tc.message == "" {
				require.NoError(t, err)
				require.Equal(t, requested, got)
				return
			}
			requireCode(t, apperror.ValidationFailed, err)
			var appErr *apperror.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, []apperror.Field{{Field: "acceptanceDeadline", Message: tc.message}}, appErr.Fields)
		})
	}
}

func TestPassed(t *testing.T) {
	require.True(t, errand.Passed(t0, t0), "a deadline has passed at its exact instant")
	require.True(t, errand.Passed(t0, t0.Add(time.Nanosecond)))
	require.False(t, errand.Passed(t0, t0.Add(-time.Nanosecond)))
}

func TestDeadlineSettings(t *testing.T) {
	s := errand.DefaultDeadlineSettings()
	require.NoError(t, s.Validate())
	require.Equal(t, t0.Add(2*time.Hour), s.PickupDeadline(t0))
	require.Equal(t, t0.Add(2*time.Hour), s.DeliveryDeadline(t0))
	require.Equal(t, t0.Add(30*time.Minute), s.VerificationDeadline(t0))
	require.Equal(t, t0.Add(5*time.Minute), s.VerificationReminderAt(t0))

	custom := errand.DeadlineSettings{
		PickupAfter: 45 * time.Minute, DeliveryAfter: 90 * time.Minute,
		VerificationAfter: time.Hour, VerificationReminderAfter: 10 * time.Minute,
	}
	require.NoError(t, custom.Validate())
	require.Equal(t, t0.Add(45*time.Minute), custom.PickupDeadline(t0))
	require.Equal(t, t0.Add(90*time.Minute), custom.DeliveryDeadline(t0))
	require.Equal(t, t0.Add(time.Hour), custom.VerificationDeadline(t0))

	t.Run("zero settings are invalid", func(t *testing.T) {
		err := errand.DeadlineSettings{}.Validate()
		requireCode(t, apperror.ValidationFailed, err)
		require.ElementsMatch(t,
			[]string{"pickupAfter", "deliveryAfter", "verificationAfter", "verificationReminderAfter"},
			fieldsOf(t, err))
	})
	t.Run("reminder must come before auto-complete", func(t *testing.T) {
		bad := errand.DefaultDeadlineSettings()
		bad.VerificationReminderAfter = bad.VerificationAfter
		err := bad.Validate()
		requireCode(t, apperror.ValidationFailed, err)
		require.Equal(t, []string{"verificationReminderAfter"}, fieldsOf(t, err))
	})
}

func TestStatusClassification(t *testing.T) {
	terminal := map[errand.Status]bool{
		errand.StatusRejected: true, errand.StatusCancelled: true, errand.StatusExpired: true,
		errand.StatusCompleted: true, errand.StatusRefunded: true,
	}
	require.Len(t, errand.Statuses(), 11)
	for _, s := range errand.Statuses() {
		require.True(t, s.Valid())
		require.Equal(t, terminal[s], s.Terminal(), s)
		require.Equal(t, !terminal[s], s.Active(), s)
	}
	require.False(t, errand.StatusNone.Valid())
	require.False(t, errand.StatusNone.Active())
	require.False(t, errand.Status("LOST").Active())
}
