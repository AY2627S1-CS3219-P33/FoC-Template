package create

import (
<<<<<<< HEAD
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

=======
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Limits of ErrandCreate in openapi/common.yaml.
const (
	maxItems           = 20
	maxItemDescription = 200
	maxItemQuantity    = 99
	maxDeliveryDetail  = 200
	maxNotes           = 500
	maxContactName     = 100
	maxContactMobile   = 32
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
// Input is a validated ErrandCreate body. UUIDs are lower case, Items is
// re-encoded as a JSON array of Item, and AcceptanceDeadline is resolved
// (the default when the body has none).
type Input struct {
	SupplierID         string
	DeliveryBuildingID string
	DeliveryDetail     string
	Items              json.RawMessage
	Notes              string
	ContactName        string
	ContactMobile      string
	Reward             int
	AcceptanceDeadline time.Time
}

<<<<<<< HEAD
=======
// item is the Item schema.
type item struct {
	Description string `json:"description"`
	Quantity    int    `json:"quantity"`
}

>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
// parseInput validates an ErrandCreate body at now. Every invalid field is
// reported in one VALIDATION_FAILED (NFR2.2.1); a body that is not a single
// JSON object is INVALID_ARGUMENT. Whether the supplier and building exist is
// checked later, against supplier-service.
func parseInput(body []byte, now time.Time) (Input, error) {
<<<<<<< HEAD
	raw, err := errandhttp.DecodeObject(body)
	if err != nil {
		return Input{}, err
	}
	v := errandhttp.NewValidator(raw)
	in := Input{
		SupplierID:         v.UUID("supplierId", true),
		DeliveryBuildingID: v.UUID("deliveryBuildingId", true),
		DeliveryDetail:     v.Text("deliveryDetail", true, errandhttp.DeliveryDetailText),
		Items:              v.Items("items", true),
		Notes:              v.Text("notes", false, errandhttp.NotesText),
		ContactName:        v.Text("contactName", true, errandhttp.ContactNameText),
		ContactMobile:      v.Text("contactMobile", true, errandhttp.ContactMobileText),
		Reward:             v.Integer("reward", true, 1, math.MaxInt32, "must be a positive integer"),
		AcceptanceDeadline: acceptanceDeadline(v, "acceptanceDeadline", now),
	}
	v.OnlyKnown("supplierId", "deliveryBuildingId", "deliveryDetail", "items", "notes",
		"contactName", "contactMobile", "reward", "acceptanceDeadline")
	if err := v.Err("errand is invalid"); err != nil {
		return Input{}, err
=======
	if len(bytes.TrimSpace(body)) == 0 {
		return Input{}, invalidArgument("request body is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return Input{}, invalidArgument("request body must be a JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Input{}, invalidArgument("request body must be a single JSON object")
	}

	v := &validator{raw: raw}
	in := Input{
		SupplierID:         v.uuid("supplierId"),
		DeliveryBuildingID: v.uuid("deliveryBuildingId"),
		DeliveryDetail:     v.text("deliveryDetail", true, maxDeliveryDetail, false),
		Items:              v.items("items"),
		Notes:              v.text("notes", false, maxNotes, false),
		ContactName:        v.text("contactName", true, maxContactName, true),
		ContactMobile:      v.text("contactMobile", true, maxContactMobile, true),
		Reward:             v.integer("reward", 1, math.MaxInt32, "must be a positive integer"),
		AcceptanceDeadline: v.acceptanceDeadline("acceptanceDeadline", now),
	}
	v.onlyKnown("supplierId", "deliveryBuildingId", "deliveryDetail", "items", "notes",
		"contactName", "contactMobile", "reward", "acceptanceDeadline")
	if len(v.fields) > 0 {
		return Input{}, &apperror.Error{Code: apperror.ValidationFailed, Message: "errand is invalid", Fields: v.fields}
>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
	}
	return in, nil
}

<<<<<<< HEAD
// acceptanceDeadline reads the optional deadline and applies F3.1.3's
// bounds, returning the default when it is absent.
func acceptanceDeadline(v *errandhttp.Validator, name string, now time.Time) time.Time {
	requested, ok := v.DateTime(name, false)
	if !ok {
		return time.Time{}
=======
// validator reads the members of one JSON object and collects a field error
// for each invalid one. prefix names the object inside the body, e.g.
// "items[2]".
type validator struct {
	raw    map[string]json.RawMessage
	prefix string
	fields []apperror.Field
}

func (v *validator) fail(name, message string) {
	if v.prefix != "" {
		name = v.prefix + "." + name
	}
	v.fields = append(v.fields, apperror.Field{Field: name, Message: message})
}

// value returns the member's JSON, or nil when it is absent or null, which
// is reported for a required member.
func (v *validator) value(name string, required bool) json.RawMessage {
	raw, ok := v.raw[name]
	if !ok || string(raw) == "null" {
		if required {
			v.fail(name, "is required")
		}
		return nil
	}
	return raw
}

func (v *validator) string(name string, required bool) (string, bool) {
	raw := v.value(name, required)
	if raw == nil {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		v.fail(name, "must be a string")
		return "", false
	}
	return s, true
}

func (v *validator) uuid(name string) string {
	s, ok := v.string(name, true)
	if !ok {
		return ""
	}
	if !uuidPattern.MatchString(s) {
		v.fail(name, "must be a UUID")
		return ""
	}
	return strings.ToLower(s)
}

// text reads a string of at most max characters. A required one must not be
// blank; a contact field must not contain control characters, like the
// user-service profile fields it is prefilled from.
func (v *validator) text(name string, required bool, max int, contact bool) string {
	s, ok := v.string(name, required)
	if !ok {
		return ""
	}
	switch {
	case required && strings.TrimSpace(s) == "":
		v.fail(name, "must not be blank")
	case utf8.RuneCountInString(s) > max:
		v.fail(name, fmt.Sprintf("must be at most %d characters", max))
	case contact && strings.ContainsFunc(s, unicode.IsControl):
		v.fail(name, "must not contain control characters")
	default:
		return s
	}
	return ""
}

// integer reads a required JSON integer within [min, max]. Strings, fractions
// and exponents are refused.
func (v *validator) integer(name string, min, max int64, message string) int {
	raw := v.value(name, true)
	if raw == nil {
		return 0
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < min || n > max {
		v.fail(name, message)
		return 0
	}
	return int(n)
}

func (v *validator) items(name string) json.RawMessage {
	raw := v.value(name, true)
	if raw == nil {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		v.fail(name, "must be an array")
		return nil
	}
	switch {
	case len(list) == 0:
		v.fail(name, "must list at least one item")
		return nil
	case len(list) > maxItems:
		v.fail(name, fmt.Sprintf("must list at most %d items", maxItems))
		return nil
	}

	items := make([]item, 0, len(list))
	valid := true
	for i, element := range list {
		at := fmt.Sprintf("%s[%d]", name, i)
		var members map[string]json.RawMessage
		if err := json.Unmarshal(element, &members); err != nil || members == nil {
			v.fail(at, "must be an object")
			valid = false
			continue
		}
		member := &validator{raw: members, prefix: v.qualified(at)}
		it := item{
			Description: member.text("description", true, maxItemDescription, false),
			Quantity: member.integer("quantity", 1, maxItemQuantity,
				fmt.Sprintf("must be an integer from 1 to %d", maxItemQuantity)),
		}
		member.onlyKnown("description", "quantity")
		if len(member.fields) > 0 {
			v.fields = append(v.fields, member.fields...)
			valid = false
			continue
		}
		items = append(items, it)
	}
	if !valid {
		return nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		v.fail(name, "could not be encoded")
		return nil
	}
	return encoded
}

func (v *validator) qualified(name string) string {
	if v.prefix == "" {
		return name
	}
	return v.prefix + "." + name
}

// acceptanceDeadline reads the optional deadline and applies F3.1.3's
// bounds, returning the default when it is absent.
func (v *validator) acceptanceDeadline(name string, now time.Time) time.Time {
	var requested *time.Time
	if raw := v.value(name, false); raw != nil {
		s, ok := v.string(name, false)
		if !ok {
			return time.Time{}
		}
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			v.fail(name, "must be an RFC 3339 date-time")
			return time.Time{}
		}
		parsed = parsed.UTC()
		requested = &parsed
>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
	}
	deadline, err := errand.AcceptanceDeadline(requested, now)
	if err != nil {
		var appErr *apperror.Error
		if errors.As(err, &appErr) {
<<<<<<< HEAD
			v.Add(appErr.Fields...)
		} else {
			v.Fail(name, "is invalid")
=======
			v.fields = append(v.fields, appErr.Fields...)
		} else {
			v.fail(name, "is invalid")
>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
		}
		return time.Time{}
	}
	return deadline
}
<<<<<<< HEAD
=======

// onlyKnown reports every member not in known, in name order.
func (v *validator) onlyKnown(known ...string) {
	var unknown []string
	for name := range v.raw {
		if !slices.Contains(known, name) {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		v.fail(name, "is not allowed")
	}
}

func invalidArgument(message string) error {
	return &apperror.Error{Code: apperror.InvalidArgument, Message: message}
}
>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
