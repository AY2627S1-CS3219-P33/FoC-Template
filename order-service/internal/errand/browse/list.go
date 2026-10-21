package browse

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"slices"
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

// groupWindow is the creation-time bucket of OpenErrandItem.groupKey
// (F3.2.1).
const groupWindow = 15 * time.Minute

// earthRadiusMeters is the mean Earth radius used for distanceMeters.
const earthRadiusMeters = 6_371_008.8

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// point is the courier's position. It orders one response and is never
// stored, logged or cached (F3.2.4).
type point struct {
	lat, lng float64
}

// listQuery is a parsed GET /errands/open query.
type listQuery struct {
	supplierID string
	at         *point
	limit      int
	after      *position
}

// position is where an item sorts: by distance when the query has a point,
// then acceptance deadline, then errand ID.
type position struct {
	Distance float64   `json:"m,omitempty"`
	Deadline time.Time `json:"d"`
	ID       string    `json:"i"`
}

func comparePositions(a, b position) int {
	return cmp.Or(
		cmp.Compare(a.Distance, b.Distance),
		a.Deadline.Compare(b.Deadline),
		strings.Compare(a.ID, b.ID),
	)
}

// cursor is nextCursor before encoding. Filters is a digest of the query's
// filters, so a cursor reused with other filters is refused; a digest keeps
// the coordinates themselves out of the cursor.
type cursor struct {
	Filters string   `json:"f"`
	After   position `json:"a"`
}

func parseListQuery(values url.Values) (listQuery, error) {
	q := listQuery{limit: DefaultPageSize}
	if values.Has("supplierId") {
		id := values.Get("supplierId")
		if !uuidPattern.MatchString(id) {
			return listQuery{}, invalidArgument("supplierId must be a UUID")
		}
		q.supplierID = strings.ToLower(id)
	}

	switch hasLat, hasLng := values.Has("lat"), values.Has("lng"); {
	case hasLat != hasLng:
		return listQuery{}, invalidArgument("lat and lng must be sent together")
	case hasLat:
		lat, ok := coordinate(values.Get("lat"), 90)
		if !ok {
			return listQuery{}, invalidArgument("lat must be a number from -90 to 90")
		}
		lng, ok := coordinate(values.Get("lng"), 180)
		if !ok {
			return listQuery{}, invalidArgument("lng must be a number from -180 to 180")
		}
		q.at = &point{lat: lat, lng: lng}
	}

	if values.Has("limit") {
		limit, err := strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > MaxPageSize {
			return listQuery{}, invalidArgument("limit must be between 1 and 100")
		}
		q.limit = limit
	}

	if values.Has("cursor") {
		after, ok := decodeCursor(values.Get("cursor"), q.filters())
		if !ok {
			return listQuery{}, invalidArgument("cursor is invalid or was issued for other filters")
		}
		q.after = &after
	}
	return q, nil
}

func coordinate(s string, limit float64) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < -limit || v > limit {
		return 0, false
	}
	return v, true
}

// filters is the digest that binds a cursor to supplierId, lat and lng.
func (q listQuery) filters() string {
	var b strings.Builder
	b.WriteString(q.supplierID)
	if q.at != nil {
		b.WriteString("\x00")
		b.WriteString(strconv.FormatFloat(q.at.lat, 'g', -1, 64))
		b.WriteString("\x00")
		b.WriteString(strconv.FormatFloat(q.at.lng, 'g', -1, 64))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func encodeCursor(filters string, after position) string {
	raw, _ := json.Marshal(cursor{Filters: filters, After: after})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s, filters string) (position, bool) {
	if s == "" || len(s) > maxCursorLength {
		return position{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return position{}, false
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Filters != filters ||
		c.After.ID == "" || c.After.Deadline.IsZero() || c.After.Distance < 0 {
		return position{}, false
	}
	return c.After, true
}

// openItem is OpenErrandItem.
type openItem struct {
	errandhttp.OpenFields
	GroupKey       string   `json:"groupKey"`
	DistanceMeters *float64 `json:"distanceMeters,omitempty"`
}

// openPage is OpenErrandPage.
type openPage struct {
	Items      []openItem `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

type candidate struct {
	view errand.OpenErrandView
	pos  position
}

// page filters views for q at now, orders them and cuts one page. An errand
// whose deadline passed after the set was cached is hidden here.
func page(views []errand.OpenErrandView, q listQuery, now time.Time) openPage {
	candidates := make([]candidate, 0, len(views))
	for _, v := range views {
		if !v.AcceptanceDeadline.After(now) || (q.supplierID != "" && v.SupplierID != q.supplierID) {
			continue
		}
		c := candidate{view: v, pos: position{Deadline: v.AcceptanceDeadline, ID: v.ErrandID}}
		if q.at != nil {
			c.pos.Distance = distance(*q.at, point{lat: v.Pickup.Latitude, lng: v.Pickup.Longitude})
		}
		if q.after != nil && comparePositions(c.pos, *q.after) <= 0 {
			continue
		}
		candidates = append(candidates, c)
	}
	slices.SortFunc(candidates, func(a, b candidate) int { return comparePositions(a.pos, b.pos) })

	out := openPage{Items: make([]openItem, 0, min(len(candidates), q.limit))}
	for _, c := range candidates[:min(len(candidates), q.limit)] {
		item := openItem{OpenFields: errandhttp.OpenFieldsOf(c.view), GroupKey: groupKey(c.view)}
		if q.at != nil {
			d := c.pos.Distance
			item.DistanceMeters = &d
		}
		out.Items = append(out.Items, item)
	}
	if len(candidates) > q.limit {
		out.NextCursor = encodeCursor(q.filters(), candidates[q.limit-1].pos)
	}
	return out
}

// groupKey is "{supplierId}:{yyyy-MM-ddTHH:mmZ}" with the creation time
// truncated to 15 minutes in UTC: errands with the same key can be picked up
// together (F3.2.1).
func groupKey(v errand.OpenErrandView) string {
	return v.SupplierID + ":" + v.CreatedAt.UTC().Truncate(groupWindow).Format("2006-01-02T15:04Z")
}

// distance is the great-circle (haversine) distance in meters.
func distance(a, b point) float64 {
	rad := func(deg float64) float64 { return deg * math.Pi / 180 }
	dLat, dLng := rad(b.lat-a.lat), rad(b.lng-a.lng)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(a.lat))*math.Cos(rad(b.lat))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusMeters * math.Asin(math.Min(1, math.Sqrt(h)))
}

func invalidArgument(message string) error {
	return &apperror.Error{Code: apperror.InvalidArgument, Message: message}
}
