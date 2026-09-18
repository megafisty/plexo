package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"plexo/internal/core"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/test/fixtures"
)

// waitMapping polls the manager's cache until it is populated or times out.
func waitMapping(t *testing.T, m *core.Manager) (model.SearchMapping, bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if list, ok := m.Mapping(); ok {
			return list, true
		}
		time.Sleep(time.Millisecond)
	}
	return m.Mapping()
}

// TestManagerMappingLoads: an injected source is loaded once at construction,
// reduced to the search shape, and served from the in-memory cache. It uses the
// committed fixture, never the network.
func TestManagerMappingLoads(t *testing.T) {
	mgr := core.NewManager(context.Background(), core.Config{
		Mapping: core.MappingFunc(func(context.Context) (model.MappingList, error) {
			return fchat.DecodeMappingList(fixtures.MappingList())
		}),
	})
	list, ok := waitMapping(t, mgr)
	if !ok {
		t.Fatal("mapping never loaded")
	}
	if len(list.Kinks.Entries) == 0 || len(list.Genders.Entries) == 0 {
		t.Fatalf("mapping is incomplete: %+v", list)
	}
	if list.Kinks.IDType != model.SearchIDNumber || list.Genders.IDType != model.SearchIDString {
		t.Fatalf("unexpected id types: kinks=%q genders=%q", list.Kinks.IDType, list.Genders.IDType)
	}
}

// TestManagerMappingError: a failed load leaves the cache empty rather than
// publishing a partial result.
func TestManagerMappingError(t *testing.T) {
	mgr := core.NewManager(context.Background(), core.Config{
		Mapping: core.MappingFunc(func(context.Context) (model.MappingList, error) {
			return model.MappingList{}, errors.New("boom")
		}),
	})
	time.Sleep(20 * time.Millisecond)
	if _, ok := mgr.Mapping(); ok {
		t.Fatal("mapping must not be set after a failed load")
	}
}

// TestManagerMappingDisabled: without a source the manager never fetches, so
// the regular test suite and any caller that does not opt in stays offline.
func TestManagerMappingDisabled(t *testing.T) {
	mgr := core.NewManager(context.Background(), core.Config{})
	if _, ok := mgr.Mapping(); ok {
		t.Fatal("mapping must be disabled without a source")
	}
}
