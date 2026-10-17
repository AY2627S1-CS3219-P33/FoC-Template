package cache

import (
	"context"
	"time"
)

// KV is a small JSON key-value cache. Keys are given without the "order:"
// namespace, which the Redis implementation adds. Neither method returns an
// error: a failure of any kind is a miss on GetJSON and a no-op on SetJSON.
type KV interface {
	// GetJSON decodes the value at key into dst and reports true, or reports
	// false on a miss or any failure. dst may be partly written on a decode
	// failure, so callers should not use it after false.
	GetJSON(ctx context.Context, key string, dst any) bool
	// SetJSON stores value, JSON-encoded, at key for ttl. A ttl of 0 means no
	// expiry; a negative ttl stores nothing.
	SetJSON(ctx context.Context, key string, value any, ttl time.Duration)
}

// NoopKV is the KV used when Redis is disabled: every get misses and every
// set does nothing.
type NoopKV struct{}

var _ KV = NoopKV{}

func (NoopKV) GetJSON(context.Context, string, any) bool { return false }

func (NoopKV) SetJSON(context.Context, string, any, time.Duration) {}
