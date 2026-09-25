package supplier

import "time"

const (
	DefaultPageSize = 25
	MaximumPageSize = 100
)

// SupplierID is a stable, system-generated UUID. It is never accepted in a
// create or update payload.
type SupplierID string

// VersionID is a system-generated UUID for exactly one immutable snapshot.
type VersionID string

// DeletionOperationID is a caller-generated UUID used to make a guarded
// deletion recoverable and idempotent across HTTP and service retries.
type DeletionOperationID string

// Details are the versioned, administrator-editable supplier fields.
type Details struct {
	Name                string
	Type                string
	Building            string
	Floor               string
	LocationDescription string
	Latitude            float64
	Longitude           float64
	OpeningTime         string
	ClosingTime         string
	ImageURL            *string
}

// Version is an immutable supplier snapshot. Available is derived: it is true
// only when this is the current version of a supplier that has not been
// deleted. Superseded and deleted-supplier versions remain readable by ID.
type Version struct {
	SupplierID SupplierID
	VersionID  VersionID
	Details    Details
	Available  bool
	CreatedAt  time.Time
}

// Supplier is the current catalogue projection. Deleted suppliers are not
// returned by current supplier reads, so Available is always true here.
type Supplier struct {
	SupplierID SupplierID
	VersionID  VersionID
	Details    Details
	Available  bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Create is the presence-aware input to the creation use case. Coordinates
// are pointers because zero is a valid coordinate and must remain distinct
// from an omitted mandatory field. IDs and timestamps are supplied by trusted
// service components.
type Create struct {
	Name                string
	Type                string
	Building            string
	Floor               string
	LocationDescription string
	Latitude            *float64
	Longitude           *float64
	OpeningTime         string
	ClosingTime         string
	ImageURL            *string
}

// Patch represents a partial update. ImageURLSet distinguishes an omitted
// image URL from an explicit nil value, which removes the image.
type Patch struct {
	Name                *string
	Type                *string
	Building            *string
	Floor               *string
	LocationDescription *string
	Latitude            *float64
	Longitude           *float64
	OpeningTime         *string
	ClosingTime         *string
	ImageURLSet         bool
	ImageURL            *string
}

func (p Patch) Empty() bool {
	return p.Name == nil &&
		p.Type == nil &&
		p.Building == nil &&
		p.Floor == nil &&
		p.LocationDescription == nil &&
		p.Latitude == nil &&
		p.Longitude == nil &&
		p.OpeningTime == nil &&
		p.ClosingTime == nil &&
		!p.ImageURLSet
}

// Apply returns prospective details for validation. It does not mutate the
// existing version.
func (p Patch) Apply(existing Details) Details {
	updated := existing
	if p.Name != nil {
		updated.Name = *p.Name
	}
	if p.Type != nil {
		updated.Type = *p.Type
	}
	if p.Building != nil {
		updated.Building = *p.Building
	}
	if p.Floor != nil {
		updated.Floor = *p.Floor
	}
	if p.LocationDescription != nil {
		updated.LocationDescription = *p.LocationDescription
	}
	if p.Latitude != nil {
		updated.Latitude = *p.Latitude
	}
	if p.Longitude != nil {
		updated.Longitude = *p.Longitude
	}
	if p.OpeningTime != nil {
		updated.OpeningTime = *p.OpeningTime
	}
	if p.ClosingTime != nil {
		updated.ClosingTime = *p.ClosingTime
	}
	if p.ImageURLSet {
		updated.ImageURL = p.ImageURL
	}
	return updated
}

// ListFilter is the shared query contract. Cursor is opaque to callers. Query
// matches normalized name, building, floor, or location description. Type is
// an exact, case-insensitive match. Repositories order by normalized name and
// then SupplierID, both ascending, before applying the cursor.
type ListFilter struct {
	Query  string
	Type   string
	Limit  int
	Cursor string
}

func (f ListFilter) PageSize() int {
	if f.Limit == 0 {
		return DefaultPageSize
	}
	return f.Limit
}

type Page struct {
	Items      []Supplier
	NextCursor string
}
