package errandhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
)

// maxActionBytes bounds an action body: an expectedVersion and, for
// ReasonedAction, a reason of at most 500 characters (4 bytes each).
const maxActionBytes = 4 << 10

// ErrandID reads the errandId path value, a UUID, in lower case.
func ErrandID(request *http.Request) (string, error) {
	id := request.PathValue("errandId")
	if !UUIDPattern.MatchString(id) {
		return "", &apperror.Error{Code: apperror.InvalidArgument, Message: "errandId must be a UUID"}
	}
	return strings.ToLower(id), nil
}

// ReadVersionedAction reads a VersionedAction body and returns its
// expectedVersion. A body that is not a single JSON object is
// INVALID_ARGUMENT; a missing or invalid expectedVersion, or any other
// member, is VALIDATION_FAILED.
func ReadVersionedAction(response http.ResponseWriter, request *http.Request) (int, error) {
	raw, err := ReadObject(response, request, maxActionBytes)
	if err != nil {
		return 0, err
	}
	v := NewValidator(raw)
	version := v.ExpectedVersion()
	v.OnlyKnown("expectedVersion")
	if err := v.Err("request body is invalid"); err != nil {
		return 0, err
	}
	return version, nil
}

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
