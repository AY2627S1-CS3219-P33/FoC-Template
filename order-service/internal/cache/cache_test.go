package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

var epoch = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

func sampleViews() []errand.OpenErrandView {
	return []errand.OpenErrandView{{
		ErrandID:          "8f8e8b4e-6a43-4c55-9d4c-1e1f0b6f9a01",
		Version:           3,
		SupplierID:        "supplier-1",
		SupplierVersionID: "supplier-1-v2",
		Pickup: errand.OpenPickupView{
			Name:                "Frontier Canteen",
			Building:            "COM1",
			Floor:               "1",
			LocationDescription: "Next to the lift lobby",
			Latitude:            1.2949,
			Longitude:           103.7737,
		},
		DeliveryBuilding:   json.RawMessage(`{"id":"b-1","name":"UTown Residence"}`),
		DeliveryDetail:     "Level 5 pantry",
		Items:              json.RawMessage(`[{"name":"Chicken rice","quantity":2}]`),
		Reward:             300,
		CreatedAt:          epoch,
		AcceptanceDeadline: epoch.Add(30 * time.Minute),
	}}
}

// closedAddr returns a loopback address with nothing listening on it.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// countingHook counts the commands go-redis attempts to send.
type countingHook struct{ commands atomic.Int64 }

func (h *countingHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *countingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.commands.Add(1)
		return next(ctx, cmd)
	}
}

func (h *countingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func unreachableClient(t *testing.T, cfg Config) (*Client, *countingHook) {
	t.Helper()
	client, err := Open("redis://"+closedAddr(t)+"/0", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	hook := &countingHook{}
	client.rdb.AddHook(hook)
	return client, hook
}

func TestNoopAdaptersAlwaysMiss(t *testing.T) {
	ctx := context.Background()

	var openCache errand.OpenErrandCache = NoopOpenErrandCache{}
	openCache.Set(ctx, sampleViews(), time.Minute)
	views, ok := openCache.Get(ctx)
	assert.False(t, ok)
	assert.Nil(t, views)

	var kv KV = NoopKV{}
	kv.SetJSON(ctx, "k", "v", 0)
	var got string
	assert.False(t, kv.GetJSON(ctx, "k", &got))
	assert.Empty(t, got)
}

func TestOpenRejectsInvalidURLWithoutEchoingIt(t *testing.T) {
	_, err := Open("redis://user:s3cret@%zz/0", Config{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")
}

func TestUnreachableRedisIsAMissNotAnError(t *testing.T) {
	client, _ := unreachableClient(t, Config{})
	ctx := context.Background()

	openCache := NewRedisOpenErrandCache(client)
	start := time.Now()
	openCache.Set(ctx, sampleViews(), time.Second)
	views, ok := openCache.Get(ctx)
	assert.False(t, ok)
	assert.Nil(t, views)

	var got map[string]any
	assert.False(t, client.GetJSON(ctx, "supplier:1:v1", &got))
	client.SetJSON(ctx, "supplier:1:v1", map[string]any{"a": 1}, 0)

	// Each command is bounded by the 50 ms timeout, so the whole sequence
	// is far quicker than an OS connect timeout.
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Error(t, client.Ping(ctx), "Ping reports the failure for readiness")
}

func TestBreakerOpensAndStopsCallingRedis(t *testing.T) {
	clock := testfakes.NewFakeClock(epoch)
	client, hook := unreachableClient(t, Config{
		Breaker: resilience.BreakerConfig{FailureThreshold: 3, CoolDown: 10 * time.Second, Clock: clock},
	})
	ctx := context.Background()
	openCache := NewRedisOpenErrandCache(client)

	for range 3 {
		_, ok := openCache.Get(ctx)
		assert.False(t, ok)
	}
	require.Equal(t, resilience.StateOpen, client.Breaker().State())
	require.EqualValues(t, 3, hook.commands.Load())

	// While open, nothing reaches Redis and everything still misses.
	for range 5 {
		_, ok := openCache.Get(ctx)
		assert.False(t, ok)
		openCache.Set(ctx, sampleViews(), time.Second)
	}
	assert.EqualValues(t, 3, hook.commands.Load())
	assert.ErrorIs(t, client.Ping(ctx), resilience.ErrCircuitOpen)

	// After the cool-down one trial goes through; it fails and reopens.
	clock.Advance(10 * time.Second)
	_, ok := openCache.Get(ctx)
	assert.False(t, ok)
	assert.EqualValues(t, 4, hook.commands.Load())
	assert.Equal(t, resilience.StateOpen, client.Breaker().State())
}

func TestFailuresAreLoggedAtDebugAndRateLimited(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	clock := testfakes.NewFakeClock(epoch)
	client, _ := unreachableClient(t, Config{Logger: logger, Clock: clock, LogInterval: time.Minute})
	ctx := context.Background()
	openCache := NewRedisOpenErrandCache(client)

	for range 4 {
		openCache.Get(ctx)
	}
	lines := logLines(t, &buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "DEBUG", lines[0]["level"])
	assert.Equal(t, "get", lines[0]["op"])
	assert.Equal(t, KeyPrefix+OpenErrandsKey, lines[0]["key"])
	assert.EqualValues(t, 0, lines[0]["suppressed"])

	clock.Advance(time.Minute)
	openCache.Get(ctx)
	lines = logLines(t, &buf)
	require.Len(t, lines, 1)
	assert.EqualValues(t, 3, lines[0]["suppressed"])
}

func TestFailuresAreNotLoggedAboveDebug(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, _ := unreachableClient(t, Config{Logger: logger})
	NewRedisOpenErrandCache(client).Get(context.Background())
	assert.Zero(t, buf.Len())
}

// logLines returns and clears the JSON log records written to buf.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		out = append(out, rec)
	}
	buf.Reset()
	return out
}

// recordingKV is an in-memory KV that keeps raw JSON, so tests can see what
// the open-errand cache would write.
type recordingKV struct {
	raw  map[string][]byte
	ttls map[string]time.Duration
}

func newRecordingKV() *recordingKV {
	return &recordingKV{raw: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (k *recordingKV) GetJSON(_ context.Context, key string, dst any) bool {
	data, ok := k.raw[key]
	return ok && json.Unmarshal(data, dst) == nil
}

func (k *recordingKV) SetJSON(_ context.Context, key string, value any, ttl time.Duration) {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	k.raw[key], k.ttls[key] = data, ttl
}

func TestOpenErrandCacheRoundTripAndTTL(t *testing.T) {
	kv := newRecordingKV()
	c := &RedisOpenErrandCache{kv: kv}
	ctx := context.Background()

	c.Set(ctx, sampleViews(), 2*time.Second)
	assert.Equal(t, 2*time.Second, kv.ttls[OpenErrandsKey])
	views, ok := c.Get(ctx)
	require.True(t, ok)
	assert.Equal(t, sampleViews(), views)
}

func TestOpenErrandCacheEmptySetIsAHit(t *testing.T) {
	kv := newRecordingKV()
	c := &RedisOpenErrandCache{kv: kv}
	ctx := context.Background()

	c.Set(ctx, nil, time.Second)
	assert.JSONEq(t, `[]`, string(kv.raw[OpenErrandsKey]))
	views, ok := c.Get(ctx)
	require.True(t, ok)
	assert.NotNil(t, views)
	assert.Empty(t, views)
}

func TestOpenErrandCacheNeverStoresWithoutExpiry(t *testing.T) {
	kv := newRecordingKV()
	c := &RedisOpenErrandCache{kv: kv}
	c.Set(context.Background(), sampleViews(), 0)
	c.Set(context.Background(), sampleViews(), -time.Second)
	assert.Empty(t, kv.raw)
}

func TestOpenErrandCacheDecodeFailureIsAMiss(t *testing.T) {
	kv := newRecordingKV()
	kv.raw[OpenErrandsKey] = []byte(`{"not":"a list"}`)
	views, ok := (&RedisOpenErrandCache{kv: kv}).Get(context.Background())
	assert.False(t, ok)
	assert.Nil(t, views)
}

// requesterFieldMarkers are substrings of JSON keys that would identify or
// describe the requester or the contact person (NFR3.6.3, NFR3.6.4).
var requesterFieldMarkers = []string{
	"requester", "contact", "user", "owner", "creator", "createdby",
	"email", "mobile", "phone", "note", "auth0", "sub",
}

// openViewKeys is every key the open-list projection may serialize. A new
// field fails this test so that whoever adds it checks it is safe to cache.
var openViewKeys = []string{
	"errandId", "version", "supplierId", "supplierVersionId", "pickup",
	"deliveryBuilding", "deliveryDetail", "items", "reward", "createdAt",
	"acceptanceDeadline",
}

var openPickupKeys = []string{
	"name", "building", "floor", "locationDescription", "latitude", "longitude",
}

// assertRedactedProjection checks serialized cache contents for requester
// identity fields.
func assertRedactedProjection(t *testing.T, data []byte) {
	t.Helper()
	var entries []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &entries))
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		assert.ElementsMatch(t, openViewKeys, keysOf(entry))
		var pickup map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(entry["pickup"], &pickup))
		assert.ElementsMatch(t, openPickupKeys, keysOf(pickup))
		for _, key := range keysOf(entry) {
			lower := strings.ToLower(key)
			for _, marker := range requesterFieldMarkers {
				assert.NotContains(t, lower, marker, "cached field %q looks like requester data", key)
			}
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestSerializedOpenProjectionHasNoRequesterFields(t *testing.T) {
	kv := newRecordingKV()
	(&RedisOpenErrandCache{kv: kv}).Set(context.Background(), sampleViews(), time.Second)
	assertRedactedProjection(t, kv.raw[OpenErrandsKey])
}

func TestKVRejectsNegativeTTLWithoutCallingRedis(t *testing.T) {
	client, hook := unreachableClient(t, Config{})
	client.SetJSON(context.Background(), "k", 1, -time.Second)
	assert.Zero(t, hook.commands.Load())
}

func TestKVEncodeFailureIsSwallowed(t *testing.T) {
	client, hook := unreachableClient(t, Config{})
	client.SetJSON(context.Background(), "k", func() {}, time.Second)
	assert.Zero(t, hook.commands.Load())
}

func TestCallerCancellationDoesNotOpenBreaker(t *testing.T) {
	client, _ := unreachableClient(t, Config{Breaker: resilience.BreakerConfig{FailureThreshold: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var v int
	assert.False(t, client.GetJSON(ctx, "k", &v))
	assert.Equal(t, resilience.StateClosed, client.Breaker().State())
}

func TestNewClientRequiresRedis(t *testing.T) {
	_, err := NewClient(nil, Config{})
	assert.Error(t, err)
}
