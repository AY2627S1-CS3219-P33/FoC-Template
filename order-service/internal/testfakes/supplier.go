// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-18
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

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
// supplier added with AddSupplier is available until SetUnavailable (deleted)
// or SetClosed; opening hours are not checked, so tests choose availability
// directly.
type FakeSupplierGate struct {
	Faults
	CallLog

	mu          sync.Mutex
	current     map[string]string // supplierID → current versionID
	unavailable map[string]bool
	closed      map[string]bool
	versions    map[string]errand.SupplierVersion
	buildings   []errand.Building
}

var _ errand.SupplierGate = (*FakeSupplierGate)(nil)

func NewFakeSupplierGate() *FakeSupplierGate {
	return &FakeSupplierGate{
		current:     map[string]string{},
		unavailable: map[string]bool{},
		closed:      map[string]bool{},
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
	delete(g.closed, v.SupplierID)
}

// SetUnavailable makes ResolveForCreate and GetCurrent refuse supplierID, as
// for a deleted supplier. Its versions stay readable.
func (g *FakeSupplierGate) SetUnavailable(supplierID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unavailable[supplierID] = true
}

// SetClosed makes ResolveForCreate refuse supplierID, as outside its opening
// hours. GetCurrent still returns it.
func (g *FakeSupplierGate) SetClosed(supplierID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed[supplierID] = true
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
	if !ok || g.unavailable[supplierID] || g.closed[supplierID] {
		return errand.SupplierVersion{}, appErr(apperror.SupplierUnavailable, "supplier %q is unavailable", supplierID)
	}
	return g.versions[versionID], nil
}

func (g *FakeSupplierGate) GetCurrent(_ context.Context, supplierID string) (errand.SupplierVersion, error) {
	g.record("GetCurrent", supplierID)
	if err := g.fault("GetCurrent"); err != nil {
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
