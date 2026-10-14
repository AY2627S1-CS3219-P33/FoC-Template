package errand

import (
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
)

// Acceptance deadline rules (F3.1.3). The window is measured from the time of
// the submission or edit.
const (
	DefaultAcceptanceWindow = time.Hour
	MinAcceptanceWindow     = 10 * time.Minute
	MaxAcceptanceWindow     = 24 * time.Hour
)

// DeadlineSettings are the system-configured deadlines (Diagram 1,
// "Deadlines"). They come from the Settings port; Next takes them through
// EventInput.
type DeadlineSettings struct {
	// PickupAfter and DeliveryAfter count from acceptance (F3.8.4, F3.8.5).
	PickupAfter   time.Duration
	DeliveryAfter time.Duration
	// VerificationAfter counts from delivery; the errand auto-completes when
	// it lapses (F3.10.3). VerificationReminderAfter is when the requester is
	// reminded.
	VerificationAfter         time.Duration
	VerificationReminderAfter time.Duration
}

// DefaultDeadlineSettings are the defaults in order-requirements.md.
func DefaultDeadlineSettings() DeadlineSettings {
	return DeadlineSettings{
		PickupAfter:               2 * time.Hour,
		DeliveryAfter:             2 * time.Hour,
		VerificationAfter:         30 * time.Minute,
		VerificationReminderAfter: 5 * time.Minute,
	}
}

// Validate rejects settings that would put a deadline at or before the event
// that sets it.
func (s DeadlineSettings) Validate() error {
	var fields []apperror.Field
	positive := func(name string, d time.Duration) {
		if d <= 0 {
			fields = append(fields, apperror.Field{Field: name, Message: "must be positive"})
		}
	}
	positive("pickupAfter", s.PickupAfter)
	positive("deliveryAfter", s.DeliveryAfter)
	positive("verificationAfter", s.VerificationAfter)
	positive("verificationReminderAfter", s.VerificationReminderAfter)
	if s.VerificationReminderAfter >= s.VerificationAfter && len(fields) == 0 {
		fields = append(fields, apperror.Field{
			Field:   "verificationReminderAfter",
			Message: "must be before verificationAfter",
		})
	}
	if len(fields) > 0 {
		return &apperror.Error{Code: apperror.ValidationFailed, Message: "deadline settings are invalid", Fields: fields}
	}
	return nil
}

func (s DeadlineSettings) PickupDeadline(acceptedAt time.Time) time.Time {
	return acceptedAt.Add(s.PickupAfter)
}

func (s DeadlineSettings) DeliveryDeadline(acceptedAt time.Time) time.Time {
	return acceptedAt.Add(s.DeliveryAfter)
}

func (s DeadlineSettings) VerificationDeadline(deliveredAt time.Time) time.Time {
	return deliveredAt.Add(s.VerificationAfter)
}

func (s DeadlineSettings) VerificationReminderAt(deliveredAt time.Time) time.Time {
	return deliveredAt.Add(s.VerificationReminderAfter)
}

// AcceptanceDeadline returns the deadline for a submission or edit at now:
// the default when requested is nil, otherwise requested if it is within the
// bounds.
func AcceptanceDeadline(requested *time.Time, now time.Time) (time.Time, error) {
	if requested == nil {
		return now.Add(DefaultAcceptanceWindow), nil
	}
	if err := ValidateAcceptanceDeadline(*requested, now); err != nil {
		return time.Time{}, err
	}
	return *requested, nil
}

// ValidateAcceptanceDeadline checks that deadline is in the future and between
// MinAcceptanceWindow and MaxAcceptanceWindow from now, both inclusive.
func ValidateAcceptanceDeadline(deadline, now time.Time) error {
	if field, ok := acceptanceDeadlineProblem(deadline, now); !ok {
		return validationError(field)
	}
	return nil
}

func acceptanceDeadlineProblem(deadline, now time.Time) (apperror.Field, bool) {
	const name = "acceptanceDeadline"
	switch {
	case !deadline.After(now):
		return apperror.Field{Field: name, Message: "must be in the future"}, false
	case deadline.Before(now.Add(MinAcceptanceWindow)):
		return apperror.Field{Field: name, Message: "must be at least 10 minutes from now"}, false
	case deadline.After(now.Add(MaxAcceptanceWindow)):
		return apperror.Field{Field: name, Message: "must be at most 24 hours from now"}, false
	default:
		return apperror.Field{}, true
	}
}

// Passed reports whether deadline has passed at now. A deadline has passed
// at the exact instant it falls, so a courier cannot accept at the deadline
// and the expiry job may run at it.
func Passed(deadline, now time.Time) bool {
	return !now.Before(deadline)
}

func validationError(fields ...apperror.Field) error {
	return &apperror.Error{Code: apperror.ValidationFailed, Message: "errand is invalid", Fields: fields}
}
