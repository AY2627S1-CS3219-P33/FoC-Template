package review

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
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

// eventCursor is the event log's nextCursor: the last row's (at, id) and a
// short hash of the filters that produced it. The hash keeps the cursor
// short however long userId is.
type eventCursor struct {
	At      time.Time `json:"t"`
	ID      int64     `json:"i"`
	Filters string    `json:"f"`
}

func encodeEventCursor(last opsrepo.TransitionCursor, query EventQuery) string {
	raw, _ := json.Marshal(eventCursor{At: last.At.UTC(), ID: last.ID, Filters: eventFilterHash(query)})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeEventCursor(value string, query EventQuery) (opsrepo.TransitionCursor, bool) {
	if len(value) > maxCursorLength {
		return opsrepo.TransitionCursor{}, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return opsrepo.TransitionCursor{}, false
	}
	var decoded eventCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return opsrepo.TransitionCursor{}, false
	}
	if decoded.ID < 1 || decoded.At.IsZero() || decoded.Filters != eventFilterHash(query) {
		return opsrepo.TransitionCursor{}, false
	}
	return opsrepo.TransitionCursor{At: decoded.At, ID: decoded.ID}, true
}

func eventFilterHash(query EventQuery) string {
	bound := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		query.UserID, query.ErrandID, string(query.Type), bound(query.From), bound(query.To),
	}, "\x00")))
	return hex.EncodeToString(sum[:8])
}
