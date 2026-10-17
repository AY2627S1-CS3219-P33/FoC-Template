package supplierclient

import (
	"context"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/cache"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// BuildingsTTL is how long the building list is cached.
const BuildingsTTL = 5 * time.Minute

// Cache keys, without the "order:" namespace cache.KV adds. The version
// key holds only the version ID because GetVersion is given nothing else.
const (
	versionKeyPrefix = "supplier-version:v1:"
	buildingsKey     = "supplier-buildings:v1"
)

// CachedGate is an errand.SupplierGate that caches GetVersion and
// ListBuildings in a cache.KV in front of another gate. Only successful
// answers are cached. ResolveForCreate is passed straight through.
//
// A version's fields never change, so it is cached with no expiry. Its
// Available flag is the exception: a cached version keeps the value it had
// when first read, after its supplier is updated or deleted. Liveness comes
// from ResolveForCreate, never from GetVersion.
type CachedGate struct {
	inner errand.SupplierGate
	kv    cache.KV
}

var _ errand.SupplierGate = (*CachedGate)(nil)

// NewCachedGate wraps inner. A nil kv means cache.NoopKV.
func NewCachedGate(inner errand.SupplierGate, kv cache.KV) *CachedGate {
	if inner == nil {
		panic("supplierclient: inner gate is required")
	}
	if kv == nil {
		kv = cache.NoopKV{}
	}
	return &CachedGate{inner: inner, kv: kv}
}

// ResolveForCreate implements errand.SupplierGate without the cache.
func (g *CachedGate) ResolveForCreate(ctx context.Context, supplierID string, now time.Time) (errand.SupplierVersion, error) {
	return g.inner.ResolveForCreate(ctx, supplierID, now)
}

// GetVersion implements errand.SupplierGate.
func (g *CachedGate) GetVersion(ctx context.Context, versionID string) (errand.SupplierVersion, error) {
	key := versionKeyPrefix + versionID
	var cached errand.SupplierVersion
	if versionID != "" && g.kv.GetJSON(ctx, key, &cached) && cached.VersionID == versionID {
		return cached, nil
	}
	v, err := g.inner.GetVersion(ctx, versionID)
	if err != nil {
		return errand.SupplierVersion{}, err
	}
	g.kv.SetJSON(ctx, key, v, 0)
	return v, nil
}

// ListBuildings implements errand.SupplierGate.
func (g *CachedGate) ListBuildings(ctx context.Context) ([]errand.Building, error) {
	var cached []errand.Building
	if g.kv.GetJSON(ctx, buildingsKey, &cached) && cached != nil {
		return cached, nil
	}
	buildings, err := g.inner.ListBuildings(ctx)
	if err != nil {
		return nil, err
	}
	g.kv.SetJSON(ctx, buildingsKey, buildings, BuildingsTTL)
	return buildings, nil
}
