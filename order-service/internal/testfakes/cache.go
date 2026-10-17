package testfakes

import (
	"context"
	"sync"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

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
