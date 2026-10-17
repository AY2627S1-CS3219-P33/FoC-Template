package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
)

// integrationClient returns a Client on TEST_REDIS_URL and a raw go-redis
// client on the same database for fixtures. The open-errand key is deleted
// before and after each test; KV tests use random keys.
func integrationClient(t *testing.T) (*Client, *redis.Client) {
	t.Helper()
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL is not set; skipping Redis cache integration test")
	}
	// A roomier timeout than production so a busy CI machine does not flake.
	client, err := Open(redisURL, Config{CommandTimeout: time.Second})
	require.NoError(t, err)
	opts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	raw := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(ctx))
	require.NoError(t, raw.Del(ctx, KeyPrefix+OpenErrandsKey).Err())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = raw.Del(cleanupCtx, KeyPrefix+OpenErrandsKey).Err()
		_ = raw.Close()
		_ = client.Close()
	})
	return client, raw
}

func randomKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "test:" + hex.EncodeToString(b)
}

func TestRedisOpenErrandCacheRoundTrip(t *testing.T) {
	client, raw := integrationClient(t)
	ctx := context.Background()
	c := NewRedisOpenErrandCache(client)

	_, ok := c.Get(ctx)
	assert.False(t, ok, "empty cache misses")

	c.Set(ctx, sampleViews(), 2*time.Second)
	views, ok := c.Get(ctx)
	require.True(t, ok)
	assert.Equal(t, sampleViews(), views)

	ttl, err := raw.PTTL(ctx, "order:open-errands:v1").Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))
	assert.LessOrEqual(t, ttl, 2*time.Second)
}

func TestRedisOpenErrandCacheExpires(t *testing.T) {
	client, _ := integrationClient(t)
	ctx := context.Background()
	c := NewRedisOpenErrandCache(client)

	c.Set(ctx, sampleViews(), 150*time.Millisecond)
	_, ok := c.Get(ctx)
	require.True(t, ok)
	assert.Eventually(t, func() bool {
		_, ok := c.Get(ctx)
		return !ok
	}, 3*time.Second, 25*time.Millisecond)
}

func TestRedisOpenErrandCacheDecodeFailureIsAMiss(t *testing.T) {
	client, raw := integrationClient(t)
	ctx := context.Background()
	require.NoError(t, raw.Set(ctx, "order:open-errands:v1", "not json", time.Minute).Err())

	views, ok := NewRedisOpenErrandCache(client).Get(ctx)
	assert.False(t, ok)
	assert.Nil(t, views)
	// Bad data is not a Redis failure.
	assert.Equal(t, resilience.StateClosed, client.Breaker().State())
}

func TestRedisStoresOnlyTheRedactedProjection(t *testing.T) {
	client, raw := integrationClient(t)
	ctx := context.Background()
	NewRedisOpenErrandCache(client).Set(ctx, sampleViews(), time.Minute)

	data, err := raw.Get(ctx, "order:open-errands:v1").Bytes()
	require.NoError(t, err)
	assertRedactedProjection(t, data)
}

func TestRedisKVRoundTripAndNamespace(t *testing.T) {
	client, raw := integrationClient(t)
	ctx := context.Background()
	key := randomKey(t)
	t.Cleanup(func() { _ = raw.Del(context.Background(), KeyPrefix+key).Err() })

	type version struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var got version
	assert.False(t, client.GetJSON(ctx, key, &got))

	client.SetJSON(ctx, key, version{ID: "v1", Name: "Frontier"}, 0)
	require.True(t, client.GetJSON(ctx, key, &got))
	assert.Equal(t, version{ID: "v1", Name: "Frontier"}, got)

	exists, err := raw.Exists(ctx, key).Result()
	require.NoError(t, err)
	assert.Zero(t, exists, "keys are stored under the order: namespace")
	ttl, err := raw.TTL(ctx, KeyPrefix+key).Result()
	require.NoError(t, err)
	assert.Equal(t, time.Duration(-1), ttl, "TTL 0 means no expiry")
}

func TestRedisKVExpiresAndMissesOnBadData(t *testing.T) {
	client, raw := integrationClient(t)
	ctx := context.Background()
	key := randomKey(t)
	t.Cleanup(func() { _ = raw.Del(context.Background(), KeyPrefix+key).Err() })

	client.SetJSON(ctx, key, []string{"a"}, 150*time.Millisecond)
	var got []string
	require.True(t, client.GetJSON(ctx, key, &got))
	assert.Eventually(t, func() bool {
		var v []string
		return !client.GetJSON(ctx, key, &v)
	}, 3*time.Second, 25*time.Millisecond)

	require.NoError(t, raw.Set(ctx, KeyPrefix+key, "{", time.Minute).Err())
	assert.False(t, client.GetJSON(ctx, key, &got))
}

func TestRedisPlainMissesDoNotOpenBreaker(t *testing.T) {
	client, _ := integrationClient(t)
	ctx := context.Background()
	for range resilience.DefaultFailureThreshold * 2 {
		var v int
		assert.False(t, client.GetJSON(ctx, randomKey(t), &v))
	}
	assert.Equal(t, resilience.StateClosed, client.Breaker().State())
}
