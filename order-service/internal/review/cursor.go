package review

import (
	"encoding/base64"
	"encoding/json"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// maxCursorLength is the Cursor parameter's maxLength in openapi/common.yaml.
const maxCursorLength = 512

// cursor is the opaque nextCursor: the last review ID of a page and the
// reason filter that produced it. It is not encrypted: a review ID is not
// secret, and a forged one only moves the admin's own position in the
// queue. Unlike an encrypted cursor with a per-process key, it stays valid
// across replicas and restarts.
type cursor struct {
	After  int64               `json:"a"`
	Reason errand.ReviewReason `json:"r,omitempty"`
}

func encodeCursor(after int64, reason errand.ReviewReason) string {
	raw, _ := json.Marshal(cursor{After: after, Reason: reason})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor returns the review ID to continue after, and false when value
// is malformed or was issued for another reason filter (Cursor in
// openapi/common.yaml: reusing it with other filters is INVALID_ARGUMENT).
func decodeCursor(value string, reason errand.ReviewReason) (int64, bool) {
	if len(value) > maxCursorLength {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return 0, false
	}
	var decoded cursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return 0, false
	}
	if decoded.After < 1 || decoded.Reason != reason {
		return 0, false
	}
	return decoded.After, true
}
