package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// SettingsAdmin is what the settings routes need (F6.3.5). adminID is the
// admin's sub. A refused update returns an *apperror.Error.
type SettingsAdmin interface {
	Get(ctx context.Context) (errand.SystemSettings, error)
	Put(ctx context.Context, adminID string, settings errand.SystemSettings) (errand.SystemSettings, error)
}

// SettingsStore reads and replaces the settings rows.
type SettingsStore interface {
	Get(ctx context.Context) (errand.SystemSettings, error)
	// UpdateAudited stores every setting and inserts record in one
	// transaction, so a change is never stored unaudited (F6.5).
	UpdateAudited(ctx context.Context, settings errand.SystemSettings, updatedBy string, record opsrepo.AuditRecord) error
}

// RepositorySettings is the SettingsStore over opsrepo.
type RepositorySettings struct {
	repo *opsrepo.Repository
}

var _ SettingsStore = RepositorySettings{}

func NewRepositorySettings(repo *opsrepo.Repository) RepositorySettings {
	if repo == nil {
		panic("review: repository is required")
	}
	return RepositorySettings{repo: repo}
}

func (s RepositorySettings) Get(ctx context.Context) (errand.SystemSettings, error) {
	return s.repo.Get(ctx)
}

func (s RepositorySettings) UpdateAudited(ctx context.Context, settings errand.SystemSettings, updatedBy string, record opsrepo.AuditRecord) error {
	return s.repo.InTx(ctx, func(tx *opsrepo.Repository) error {
		if err := tx.Update(ctx, settings, updatedBy); err != nil {
			return err
		}
		_, err := tx.InsertAudit(ctx, record)
		return err
	})
}

// Audit log vocabulary for settings changes.
const (
	AuditTargetSettings  = "settings"
	AuditActionSettings  = "settings.update"
	auditSettingsID      = "settings"
	auditNoSettingChange = "no change"
)

// SettingsService implements SettingsAdmin.
type SettingsService struct {
	store  SettingsStore
	audit  AuditWriter
	logger *slog.Logger
}

var _ SettingsAdmin = (*SettingsService)(nil)

func NewSettingsService(store SettingsStore, audit AuditWriter, logger *slog.Logger) *SettingsService {
	switch {
	case store == nil:
		panic("review: settings store is required")
	case audit == nil:
		panic("review: audit writer is required")
	case logger == nil:
		panic("review: logger is required")
	}
	return &SettingsService{store: store, audit: audit, logger: logger}
}

// Get returns the stored settings, with defaults for keys never stored.
func (s *SettingsService) Get(ctx context.Context) (errand.SystemSettings, error) {
	settings, err := s.store.Get(ctx)
	if err != nil {
		return errand.SystemSettings{}, fmt.Errorf("read settings: %w", err)
	}
	return settings, nil
}

// Put validates and replaces every setting. The audit row's reason names
// the settings that changed, or "no change". Invalid settings are refused
// with VALIDATION_FAILED and audited as REJECTED.
func (s *SettingsService) Put(ctx context.Context, adminID string, settings errand.SystemSettings) (errand.SystemSettings, error) {
	if err := settings.Validate(); err != nil {
		s.auditRejected(ctx, adminID, err)
		return errand.SystemSettings{}, err
	}
	reason := "changed: all (the stored settings could not be read)"
	if current, err := s.store.Get(ctx); err == nil {
		reason = changeSummary(current, settings)
	} else {
		// A broken stored value must not stop an admin from replacing it.
		s.logger.WarnContext(ctx, "settings could not be read before update", slog.Any("error", err))
	}
	if err := s.store.UpdateAudited(ctx, settings, adminID, opsrepo.AuditRecord{
		ActorID:    adminID,
		Action:     AuditActionSettings,
		TargetType: AuditTargetSettings,
		TargetID:   auditSettingsID,
		Reason:     reason,
		Outcome:    opsrepo.AuditSucceeded,
	}); err != nil {
		var appErr *apperror.Error
		if errors.As(err, &appErr) {
			s.auditRejected(ctx, adminID, err)
			return errand.SystemSettings{}, err
		}
		return errand.SystemSettings{}, fmt.Errorf("update settings: %w", err)
	}
	s.logger.InfoContext(ctx, "settings updated", slog.String("change", reason))
	return settings, nil
}

func (s *SettingsService) auditRejected(ctx context.Context, adminID string, err error) {
	code := apperror.Internal
	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		code = appErr.Code
	}
	if _, auditErr := s.audit.InsertAudit(ctx, opsrepo.AuditRecord{
		ActorID:    adminID,
		Action:     AuditActionSettings,
		TargetType: AuditTargetSettings,
		TargetID:   auditSettingsID,
		Reason:     string(code),
		Outcome:    opsrepo.AuditRejected,
	}); auditErr != nil {
		s.logger.ErrorContext(ctx, "settings rejection audit failed",
			slog.String("code", string(code)),
			slog.Any("error", auditErr))
	}
}

// settingValues lists every setting under its field path (the Settings
// schema, the VALIDATION_FAILED field names and the opsrepo keys).
func settingValues(s errand.SystemSettings) []struct {
	name  string
	value any
} {
	return []struct {
		name  string
		value any
	}{
		{opsrepo.KeyPickupAfter, s.Deadlines.PickupAfter},
		{opsrepo.KeyDeliveryAfter, s.Deadlines.DeliveryAfter},
		{opsrepo.KeyVerificationAfter, s.Deadlines.VerificationAfter},
		{opsrepo.KeyVerificationReminderAfter, s.Deadlines.VerificationReminderAfter},
		{opsrepo.KeyWithdrawalRestrictionCount, s.WithdrawalRestriction.Count},
		{opsrepo.KeyWithdrawalRestrictionWindow, s.WithdrawalRestriction.Window},
		{opsrepo.KeyWithdrawalRestrictionDuration, s.WithdrawalRestriction.Duration},
		{opsrepo.KeyCancellationFlagCount, s.CancellationFlag.Count},
		{opsrepo.KeyCancellationFlagWindow, s.CancellationFlag.Window},
		{opsrepo.KeyReservationTimeout, s.ReservationTimeout},
		{opsrepo.KeyEditTimeout, s.EditTimeout},
		{opsrepo.KeyOutboxConfirmTimeout, s.OutboxConfirmTimeout},
	}
}

// changeSummary names the settings whose value differs, e.g.
// "changed: pickupAfter, cancellationFlag.count".
func changeSummary(before, after errand.SystemSettings) string {
	old, updated := settingValues(before), settingValues(after)
	var changed []string
	for i := range updated {
		if old[i].value != updated[i].value {
			changed = append(changed, updated[i].name)
		}
	}
	if len(changed) == 0 {
		return auditNoSettingChange
	}
	return "changed: " + strings.Join(changed, ", ")
}
