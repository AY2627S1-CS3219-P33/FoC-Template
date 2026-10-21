package errandhttp

import (
<<<<<<< HEAD
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
=======
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
	"strconv"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
)

// maxActionBytes bounds an action body: an expectedVersion and, for
// ReasonedAction, a reason of at most 500 characters (4 bytes each).
const maxActionBytes = 4 << 10

<<<<<<< HEAD
// ErrandID reads the errandId path value, a UUID, in lower case.
func ErrandID(request *http.Request) (string, error) {
	id := request.PathValue("errandId")
	if !UUIDPattern.MatchString(id) {
=======
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ErrandID reads the errandId path value, a UUID, in lower case.
func ErrandID(request *http.Request) (string, error) {
	id := request.PathValue("errandId")
	if !uuidPattern.MatchString(id) {
>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
		return "", &apperror.Error{Code: apperror.InvalidArgument, Message: "errandId must be a UUID"}
	}
	return strings.ToLower(id), nil
}

// ReadVersionedAction reads a VersionedAction body and returns its
// expectedVersion. A body that is not a single JSON object is
// INVALID_ARGUMENT; a missing or invalid expectedVersion, or any other
// member, is VALIDATION_FAILED.
func ReadVersionedAction(response http.ResponseWriter, request *http.Request) (int, error) {
<<<<<<< HEAD
	raw, err := ReadObject(response, request, maxActionBytes)
	if err != nil {
		return 0, err
	}
	v := NewValidator(raw)
	version := v.ExpectedVersion()
	v.OnlyKnown("expectedVersion")
	if err := v.Err("request body is invalid"); err != nil {
		return 0, err
=======
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxActionBytes))
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			return 0, invalidArgument("request body is too large")
		}
		return 0, invalidArgument("request body could not be read")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return 0, invalidArgument("request body is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return 0, invalidArgument("request body must be a JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return 0, invalidArgument("request body must be a single JSON object")
	}

	var fields []apperror.Field
	version := 0
	switch value, ok := raw["expectedVersion"]; {
	case !ok || string(value) == "null":
		fields = append(fields, apperror.Field{Field: "expectedVersion", Message: "is required"})
	default:
		n, err := strconv.ParseInt(string(value), 10, 32)
		if err != nil || n < 1 {
			fields = append(fields, apperror.Field{Field: "expectedVersion", Message: "must be a positive integer"})
		} else {
			version = int(n)
		}
	}
	var unknown []string
	for name := range raw {
		if name != "expectedVersion" {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		fields = append(fields, apperror.Field{Field: name, Message: "is not allowed"})
	}
	if len(fields) > 0 {
		return 0, &apperror.Error{Code: apperror.ValidationFailed, Message: "request body is invalid", Fields: fields}
>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
	}
	return version, nil
}

<<<<<<< HEAD
// reasonText is ReasonedAction's reason.
var reasonText = TextRule{Max: 500, NotBlank: true}

// ReadReasonedAction reads a ReasonedAction body: expectedVersion and a
// required, non-blank reason of at most 500 characters. Errors are as for
// ReadVersionedAction.
func ReadReasonedAction(response http.ResponseWriter, request *http.Request) (version int, reason string, err error) {
	raw, err := ReadObject(response, request, maxActionBytes)
	if err != nil {
		return 0, "", err
	}
	v := NewValidator(raw)
	version = v.ExpectedVersion()
	reason = v.Text("reason", true, reasonText)
	v.OnlyKnown("expectedVersion", "reason")
	if err := v.Err("request body is invalid"); err != nil {
		return 0, "", err
	}
	return version, reason, nil
}

// ExpectedVersion reads the required expectedVersion, a positive 32-bit
// integer.
func (v *Validator) ExpectedVersion() int {
	return v.Integer("expectedVersion", true, 1, math.MaxInt32, "must be a positive integer")
}

=======
>>>>>>> eecf0136af1d475ffece2cedd4de8b51e4d48837
// RejectionRequestID is the requestId of a rejection event
// (Store.RecordRejection): a digest of who acted and the version they acted
// on. The service has no request-ID header, and this gives the property the
// contract asks of one: a retry of the same refused request (same caller,
// same expectedVersion) gets the same outbox key and notifies once. The
// errand and action are already part of the key.
func RejectionRequestID(actorID string, expectedVersion int) string {
	sum := sha256.Sum256([]byte(actorID + "\x00" + strconv.Itoa(expectedVersion)))
	return hex.EncodeToString(sum[:16])
}

func invalidArgument(message string) error {
	return &apperror.Error{Code: apperror.InvalidArgument, Message: message}
}
