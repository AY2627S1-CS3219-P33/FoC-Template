package supplierclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
)

func requireCode(t *testing.T, err error, code apperror.Code) {
	t.Helper()
	var appErr *apperror.Error
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, code, appErr.Code, appErr.Message)
}

func TestOpenAt(t *testing.T) {
	cases := []struct {
		name             string
		opening, closing string
		at               time.Time
		open             bool
	}{
		{"day: just before open", "09:00", "18:00", sgt(8, 59, 59), false},
		{"day: at open", "09:00", "18:00", sgt(9, 0, 0), true},
		{"day: midday", "09:00", "18:00", sgt(12, 30, 0), true},
		{"day: just before close", "09:00", "18:00", sgt(17, 59, 59), true},
		{"day: at close", "09:00", "18:00", sgt(18, 0, 0), false},
		{"day: just after close", "09:00", "18:00", sgt(18, 0, 1), false},
		{"day: midnight", "09:00", "18:00", sgt(0, 0, 0), false},

		{"overnight: just before open", "22:00", "02:00", sgt(21, 59, 0), false},
		{"overnight: at open", "22:00", "02:00", sgt(22, 0, 0), true},
		{"overnight: before midnight", "22:00", "02:00", sgt(23, 59, 59), true},
		{"overnight: at midnight", "22:00", "02:00", sgt(0, 0, 0), true},
		{"overnight: after midnight", "22:00", "02:00", sgt(1, 59, 0), true},
		{"overnight: at close", "22:00", "02:00", sgt(2, 0, 0), false},
		{"overnight: midday", "22:00", "02:00", sgt(12, 0, 0), false},

		{"closes at midnight: last minute", "18:00", "00:00", sgt(23, 59, 0), true},
		{"closes at midnight: at midnight", "18:00", "00:00", sgt(0, 0, 0), false},
		{"opens at midnight", "00:00", "06:00", sgt(0, 0, 0), true},

		{"equal times: open all day", "07:00", "07:00", sgt(6, 59, 0), true},
		{"equal times: at the boundary", "07:00", "07:00", sgt(7, 0, 0), true},

		// 01:30 UTC is 09:30 in Singapore: open, though 01:30 is outside the
		// hours read as UTC.
		{"reads now in Singapore time", "09:00", "18:00", time.Date(2026, 10, 17, 1, 30, 0, 0, time.UTC), true},
		// 10:30 UTC is 18:30 in Singapore: closed, though inside the hours
		// read as UTC.
		{"closed in Singapore though open in UTC", "09:00", "18:00", time.Date(2026, 10, 17, 10, 30, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, err := openAt(tc.opening, tc.closing, tc.at)
			require.NoError(t, err)
			assert.Equal(t, tc.open, open)
		})
	}
}

func TestOpenAtRejectsMalformedHours(t *testing.T) {
	for _, bad := range []string{"", "9:00", "24:00", "09:60", "09:00:00", "0900", "ab:cd"} {
		_, err := openAt(bad, "18:00", sgt(12, 0, 0))
		assert.Error(t, err, "opening %q", bad)
		_, err = openAt("09:00", bad, sgt(12, 0, 0))
		assert.Error(t, err, "closing %q", bad)
	}
}

func TestResolveForCreateOpeningHours(t *testing.T) {
	s := newStub(t)
	s.addCurrent(version(supplierID, versionID, "22:00", "02:00", true))
	c, _ := newTestClient(t, s)
	ctx := context.Background()

	v, err := c.ResolveForCreate(ctx, supplierID, sgt(1, 0, 0))
	require.NoError(t, err)
	assert.Equal(t, versionID, v.VersionID)
	assert.Equal(t, supplierID, v.SupplierID)
	assert.Equal(t, "Frontier Canteen", v.Name)
	assert.True(t, v.Available)

	for _, at := range []time.Time{sgt(21, 59, 59), sgt(2, 0, 0), sgt(12, 0, 0)} {
		_, err := c.ResolveForCreate(ctx, supplierID, at)
		requireCode(t, err, apperror.SupplierUnavailable)
	}
}

func TestResolveForCreateUsesTheCurrentVersion(t *testing.T) {
	s := newStub(t)
	s.addHistorical(version(supplierID, oldVersionID, "09:00", "18:00", false))
	s.addCurrent(version(supplierID, versionID, "09:00", "18:00", true))
	c, _ := newTestClient(t, s)

	v, err := c.ResolveForCreate(context.Background(), supplierID, sgt(10, 0, 0))
	require.NoError(t, err)
	assert.Equal(t, versionID, v.VersionID)
}

func TestDeletedSupplierRejectedForCreateButReadableByVersion(t *testing.T) {
	s := newStub(t)
	// A deleted supplier is absent from GET /suppliers/{id}; its last
	// version stays readable with available false.
	s.addHistorical(version(deletedID, deletedVerID, "00:00", "00:00", false))
	c, _ := newTestClient(t, s)
	ctx := context.Background()

	_, err := c.ResolveForCreate(ctx, deletedID, sgt(12, 0, 0))
	requireCode(t, err, apperror.SupplierUnavailable)

	v, err := c.GetVersion(ctx, deletedVerID)
	require.NoError(t, err)
	assert.Equal(t, deletedID, v.SupplierID)
	assert.Equal(t, deletedVerID, v.VersionID)
	assert.False(t, v.Available)

	// Not-found answers come from a healthy service: the breaker stays
	// closed however many there are.
	for range 10 {
		_, _ = c.ResolveForCreate(ctx, deletedID, sgt(12, 0, 0))
	}
	assert.Equal(t, resilience.StateClosed, c.Breaker().State())
}

func TestResolveForCreateRejectsUnavailableBody(t *testing.T) {
	// Defence in depth: GET /suppliers/{id} documents available as always
	// true, but a false answer must not let a creation through.
	s := newStub(t)
	s.addCurrent(version(supplierID, versionID, "00:00", "00:00", true))
	s.mu.Lock()
	v := s.current[supplierID]
	v.Available = false
	s.current[supplierID] = v
	s.mu.Unlock()
	c, _ := newTestClient(t, s)

	_, err := c.ResolveForCreate(context.Background(), supplierID, sgt(12, 0, 0))
	requireCode(t, err, apperror.SupplierUnavailable)
}

func TestUnknownIDs(t *testing.T) {
	s := newStub(t)
	c, _ := newTestClient(t, s)
	ctx := context.Background()

	_, err := c.ResolveForCreate(ctx, unknownID, sgt(12, 0, 0))
	requireCode(t, err, apperror.SupplierUnavailable)
	_, err = c.GetVersion(ctx, unknownVerID)
	requireCode(t, err, apperror.SupplierUnavailable)

	// Empty IDs are refused without a call.
	before := s.hits.Load()
	_, err = c.ResolveForCreate(ctx, "", sgt(12, 0, 0))
	requireCode(t, err, apperror.SupplierUnavailable)
	_, err = c.GetVersion(ctx, "")
	requireCode(t, err, apperror.SupplierUnavailable)
	assert.Equal(t, before, s.hits.Load())
}

func TestIDsArePathEscaped(t *testing.T) {
	s := newStub(t)
	c, _ := newTestClient(t, s)

	_, err := c.GetVersion(context.Background(), "../buildings")
	requireCode(t, err, apperror.SupplierUnavailable)
	assert.Zero(t, s.count("/buildings"))
}

func TestListBuildingsIncludesRetired(t *testing.T) {
	s := newStub(t)
	s.buildings = []errand.Building{
		{ID: buildingCOM1, Name: "COM1"},
		{ID: buildingOldID, Name: "Old Block", Retired: true},
	}
	c, _ := newTestClient(t, s)

	got, err := c.ListBuildings(context.Background())
	require.NoError(t, err)
	assert.Equal(t, s.buildings, got)
}

func TestListBuildingsEmpty(t *testing.T) {
	s := newStub(t)
	c, _ := newTestClient(t, s)

	got, err := c.ListBuildings(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestDependency500(t *testing.T) {
	s := newStub(t)
	s.addCurrent(version(supplierID, versionID, "00:00", "00:00", true))
	s.setFailure(http.StatusInternalServerError)
	c, _ := newTestClient(t, s)
	ctx := context.Background()

	_, err := c.ResolveForCreate(ctx, supplierID, sgt(12, 0, 0))
	requireCode(t, err, apperror.DependencyUnavailable)
	_, err = c.GetVersion(ctx, versionID)
	requireCode(t, err, apperror.DependencyUnavailable)
	_, err = c.ListBuildings(ctx)
	requireCode(t, err, apperror.DependencyUnavailable)
}

func TestOtherFailuresAreDependencyUnavailable(t *testing.T) {
	ctx := context.Background()

	t.Run("401 from a rejected token", func(t *testing.T) {
		s := newStub(t)
		s.addCurrent(version(supplierID, versionID, "00:00", "00:00", true))
		c, _ := newTestClient(t, s, func(cfg *Config) { cfg.Tokens = staticToken{token: "wrong"} })
		_, err := c.ResolveForCreate(ctx, supplierID, sgt(12, 0, 0))
		requireCode(t, err, apperror.DependencyUnavailable)
	})

	t.Run("token source failure", func(t *testing.T) {
		s := newStub(t)
		c, _ := newTestClient(t, s, func(cfg *Config) { cfg.Tokens = staticToken{err: errors.New("auth0 down")} })
		_, err := c.GetVersion(ctx, versionID)
		requireCode(t, err, apperror.DependencyUnavailable)
		assert.Zero(t, s.hits.Load())
	})

	t.Run("connection refused", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := l.Addr().String()
		require.NoError(t, l.Close())
		c, err := New(Config{BaseURL: "http://" + addr, Tokens: staticToken{token: testToken}})
		require.NoError(t, err)
		_, err = c.ListBuildings(ctx)
		requireCode(t, err, apperror.DependencyUnavailable)
	})

	t.Run("timeout", func(t *testing.T) {
		s := newStub(t)
		s.addCurrent(version(supplierID, versionID, "00:00", "00:00", true))
		s.delay = time.Second
		c, _ := newTestClient(t, s, func(cfg *Config) { cfg.Timeout = 20 * time.Millisecond })
		_, err := c.ResolveForCreate(ctx, supplierID, sgt(12, 0, 0))
		requireCode(t, err, apperror.DependencyUnavailable)
	})

	t.Run("undecodable body", func(t *testing.T) {
		server := newRawServer(t, `{"supplierId":`)
		c, err := New(Config{BaseURL: server, Tokens: staticToken{token: testToken}})
		require.NoError(t, err)
		_, err = c.ResolveForCreate(ctx, supplierID, sgt(12, 0, 0))
		requireCode(t, err, apperror.DependencyUnavailable)
	})

	t.Run("body for another supplier", func(t *testing.T) {
		s := newStub(t)
		s.mu.Lock()
		s.current[supplierID] = version(deletedID, versionID, "00:00", "00:00", true)
		s.mu.Unlock()
		c, _ := newTestClient(t, s)
		_, err := c.ResolveForCreate(ctx, supplierID, sgt(12, 0, 0))
		requireCode(t, err, apperror.DependencyUnavailable)
	})

	t.Run("malformed opening hours", func(t *testing.T) {
		s := newStub(t)
		s.addCurrent(version(supplierID, versionID, "9am", "18:00", true))
		c, _ := newTestClient(t, s)
		_, err := c.ResolveForCreate(ctx, supplierID, sgt(12, 0, 0))
		requireCode(t, err, apperror.DependencyUnavailable)
	})
}

func newRawServer(t *testing.T, body string) string {
	t.Helper()
	s := newStub(t)
	s.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	return s.server.URL
}

func TestBreakerOpensAndRecovers(t *testing.T) {
	s := newStub(t)
	s.addCurrent(version(supplierID, versionID, "00:00", "00:00", true))
	s.setFailure(http.StatusServiceUnavailable)
	c, clock := newTestClient(t, s)
	ctx := context.Background()

	for range 3 {
		_, err := c.GetVersion(ctx, versionID)
		requireCode(t, err, apperror.DependencyUnavailable)
	}
	require.Equal(t, resilience.StateOpen, c.Breaker().State())
	assert.Equal(t, "supplier", c.Breaker().Name())

	// Open: every method fails fast without calling supplier-service.
	hits := s.hits.Load()
	_, err := c.ResolveForCreate(ctx, supplierID, sgt(12, 0, 0))
	requireCode(t, err, apperror.DependencyUnavailable)
	_, err = c.ListBuildings(ctx)
	requireCode(t, err, apperror.DependencyUnavailable)
	assert.Equal(t, hits, s.hits.Load())

	// After the cool-down a successful trial closes it.
	s.setFailure(0)
	clock.Advance(time.Minute)
	_, err = c.GetVersion(ctx, versionID)
	require.NoError(t, err)
	assert.Equal(t, resilience.StateClosed, c.Breaker().State())
}

func TestNewValidatesConfig(t *testing.T) {
	tokens := staticToken{token: testToken}
	for _, base := range []string{"", "supplier-service:8080", "ftp://x", "http://", "http://x/?a=b"} {
		_, err := New(Config{BaseURL: base, Tokens: tokens})
		assert.Error(t, err, "base %q", base)
	}
	_, err := New(Config{BaseURL: "http://supplier-service"})
	assert.Error(t, err)

	c, err := New(Config{BaseURL: "http://supplier-service:8080/", Tokens: tokens})
	require.NoError(t, err)
	assert.Equal(t, DefaultTimeout, c.timeout)
	assert.Equal(t, DefaultBreakerName, c.Breaker().Name())
}

func TestBaseURLWithPathPrefix(t *testing.T) {
	s := newStub(t)
	s.buildings = []errand.Building{{ID: buildingCOM1, Name: "COM1"}}
	inner := s.server.Config.Handler
	s.server.Config.Handler = http.StripPrefix("/supplier-service", inner)
	c, err := New(Config{BaseURL: s.server.URL + "/supplier-service", Tokens: staticToken{token: testToken}})
	require.NoError(t, err)

	got, err := c.ListBuildings(context.Background())
	require.NoError(t, err)
	assert.Len(t, got, 1)
}
