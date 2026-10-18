package supplierclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	testToken     = "service-token"
	supplierID    = "6f1c2a52-8d0b-4a8e-9a55-2b7f1d4b2c01"
	versionID     = "0b8f7c1e-3a7d-4d6e-8f21-9c5e4b3a2d01"
	oldVersionID  = "0b8f7c1e-3a7d-4d6e-8f21-9c5e4b3a2d00"
	deletedID     = "6f1c2a52-8d0b-4a8e-9a55-2b7f1d4b2c02"
	deletedVerID  = "0b8f7c1e-3a7d-4d6e-8f21-9c5e4b3a2d02"
	unknownID     = "6f1c2a52-8d0b-4a8e-9a55-2b7f1d4b2cff"
	unknownVerID  = "0b8f7c1e-3a7d-4d6e-8f21-9c5e4b3a2dff"
	buildingCOM1  = "a1b2c3d4-0000-4000-8000-000000000001"
	buildingOldID = "a1b2c3d4-0000-4000-8000-000000000002"
)

// sgt builds an instant from a Singapore wall-clock time on a fixed day.
func sgt(hour, minute, second int) time.Time {
	return time.Date(2026, 10, 17, hour, minute, second, 0, singapore)
}

func version(supplier, id, opening, closing string, available bool) errand.SupplierVersion {
	return errand.SupplierVersion{
		SupplierID:          supplier,
		VersionID:           id,
		Name:                "Frontier Canteen",
		Type:                "Food",
		TypeID:              "c0ffee00-0000-4000-8000-000000000001",
		Building:            "COM1",
		BuildingID:          buildingCOM1,
		Floor:               "1",
		LocationDescription: "Next to the lift lobby",
		Latitude:            1.2949,
		Longitude:           103.7737,
		OpeningTime:         opening,
		ClosingTime:         closing,
		Available:           available,
		CreatedAt:           time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

// stub is an httptest supplier-service. current holds GET /suppliers/{id}
// answers (deleted suppliers are simply absent, as supplier-service
// reports them 404); versions holds every version, current or not.
type stub struct {
	t      *testing.T
	server *httptest.Server

	mu        sync.Mutex
	current   map[string]errand.SupplierVersion
	versions  map[string]errand.SupplierVersion
	buildings []errand.Building
	// failWith, when non-zero, answers every request with that status.
	failWith int
	// delay is slept (honouring the request context) before answering.
	delay time.Duration

	hits atomic.Int64
	// paths counts requests per path.
	paths sync.Map
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{
		t:        t,
		current:  map[string]errand.SupplierVersion{},
		versions: map[string]errand.SupplierVersion{},
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}

// addCurrent makes v its supplier's current, available version.
func (s *stub) addCurrent(v errand.SupplierVersion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.Available = true
	s.current[v.SupplierID] = v
	s.versions[v.VersionID] = v
}

// addHistorical stores a version reachable only by version ID.
func (s *stub) addHistorical(v errand.SupplierVersion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.Available = false
	s.versions[v.VersionID] = v
}

func (s *stub) setFailure(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWith = status
}

func (s *stub) count(path string) int64 {
	n, ok := s.paths.Load(path)
	if !ok {
		return 0
	}
	return n.(*atomic.Int64).Load()
}

func (s *stub) serve(w http.ResponseWriter, r *http.Request) {
	s.hits.Add(1)
	n, _ := s.paths.LoadOrStore(r.URL.Path, new(atomic.Int64))
	n.(*atomic.Int64).Add(1)

	if r.Method != http.MethodGet {
		s.t.Errorf("unexpected method %s", r.Method)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED")
		return
	}

	s.mu.Lock()
	failWith, delay := s.failWith, s.delay
	s.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if failWith != 0 {
		writeError(w, failWith, "INTERNAL")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/suppliers/"):
		v, ok := s.current[strings.TrimPrefix(r.URL.Path, "/suppliers/")]
		if !ok {
			writeError(w, http.StatusNotFound, "SUPPLIER_NOT_FOUND")
			return
		}
		// The Supplier schema adds updatedAt to the version fields.
		writeJSON(w, struct {
			errand.SupplierVersion
			UpdatedAt time.Time `json:"updatedAt"`
		}{v, v.CreatedAt})
	case strings.HasPrefix(r.URL.Path, "/supplier-versions/"):
		v, ok := s.versions[strings.TrimPrefix(r.URL.Path, "/supplier-versions/")]
		if !ok {
			writeError(w, http.StatusNotFound, "SUPPLIER_VERSION_NOT_FOUND")
			return
		}
		writeJSON(w, v)
	case r.URL.Path == "/buildings":
		items := []errand.Building{}
		for _, b := range s.buildings {
			if !b.Retired || r.URL.Query().Get("includeRetired") == "true" {
				items = append(items, b)
			}
		}
		writeJSON(w, map[string]any{"items": items})
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND")
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": code})
}

type staticToken struct {
	token string
	err   error
}

func (s staticToken) Token(context.Context) (string, error) { return s.token, s.err }

// newTestClient returns a Client for s with a fake-clocked breaker that
// opens after three consecutive failures.
func newTestClient(t *testing.T, s *stub, mutate ...func(*Config)) (*Client, *testfakes.FakeClock) {
	t.Helper()
	clock := testfakes.NewFakeClock(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	cfg := Config{
		BaseURL: s.server.URL + "/",
		Tokens:  staticToken{token: testToken},
		Timeout: time.Second,
		Breaker: resilience.BreakerConfig{FailureThreshold: 3, CoolDown: time.Minute, Clock: clock},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := New(cfg)
	require.NoError(t, err)
	return c, clock
}

// memKV is an in-memory cache.KV that round-trips through JSON like the
// Redis client and records TTLs.
type memKV struct {
	mu   sync.Mutex
	data map[string][]byte
	ttls map[string]time.Duration
	gets atomic.Int64
}

func newMemKV() *memKV {
	return &memKV{data: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (m *memKV) GetJSON(_ context.Context, key string, dst any) bool {
	m.gets.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	return ok && json.Unmarshal(b, dst) == nil
}

func (m *memKV) SetJSON(_ context.Context, key string, value any, ttl time.Duration) {
	b, err := json.Marshal(value)
	if err != nil || ttl < 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	m.ttls[key] = ttl
}

func (m *memKV) expire(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
}
