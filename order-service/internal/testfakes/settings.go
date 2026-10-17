package testfakes

import (
	"context"
	"sync"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// FakeSettings is an errand.Settings holding one SystemSettings value,
// DefaultSystemSettings until Set.
type FakeSettings struct {
	Faults
	CallLog

	mu       sync.Mutex
	settings errand.SystemSettings
}

var _ errand.Settings = (*FakeSettings)(nil)

func NewFakeSettings() *FakeSettings {
	return &FakeSettings{settings: errand.DefaultSystemSettings()}
}

// Set replaces the settings Get returns.
func (s *FakeSettings) Set(settings errand.SystemSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
}

func (s *FakeSettings) Get(context.Context) (errand.SystemSettings, error) {
	s.record("Get")
	if err := s.fault("Get"); err != nil {
		return errand.SystemSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, nil
}

// FakeOpenErrandCache is an errand.OpenErrandCache in memory. Entries expire
// by the given clock, so tests control the TTL with a FakeClock. A scripted
// fault on "Get" is reported as a miss and one on "Set" drops the write,
