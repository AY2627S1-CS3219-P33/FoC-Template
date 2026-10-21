package mine

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// Limit and Cursor parameters in openapi/common.yaml.
const (
	DefaultPageSize = 25
	MaxPageSize     = 100
	maxCursorLength = 512
)

// cursor is nextCursor before encoding. List is the list that issued it, so
// a cursor reused on another list is refused. It holds no user ID: every
// list reads the token's subject, so a cursor passed to another user only
// pages through that user's own errands.
type cursor struct {
	List string    `json:"l"`
	At   time.Time `json:"t"`
	ID   string    `json:"i"`
}

// parseQuery reads limit and cursor for the list named name. UserID is left
// for the caller to set.
func parseQuery(values url.Values, name string) (errand.UserListQuery, error) {
	q := errand.UserListQuery{Limit: DefaultPageSize}
	if values.Has("limit") {
		limit, err := strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > MaxPageSize {
			return errand.UserListQuery{}, invalidArgument("limit must be between 1 and 100")
		}
		q.Limit = limit
	}
	if values.Has("cursor") {
		after, ok := decodeCursor(values.Get("cursor"), name)
		if !ok {
			return errand.UserListQuery{}, invalidArgument("cursor is invalid or was issued for another list")
		}
		q.After = &after
	}
	return q, nil
}

func encodeCursor(name string, after errand.UserListCursor) string {
	raw, _ := json.Marshal(cursor{List: name, At: after.At, ID: after.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s, name string) (errand.UserListCursor, bool) {
	if s == "" || len(s) > maxCursorLength {
		return errand.UserListCursor{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return errand.UserListCursor{}, false
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil || c.List != name ||
		c.At.IsZero() || !errandhttp.UUIDPattern.MatchString(c.ID) {
		return errand.UserListCursor{}, false
	}
	return errand.UserListCursor{At: c.At, ID: strings.ToLower(c.ID)}, true
}

func invalidArgument(message string) error {
	return &apperror.Error{Code: apperror.InvalidArgument, Message: message}
}
