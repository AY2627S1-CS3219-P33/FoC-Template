package opsrepo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Each setting is one settings row. Keys match the field names in
// errand.SystemSettings.Validate errors, so a validation failure names the
// key. Durations are stored as Go duration strings ("2h0m0s") and counts as
// JSON integers.
const (
	KeyPickupAfter                   = "pickupAfter"
	KeyDeliveryAfter                 = "deliveryAfter"
	KeyVerificationAfter             = "verificationAfter"
	KeyVerificationReminderAfter     = "verificationReminderAfter"
	KeyWithdrawalRestrictionCount    = "withdrawalRestriction.count"
	KeyWithdrawalRestrictionWindow   = "withdrawalRestriction.window"
	KeyWithdrawalRestrictionDuration = "withdrawalRestriction.duration"
	KeyCancellationFlagCount         = "cancellationFlag.count"
	KeyCancellationFlagWindow        = "cancellationFlag.window"
	KeyReservationTimeout            = "reservationTimeout"
	KeyEditTimeout                   = "editTimeout"
	KeyOutboxConfirmTimeout          = "outboxConfirmTimeout"
)

// settingFields maps every key to its field: a *time.Duration or an *int.
var settingFields = []struct {
	key   string
	field func(*errand.SystemSettings) any
}{
	{KeyPickupAfter, func(s *errand.SystemSettings) any { return &s.Deadlines.PickupAfter }},
	{KeyDeliveryAfter, func(s *errand.SystemSettings) any { return &s.Deadlines.DeliveryAfter }},
	{KeyVerificationAfter, func(s *errand.SystemSettings) any { return &s.Deadlines.VerificationAfter }},
	{KeyVerificationReminderAfter, func(s *errand.SystemSettings) any { return &s.Deadlines.VerificationReminderAfter }},
	{KeyWithdrawalRestrictionCount, func(s *errand.SystemSettings) any { return &s.WithdrawalRestriction.Count }},
	{KeyWithdrawalRestrictionWindow, func(s *errand.SystemSettings) any { return &s.WithdrawalRestriction.Window }},
	{KeyWithdrawalRestrictionDuration, func(s *errand.SystemSettings) any { return &s.WithdrawalRestriction.Duration }},
	{KeyCancellationFlagCount, func(s *errand.SystemSettings) any { return &s.CancellationFlag.Count }},
	{KeyCancellationFlagWindow, func(s *errand.SystemSettings) any { return &s.CancellationFlag.Window }},
	{KeyReservationTimeout, func(s *errand.SystemSettings) any { return &s.ReservationTimeout }},
	{KeyEditTimeout, func(s *errand.SystemSettings) any { return &s.EditTimeout }},
	{KeyOutboxConfirmTimeout, func(s *errand.SystemSettings) any { return &s.OutboxConfirmTimeout }},
}

var _ errand.Settings = (*Repository)(nil)

// Get returns the stored settings, with DefaultSystemSettings for any key
// never stored. Unknown keys are ignored. A stored value that cannot be
// decoded, or a stored set that fails validation, is an error: callers
// refuse the operation rather than run on bad settings.
func (r *Repository) Get(ctx context.Context) (errand.SystemSettings, error) {
	rows, err := r.queries.ListSettings(ctx)
	if err != nil {
		return errand.SystemSettings{}, mapError(err)
	}
	stored := make(map[string][]byte, len(rows))
	for _, row := range rows {
		stored[row.Key] = row.Value
	}
	settings := errand.DefaultSystemSettings()
	for _, f := range settingFields {
		value, ok := stored[f.key]
		if !ok {
			continue
		}
		if err := decodeSetting(value, f.field(&settings)); err != nil {
			return errand.SystemSettings{}, fmt.Errorf("setting %q: %w", f.key, err)
		}
	}
	if err := settings.Validate(); err != nil {
		return errand.SystemSettings{}, fmt.Errorf("stored settings are invalid: %w", err)
	}
	return settings, nil
}

// Update validates settings (errand.SystemSettings.Validate: an
// apperror VALIDATION_FAILED naming each bad key) and stores every value in
// one transaction. Keys whose value is unchanged keep their updated_by and
// updated_at.
func (r *Repository) Update(ctx context.Context, settings errand.SystemSettings, updatedBy string) error {
	if updatedBy == "" {
		return invalid("updatedBy is required")
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	return r.InTx(ctx, func(tx *Repository) error {
		for _, f := range settingFields {
			value, err := encodeSetting(f.field(&settings))
			if err != nil {
				return err
			}
			if _, err := tx.queries.UpsertSetting(ctx, opsgen.UpsertSettingParams{
				Key:       f.key,
				Value:     value,
				UpdatedBy: text(updatedBy),
			}); err != nil {
				return mapError(err)
			}
		}
		return nil
	})
}

// SeedDefaults stores DefaultSystemSettings for every key not yet stored and
// returns how many rows it inserted. Running it again inserts nothing and
// never overwrites an admin's value.
func (r *Repository) SeedDefaults(ctx context.Context) (inserted int, err error) {
	defaults := errand.DefaultSystemSettings()
	err = r.InTx(ctx, func(tx *Repository) error {
		inserted = 0
		for _, f := range settingFields {
			value, err := encodeSetting(f.field(&defaults))
			if err != nil {
				return err
			}
			affected, err := tx.queries.SeedSetting(ctx, opsgen.SeedSettingParams{Key: f.key, Value: value})
			if err != nil {
				return mapError(err)
			}
			inserted += int(affected)
		}
		return nil
	})
	return inserted, err
}

func encodeSetting(field any) ([]byte, error) {
	switch v := field.(type) {
	case *time.Duration:
		return json.Marshal(v.String())
	case *int:
		return json.Marshal(*v)
	default:
		return nil, fmt.Errorf("unsupported setting type %T", field)
	}
}

func decodeSetting(value []byte, field any) error {
	switch v := field.(type) {
	case *time.Duration:
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return fmt.Errorf("want a duration string: %w", err)
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*v = d
		return nil
	case *int:
		return json.Unmarshal(value, v)
	default:
		return fmt.Errorf("unsupported setting type %T", field)
	}
}
