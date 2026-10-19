package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

func TestDelayJittersWithinTwentyPercent(t *testing.T) {
	cases := []struct {
		name string
		rand func(n int64) int64
		want time.Duration
	}{
		{"lowest", func(int64) int64 { return 0 }, 4 * time.Second},
		{"middle", func(n int64) int64 { return n / 2 }, 5 * time.Second},
		{"highest", func(n int64) int64 { return n - 1 }, 6 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(testfakes.NewFakeStore(), testfakes.NewFakeClock(time.Time{}), Config{Rand: tc.rand})
			assert.Equal(t, tc.want, s.delay(5*time.Second))
		})
	}
}

func TestDelayWithJitterDisabled(t *testing.T) {
	s := New(testfakes.NewFakeStore(), testfakes.NewFakeClock(time.Time{}), Config{Jitter: -1})
	assert.Equal(t, 15*time.Second, s.delay(15*time.Second))
}

func TestDelayStaysInBoundsWithTheRealSource(t *testing.T) {
	s := New(testfakes.NewFakeStore(), testfakes.NewFakeClock(time.Time{}), Config{})
	for range 1000 {
		d := s.delay(15 * time.Second)
		assert.GreaterOrEqual(t, d, 12*time.Second)
		assert.LessOrEqual(t, d, 18*time.Second)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}.withDefaults()
	assert.Equal(t, 5*time.Second, cfg.AcceptanceExpiryInterval)
	assert.Equal(t, 15*time.Second, cfg.PickupDeadlineInterval)
	assert.Equal(t, 15*time.Second, cfg.DeliveryDeadlineInterval)
	assert.Equal(t, 5*time.Second, cfg.VerificationLapseInterval)
	assert.Equal(t, 0.2, cfg.Jitter)
	assert.Equal(t, DefaultBatchSize, cfg.BatchSize)
}
