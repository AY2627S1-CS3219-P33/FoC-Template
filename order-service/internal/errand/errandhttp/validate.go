package errandhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// Limits of Items in openapi/common.yaml.
const (
	maxItems        = 20
	maxItemQuantity = 99
)

// TextRule is the schema of a string field: at most Max characters, not
// blank when NotBlank, and without control characters when Contact (like
// the user-service profile fields a contact is prefilled from).
type TextRule struct {
	Max      int
	NotBlank bool
	Contact  bool
}

// Text fields of ErrandCreate and ErrandEdit in openapi/common.yaml.
var (
	DeliveryDetailText  = TextRule{Max: 200, NotBlank: true}
	NotesText           = TextRule{Max: 500}
	ContactNameText     = TextRule{Max: 100, NotBlank: true, Contact: true}
	ContactMobileText   = TextRule{Max: 32, NotBlank: true, Contact: true}
	itemDescriptionText = TextRule{Max: 200, NotBlank: true}
)

// MaxErrandBodyBytes bounds an ErrandCreate or ErrandEdit body: 20 items of
// 200 characters, a 500-character note and the short fields, at up to 4
// bytes per character, with room for JSON syntax.
const MaxErrandBodyBytes = 64 << 10

// UUIDPattern matches a UUID in any letter case.
var UUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ReadObject reads a body of at most maxBytes that must be a single JSON
// object, returning its members. Anything else is INVALID_ARGUMENT.
func ReadObject(response http.ResponseWriter, request *http.Request, maxBytes int64) (map[string]json.RawMessage, error) {
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxBytes))
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			return nil, invalidArgument("request body is too large")
		}
		return nil, invalidArgument("request body could not be read")
	}
	return DecodeObject(body)
}

// DecodeObject decodes body, which must be a single JSON object.
func DecodeObject(body []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, invalidArgument("request body is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return nil, invalidArgument("request body must be a JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, invalidArgument("request body must be a single JSON object")
	}
	return raw, nil
}

// Validator reads the members of one JSON object and collects a field error
// for each invalid one, so a body is answered with every problem at once
// (NFR2.2.1). A member that is absent or null counts as missing: a required
// one is reported, an optional one is skipped.
type Validator struct {
	raw map[string]json.RawMessage
	// prefix names the object inside the body, e.g. "items[2]".
	prefix string
	fields []apperror.Field
}

func NewValidator(raw map[string]json.RawMessage) *Validator {
	return &Validator{raw: raw}
}

// Has reports whether the member is present and not null.
func (v *Validator) Has(name string) bool {
	raw, ok := v.raw[name]
	return ok && string(raw) != "null"
}

// Fail records a field error.
func (v *Validator) Fail(name, message string) {
	v.fields = append(v.fields, apperror.Field{Field: v.qualified(name), Message: message})
}

// Add records field errors produced elsewhere, e.g. by the errand domain.
func (v *Validator) Add(fields ...apperror.Field) {
	v.fields = append(v.fields, fields...)
}

// Fields returns the field errors recorded so far.
func (v *Validator) Fields() []apperror.Field { return v.fields }

// Err returns VALIDATION_FAILED with every recorded field, or nil.
func (v *Validator) Err(message string) error {
	if len(v.fields) == 0 {
		return nil
	}
	return &apperror.Error{Code: apperror.ValidationFailed, Message: message, Fields: v.fields}
}

// value returns the member's JSON, or nil when it is absent or null, which
// is reported for a required member.
func (v *Validator) value(name string, required bool) json.RawMessage {
	if !v.Has(name) {
		if required {
			v.Fail(name, "is required")
		}
		return nil
	}
	return v.raw[name]
}

func (v *Validator) string(name string, required bool) (string, bool) {
	raw := v.value(name, required)
	if raw == nil {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		v.Fail(name, "must be a string")
		return "", false
	}
	return s, true
}

// UUID reads a UUID in lower case.
func (v *Validator) UUID(name string, required bool) string {
	s, ok := v.string(name, required)
	if !ok {
		return ""
	}
	if !UUIDPattern.MatchString(s) {
		v.Fail(name, "must be a UUID")
		return ""
	}
	return strings.ToLower(s)
}

// Text reads a string that follows rule.
func (v *Validator) Text(name string, required bool, rule TextRule) string {
	s, ok := v.string(name, required)
	if !ok {
		return ""
	}
	switch {
	case rule.NotBlank && strings.TrimSpace(s) == "":
		v.Fail(name, "must not be blank")
	case utf8.RuneCountInString(s) > rule.Max:
		v.Fail(name, fmt.Sprintf("must be at most %d characters", rule.Max))
	case rule.Contact && strings.ContainsFunc(s, unicode.IsControl):
		v.Fail(name, "must not contain control characters")
	default:
		return s
	}
	return ""
}

// Integer reads a JSON integer within [min, max]. Strings, fractions and
// exponents are refused.
func (v *Validator) Integer(name string, required bool, min, max int64, message string) int {
	raw := v.value(name, required)
	if raw == nil {
		return 0
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < min || n > max {
		v.Fail(name, message)
		return 0
	}
	return int(n)
}

// DateTime reads an RFC 3339 date-time in UTC. It returns nil when the
// member is missing or invalid; ok is false only when it is invalid.
func (v *Validator) DateTime(name string, required bool) (at *time.Time, ok bool) {
	s, present := v.string(name, required)
	if !present {
		return nil, !v.Has(name) && !required
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		v.Fail(name, "must be an RFC 3339 date-time")
		return nil, false
	}
	parsed = parsed.UTC()
	return &parsed, true
}

// item is the Item schema.
type item struct {
	Description string `json:"description"`
	Quantity    int    `json:"quantity"`
}

// Items reads the Items schema and returns it re-encoded as a JSON array of
// Item, or nil when it is missing or invalid.
func (v *Validator) Items(name string, required bool) json.RawMessage {
	raw := v.value(name, required)
	if raw == nil {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		v.Fail(name, "must be an array")
		return nil
	}
	switch {
	case len(list) == 0:
		v.Fail(name, "must list at least one item")
		return nil
	case len(list) > maxItems:
		v.Fail(name, fmt.Sprintf("must list at most %d items", maxItems))
		return nil
	}

	items := make([]item, 0, len(list))
	valid := true
	for i, element := range list {
		at := fmt.Sprintf("%s[%d]", name, i)
		var members map[string]json.RawMessage
		if err := json.Unmarshal(element, &members); err != nil || members == nil {
			v.Fail(at, "must be an object")
			valid = false
			continue
		}
		member := &Validator{raw: members, prefix: v.qualified(at)}
		it := item{
			Description: member.Text("description", true, itemDescriptionText),
			Quantity: member.Integer("quantity", true, 1, maxItemQuantity,
				fmt.Sprintf("must be an integer from 1 to %d", maxItemQuantity)),
		}
		member.OnlyKnown("description", "quantity")
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
		v.Fail(name, "could not be encoded")
		return nil
	}
	return encoded
}

// OnlyKnown reports every member not in known, in name order.
func (v *Validator) OnlyKnown(known ...string) {
	var unknown []string
	for name := range v.raw {
		if !slices.Contains(known, name) {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		v.Fail(name, "is not allowed")
	}
}

func (v *Validator) qualified(name string) string {
	if v.prefix == "" {
		return name
	}
	return v.prefix + "." + name
}

// DeliveryBuilding finds building id in supplier-service's list. It must
// exist and not be retired (ErrandCreate, ErrandEdit); otherwise the error is
// VALIDATION_FAILED on deliveryBuildingId.
func DeliveryBuilding(ctx context.Context, suppliers errand.SupplierGate, id string) (errand.Building, error) {
	buildings, err := suppliers.ListBuildings(ctx)
	if err != nil {
		return errand.Building{}, err
	}
	for _, b := range buildings {
		if !strings.EqualFold(b.ID, id) {
			continue
		}
		if b.Retired {
			return errand.Building{}, buildingError("must not be a retired building")
		}
		return b, nil
	}
	return errand.Building{}, buildingError("must be an existing building")
}

func buildingError(message string) error {
	return &apperror.Error{
		Code:    apperror.ValidationFailed,
		Message: "errand is invalid",
		Fields:  []apperror.Field{{Field: "deliveryBuildingId", Message: message}},
	}
}
