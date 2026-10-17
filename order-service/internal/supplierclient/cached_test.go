package supplierclient

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/cache"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

func cachedFixture(t *testing.T, kv cache.KV) (*CachedGate, *stub) {
	t.Helper()
	s := newStub(t)
	s.addCurrent(version(supplierID, versionID, "09:00", "18:00", true))
	s.addHistorical(version(deletedID, deletedVerID, "09:00", "18:00", false))
	s.buildings = []errand.Building{
		{ID: buildingCOM1, Name: "COM1"},
		{ID: buildingOldID, Name: "Old Block", Retired: true},
	}
	c, _ := newTestClient(t, s)
	return NewCachedGate(c, kv), s
}

func TestCachedGetVersionHitAvoidsSecondCall(t *testing.T) {
	kv := newMemKV()
	g, s := cachedFixture(t, kv)
	ctx := context.Background()

	first, err := g.GetVersion(ctx, deletedVerID)
	require.NoError(t, err)
	second, err := g.GetVersion(ctx, deletedVerID)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, int64(1), s.count("/supplier-versions/"+deletedVerID))
	assert.Equal(t, time.Duration(0), kv.ttls[versionKeyPrefix+deletedVerID], "versions never expire")

	// The cache stays good when supplier-service goes down.
	s.setFailure(http.StatusInternalServerError)
	third, err := g.GetVersion(ctx, deletedVerID)
	require.NoError(t, err)
	assert.Equal(t, first, third)
}

func TestCachedListBuildingsHitAvoidsSecondCall(t *testing.T) {
	kv := newMemKV()
	g, s := cachedFixture(t, kv)
	ctx := context.Background()

	first, err := g.ListBuildings(ctx)
	require.NoError(t, err)
	second, err := g.ListBuildings(ctx)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Len(t, second, 2)
	assert.Equal(t, int64(1), s.count("/buildings"))
	assert.Equal(t, 5*time.Minute, kv.ttls[buildingsKey])

	// Once the entry expires the next read goes to supplier-service again.
	kv.expire(buildingsKey)
	_, err = g.ListBuildings(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), s.count("/buildings"))
}

func TestCachedResolveForCreateIsNeverCached(t *testing.T) {
	kv := newMemKV()
	g, s := cachedFixture(t, kv)
	ctx := context.Background()

	_, err := g.ResolveForCreate(ctx, supplierID, sgt(10, 0, 0))
	require.NoError(t, err)
	// Same supplier, now closed: answered live, not from the earlier result.
	_, err = g.ResolveForCreate(ctx, supplierID, sgt(19, 0, 0))
	requireCode(t, err, apperror.SupplierUnavailable)

	assert.Equal(t, int64(2), s.count("/suppliers/"+supplierID))
	assert.Zero(t, kv.gets.Load())
	assert.Empty(t, kv.data)
}

func TestCachedErrorsAreNotCached(t *testing.T) {
	kv := newMemKV()
	g, s := cachedFixture(t, kv)
	ctx := context.Background()

	_, err := g.GetVersion(ctx, unknownVerID)
	requireCode(t, err, apperror.SupplierUnavailable)

	s.setFailure(http.StatusInternalServerError)
	_, err = g.ListBuildings(ctx)
	requireCode(t, err, apperror.DependencyUnavailable)
	_, err = g.GetVersion(ctx, versionID)
	requireCode(t, err, apperror.DependencyUnavailable)

	assert.Empty(t, kv.data)

	// Recovery is immediate: no failure was stored.
	s.setFailure(0)
	_, err = g.ListBuildings(ctx)
	require.NoError(t, err)
}

// brokenKV is a cache whose every get fails and every set is lost, as
// cache.Client reports an unreachable Redis.
type brokenKV struct{ gets, sets int }

func (b *brokenKV) GetJSON(context.Context, string, any) bool { b.gets++; return false }

func (b *brokenKV) SetJSON(context.Context, string, any, time.Duration) { b.sets++ }

func TestCacheUnavailableFallsThrough(t *testing.T) {
	for name, kv := range map[string]cache.KV{
		"broken": &brokenKV{},
		"noop":   cache.NoopKV{},
		"nil":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			g, s := cachedFixture(t, kv)
			ctx := context.Background()

			for range 2 {
				v, err := g.GetVersion(ctx, versionID)
				require.NoError(t, err)
				assert.Equal(t, versionID, v.VersionID)
				b, err := g.ListBuildings(ctx)
				require.NoError(t, err)
				assert.Len(t, b, 2)
			}
			assert.Equal(t, int64(2), s.count("/supplier-versions/"+versionID))
			assert.Equal(t, int64(2), s.count("/buildings"))
		})
	}
}

func TestCachedIgnoresMismatchedEntry(t *testing.T) {
	// An entry whose body names another version (a corrupted or
	// mis-keyed value) is treated as a miss.
	kv := newMemKV()
	g, s := cachedFixture(t, kv)
	kv.SetJSON(context.Background(), versionKeyPrefix+versionID, version(deletedID, deletedVerID, "09:00", "18:00", false), 0)

	v, err := g.GetVersion(context.Background(), versionID)
	require.NoError(t, err)
	assert.Equal(t, versionID, v.VersionID)
	assert.Equal(t, int64(1), s.count("/supplier-versions/"+versionID))
}
