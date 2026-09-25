package supplier

import (
	"math"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

type FieldViolation struct {
	Field   string
	Message string
}

type ValidationError struct {
	Violations []FieldViolation
}

func (e *ValidationError) Error() string {
	return "supplier fields are invalid"
}

var clockTimePattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// NormalizeName defines the single normalization used by create, rename, and
// repository uniqueness checks.
func NormalizeName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

func ValidSupplierID(id SupplierID) bool {
	return uuidPattern.MatchString(string(id))
}

func ValidVersionID(id VersionID) bool {
	return uuidPattern.MatchString(string(id))
}

func ValidDeletionOperationID(id DeletionOperationID) bool {
	return uuidPattern.MatchString(string(id))
}

// ValidateCreate checks presence-sensitive create fields and returns the
// validated domain details used by the repository.
func ValidateCreate(input Create) (Details, error) {
	details := Details{
		Name:                input.Name,
		Type:                input.Type,
		Building:            input.Building,
		Floor:               input.Floor,
		LocationDescription: input.LocationDescription,
		OpeningTime:         input.OpeningTime,
		ClosingTime:         input.ClosingTime,
		ImageURL:            input.ImageURL,
	}
	violations := make([]FieldViolation, 0)
	if input.Latitude == nil {
		violations = append(violations, FieldViolation{Field: "latitude", Message: "is required"})
	} else {
		details.Latitude = *input.Latitude
	}
	if input.Longitude == nil {
		violations = append(violations, FieldViolation{Field: "longitude", Message: "is required"})
	} else {
		details.Longitude = *input.Longitude
	}

	if err := ValidateDetails(details); err != nil {
		validationError := err.(*ValidationError)
		violations = append(violations, validationError.Violations...)
	}
	if len(violations) > 0 {
		return Details{}, &ValidationError{Violations: violations}
	}
	return details, nil
}

// ValidateDetails implements F2.2.1-F2.2.3 for both create and update.
func ValidateDetails(details Details) error {
	violations := make([]FieldViolation, 0)
	checkRequired := func(field, value string, maximum int) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			violations = append(violations, FieldViolation{Field: field, Message: "is required"})
		} else if utf8.RuneCountInString(value) > maximum {
			violations = append(violations, FieldViolation{Field: field, Message: "is too long"})
		}
	}

	checkRequired("name", details.Name, 120)
	checkRequired("type", details.Type, 64)
	checkRequired("building", details.Building, 120)
	checkRequired("floor", details.Floor, 32)
	checkRequired("locationDescription", details.LocationDescription, 500)

	if math.IsNaN(details.Latitude) || math.IsInf(details.Latitude, 0) || details.Latitude < -90 || details.Latitude > 90 {
		violations = append(violations, FieldViolation{Field: "latitude", Message: "must be between -90 and 90"})
	}
	if math.IsNaN(details.Longitude) || math.IsInf(details.Longitude, 0) || details.Longitude < -180 || details.Longitude > 180 {
		violations = append(violations, FieldViolation{Field: "longitude", Message: "must be between -180 and 180"})
	}
	if details.OpeningTime == "" {
		violations = append(violations, FieldViolation{Field: "openingTime", Message: "is required"})
	} else if !clockTimePattern.MatchString(details.OpeningTime) {
		violations = append(violations, FieldViolation{Field: "openingTime", Message: "must use HH:MM in 24-hour time"})
	}
	if details.ClosingTime == "" {
		violations = append(violations, FieldViolation{Field: "closingTime", Message: "is required"})
	} else if !clockTimePattern.MatchString(details.ClosingTime) {
		violations = append(violations, FieldViolation{Field: "closingTime", Message: "must use HH:MM in 24-hour time"})
	}
	if details.OpeningTime != "" && details.OpeningTime == details.ClosingTime {
		violations = append(violations, FieldViolation{Field: "closingTime", Message: "must differ from openingTime"})
	}
	if details.ImageURL != nil {
		validateImageURL(*details.ImageURL, &violations)
	}

	if len(violations) > 0 {
		return &ValidationError{Violations: violations}
	}
	return nil
}

func ValidateListFilter(filter ListFilter) error {
	violations := make([]FieldViolation, 0)
	if filter.Limit < 0 || filter.Limit > MaximumPageSize {
		violations = append(violations, FieldViolation{Field: "limit", Message: "must be between 1 and 100 when provided"})
	}
	if utf8.RuneCountInString(strings.TrimSpace(filter.Query)) > 120 {
		violations = append(violations, FieldViolation{Field: "q", Message: "is too long"})
	}
	if utf8.RuneCountInString(strings.TrimSpace(filter.Type)) > 64 {
		violations = append(violations, FieldViolation{Field: "type", Message: "is too long"})
	}
	if utf8.RuneCountInString(filter.Cursor) > 512 {
		violations = append(violations, FieldViolation{Field: "cursor", Message: "is too long"})
	}
	if len(violations) > 0 {
		return &ValidationError{Violations: violations}
	}
	return nil
}

func validateImageURL(value string, violations *[]FieldViolation) {
	if utf8.RuneCountInString(value) > 2048 {
		*violations = append(*violations, FieldViolation{Field: "imageUrl", Message: "is too long"})
		return
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		*violations = append(*violations, FieldViolation{Field: "imageUrl", Message: "must be an absolute HTTP or HTTPS URL"})
	}
}
