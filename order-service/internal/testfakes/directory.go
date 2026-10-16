package testfakes

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// FakeSupplierGate is an errand.SupplierGate over in-memory suppliers. A
// supplier added with AddSupplier is available until SetUnavailable; opening
// hours are not checked, so tests choose availability directly.
type FakeSupplierGate struct {
	Faults
	CallLog

	mu          sync.Mutex
	current     map[string]string // supplierID → current versionID
	unavailable map[string]bool
	versions    map[string]errand.SupplierVersion
	buildings   []errand.Building
}

var _ errand.SupplierGate = (*FakeSupplierGate)(nil)

func NewFakeSupplierGate() *FakeSupplierGate {
	return &FakeSupplierGate{
		current:     map[string]string{},
		unavailable: map[string]bool{},
		versions:    map[string]errand.SupplierVersion{},
	}
}

// AddSupplier stores v and makes it its supplier's current, available
// version. Earlier versions stay readable through GetVersion.
func (g *FakeSupplierGate) AddSupplier(v errand.SupplierVersion) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if prev, ok := g.versions[g.current[v.SupplierID]]; ok {
		prev.Available = false
		g.versions[prev.VersionID] = prev
	}
	v.Available = true
	g.versions[v.VersionID] = v
	g.current[v.SupplierID] = v.VersionID
	delete(g.unavailable, v.SupplierID)
}

// SetUnavailable makes ResolveForCreate refuse supplierID (deleted or
// closed). Its versions stay readable.
func (g *FakeSupplierGate) SetUnavailable(supplierID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unavailable[supplierID] = true
}

// SetBuildings replaces the building list.
func (g *FakeSupplierGate) SetBuildings(buildings ...errand.Building) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.buildings = slices.Clone(buildings)
}

func (g *FakeSupplierGate) ResolveForCreate(_ context.Context, supplierID string, now time.Time) (errand.SupplierVersion, error) {
	g.record("ResolveForCreate", supplierID, now)
	if err := g.fault("ResolveForCreate"); err != nil {
		return errand.SupplierVersion{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	versionID, ok := g.current[supplierID]
	if !ok || g.unavailable[supplierID] {
		return errand.SupplierVersion{}, appErr(apperror.SupplierUnavailable, "supplier %q is unavailable", supplierID)
	}
	return g.versions[versionID], nil
}

func (g *FakeSupplierGate) GetVersion(_ context.Context, versionID string) (errand.SupplierVersion, error) {
	g.record("GetVersion", versionID)
	if err := g.fault("GetVersion"); err != nil {
		return errand.SupplierVersion{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.versions[versionID]
	if !ok {
		return errand.SupplierVersion{}, appErr(apperror.SupplierUnavailable, "supplier version %q not found", versionID)
	}
	if g.unavailable[v.SupplierID] {
		v.Available = false
	}
	return v, nil
}

func (g *FakeSupplierGate) ListBuildings(context.Context) ([]errand.Building, error) {
	g.record("ListBuildings")
	if err := g.fault("ListBuildings"); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.buildings), nil
}

// FakeSettings is an errand.Settings holding one SystemSettings value,
// DefaultSystemSettings until Set.
type FakeSettings struct {
	Faults
	CallLog

	mu       sync.Mutex
	settings errand.SystemSettings
}

var _ errand.Settings = (*FakeSettings)(nil)

func NewFakeSettings() *FakeSettings {
	return &FakeSettings{settings: errand.DefaultSystemSettings()}
}

// Set replaces the settings Get returns.
func (s *FakeSettings) Set(settings errand.SystemSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
}

func (s *FakeSettings) Get(context.Context) (errand.SystemSettings, error) {
	s.record("Get")
	if err := s.fault("Get"); err != nil {
		return errand.SystemSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, nil
}

// FakeOpenErrandCache is an errand.OpenErrandCache in memory. Entries expire
// by the given clock, so tests control the TTL with a FakeClock. A scripted
// fault on "Get" is reported as a miss and one on "Set" drops the write,
// matching the port's contract that cache errors never reach the caller.
type FakeOpenErrandCache struct {
	Faults
	CallLog

	clock     errand.Clock
	mu        sync.Mutex
	views     []errand.OpenErrandView
	stored    bool
	expiresAt time.Time
}

var _ errand.OpenErrandCache = (*FakeOpenErrandCache)(nil)

func NewFakeOpenErrandCache(clock errand.Clock) *FakeOpenErrandCache {
	return &FakeOpenErrandCache{clock: clock}
}

func (c *FakeOpenErrandCache) Get(context.Context) ([]errand.OpenErrandView, bool) {
	c.record("Get")
	if c.fault("Get") != nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.stored || !c.clock.Now().Before(c.expiresAt) {
		return nil, false
	}
	return cloneViews(c.views), true
}

func (c *FakeOpenErrandCache) Set(_ context.Context, views []errand.OpenErrandView, ttl time.Duration) {
	c.record("Set", len(views), ttl)
	if c.fault("Set") != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.views = cloneViews(views)
	c.stored = true
	c.expiresAt = c.clock.Now().Add(ttl)
}

// Clear empties the cache.
func (c *FakeOpenErrandCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.views, c.stored = nil, false
}

func cloneViews(views []errand.OpenErrandView) []errand.OpenErrandView {
	out := make([]errand.OpenErrandView, len(views))
	for i, v := range views {
		v.DeliveryBuilding = cloneRaw(v.DeliveryBuilding)
		v.Items = cloneRaw(v.Items)
		out[i] = v
	}
	return out
}
