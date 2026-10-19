package cache

import (
	"context"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// OpenErrandsKey is the key, under the "order:" namespace, that holds the
// open-errand set. Bump the version when errand.OpenErrandView changes shape
// so old entries are ignored instead of misread.
const OpenErrandsKey = "open-errands:v1"

// RedisOpenErrandCache is the Redis errand.OpenErrandCache. It stores the
// whole set as one JSON array of errand.OpenErrandView, the redacted
// projection, at "order:open-errands:v1".
type RedisOpenErrandCache struct {
	kv KV
}

var _ errand.OpenErrandCache = (*RedisOpenErrandCache)(nil)

// NewRedisOpenErrandCache returns a cache backed by client.
func NewRedisOpenErrandCache(client *Client) *RedisOpenErrandCache {
	return &RedisOpenErrandCache{kv: client}
}

// Get implements errand.OpenErrandCache. Any failure is a miss.
func (c *RedisOpenErrandCache) Get(ctx context.Context) ([]errand.OpenErrandView, bool) {
	var views []errand.OpenErrandView
	if !c.kv.GetJSON(ctx, OpenErrandsKey, &views) {
		return nil, false
	}
	if views == nil {
		views = []errand.OpenErrandView{}
	}
	return views, true
}

// Set implements errand.OpenErrandCache. A ttl that is not positive stores
// nothing: the open set must never be cached without an expiry. Failures are
// swallowed.
func (c *RedisOpenErrandCache) Set(ctx context.Context, views []errand.OpenErrandView, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	if views == nil {
		views = []errand.OpenErrandView{} // an empty set is cached as [], not null
	}
	c.kv.SetJSON(ctx, OpenErrandsKey, views, ttl)
}

// NoopOpenErrandCache is the errand.OpenErrandCache used when Redis is
// disabled: Get always misses and Set does nothing.
type NoopOpenErrandCache struct{}

var _ errand.OpenErrandCache = NoopOpenErrandCache{}

func (NoopOpenErrandCache) Get(context.Context) ([]errand.OpenErrandView, bool) { return nil, false }

func (NoopOpenErrandCache) Set(context.Context, []errand.OpenErrandView, time.Duration) {}
