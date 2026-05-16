//go:build unit

package systemplanetest_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/systemplanetest"
)

// memEntry is an in-memory key-value entry.
type memEntry struct {
	ns, key, tenantID, updatedBy string
	value                        []byte
	updatedAt                    time.Time
}

// memStore is a minimal in-memory implementation of systemplane.TestStore for unit testing.
type memStore struct {
	mu            sync.Mutex
	entries       []memEntry
	handlers      map[int]func(systemplane.TestEvent)
	nextHandlerID int
	closed        bool
}

func newMemStore() *memStore {
	return &memStore{handlers: make(map[int]func(systemplane.TestEvent))}
}

func (m *memStore) List(_ context.Context) ([]systemplane.TestEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]systemplane.TestEntry, len(m.entries))
	for i, e := range m.entries {
		out[i] = systemplane.TestEntry{
			Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
			Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
		}
	}

	return out, nil
}

func (m *memStore) Get(_ context.Context, namespace, key string) (systemplane.TestEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range m.entries {
		if e.ns == namespace && e.key == key {
			return systemplane.TestEntry{
				Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
				Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
			}, true, nil
		}
	}

	return systemplane.TestEntry{}, false, nil
}

func (m *memStore) Set(_ context.Context, e systemplane.TestEntry) error {
	m.mu.Lock()

	updated := false
	for i, existing := range m.entries {
		if existing.ns == e.Namespace && existing.key == e.Key && existing.tenantID == e.TenantID {
			m.entries[i] = memEntry{
				ns: e.Namespace, key: e.Key, tenantID: e.TenantID,
				value: e.Value, updatedAt: e.UpdatedAt, updatedBy: e.UpdatedBy,
			}
			updated = true
			break
		}
	}

	if !updated {
		m.entries = append(m.entries, memEntry{
			ns: e.Namespace, key: e.Key, tenantID: e.TenantID,
			value: e.Value, updatedAt: e.UpdatedAt, updatedBy: e.UpdatedBy,
		})
	}

	handlers := make([]func(systemplane.TestEvent), 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	m.mu.Unlock()

	evt := systemplane.TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: e.TenantID}
	for _, h := range handlers {
		h(evt)
	}

	return nil
}

func (m *memStore) Subscribe(ctx context.Context, handler func(systemplane.TestEvent)) error {
	m.mu.Lock()
	m.nextHandlerID++
	handlerID := m.nextHandlerID
	m.handlers[handlerID] = handler
	m.mu.Unlock()

	<-ctx.Done()

	m.mu.Lock()
	delete(m.handlers, handlerID)
	m.mu.Unlock()

	return nil
}

func (m *memStore) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	return nil
}

func (m *memStore) GetTenantValue(_ context.Context, tenantID, namespace, key string) (systemplane.TestEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range m.entries {
		if e.ns == namespace && e.key == key && e.tenantID == tenantID {
			return systemplane.TestEntry{
				Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
				Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
			}, true, nil
		}
	}

	return systemplane.TestEntry{}, false, nil
}

func (m *memStore) SetTenantValue(_ context.Context, tenantID string, e systemplane.TestEntry) error {
	m.mu.Lock()

	e.TenantID = tenantID
	updated := false
	for i, existing := range m.entries {
		if existing.ns == e.Namespace && existing.key == e.Key && existing.tenantID == tenantID {
			m.entries[i] = memEntry{
				ns: e.Namespace, key: e.Key, tenantID: tenantID,
				value: e.Value, updatedAt: e.UpdatedAt, updatedBy: e.UpdatedBy,
			}
			updated = true
			break
		}
	}

	if !updated {
		m.entries = append(m.entries, memEntry{
			ns: e.Namespace, key: e.Key, tenantID: tenantID,
			value: e.Value, updatedAt: e.UpdatedAt, updatedBy: e.UpdatedBy,
		})
	}

	handlers := make([]func(systemplane.TestEvent), 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	m.mu.Unlock()

	evt := systemplane.TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: tenantID}
	for _, h := range handlers {
		h(evt)
	}

	return nil
}

func (m *memStore) DeleteTenantValue(_ context.Context, tenantID, namespace, key, _ string) error {
	m.mu.Lock()
	deleted := false

	for i, e := range m.entries {
		if e.ns == namespace && e.key == key && e.tenantID == tenantID {
			m.entries = append(m.entries[:i], m.entries[i+1:]...)
			deleted = true

			break
		}
	}

	var handlers []func(systemplane.TestEvent)
	if deleted {
		handlers = make([]func(systemplane.TestEvent), 0, len(m.handlers))
		for _, h := range m.handlers {
			handlers = append(handlers, h)
		}
	}
	m.mu.Unlock()

	if deleted {
		evt := systemplane.TestEvent{Namespace: namespace, Key: key, TenantID: tenantID}
		for _, h := range handlers {
			h(evt)
		}
	}

	return nil
}

func (m *memStore) ListTenantOverrides(_ context.Context, afterNS, afterKey, afterTID string, limit int) ([]systemplane.TestEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var all []systemplane.TestEntry

	for _, e := range m.entries {
		if e.tenantID == "_global" {
			continue
		}

		all = append(all, systemplane.TestEntry{
			Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
			Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
		})
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Namespace != all[j].Namespace {
			return all[i].Namespace < all[j].Namespace
		}
		if all[i].Key != all[j].Key {
			return all[i].Key < all[j].Key
		}
		return all[i].TenantID < all[j].TenantID
	})

	var result []systemplane.TestEntry
	for _, e := range all {
		if afterNS != "" || afterKey != "" || afterTID != "" {
			if e.Namespace < afterNS || (e.Namespace == afterNS && e.Key < afterKey) ||
				(e.Namespace == afterNS && e.Key == afterKey && e.TenantID <= afterTID) {
				continue
			}
		}

		result = append(result, e)
		if limit > 0 && len(result) >= limit {
			break
		}
	}

	return result, nil
}

func (m *memStore) ListTenantsForKey(_ context.Context, namespace, key string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[string]struct{})
	var tenants []string

	for _, e := range m.entries {
		if e.ns == namespace && e.key == key && e.tenantID != "_global" && e.tenantID != "" {
			if _, already := seen[e.tenantID]; !already {
				seen[e.tenantID] = struct{}{}
				tenants = append(tenants, e.tenantID)
			}
		}
	}

	sort.Strings(tenants)

	return tenants, nil
}

// memClientFactory creates a systemplane.Client backed by an in-memory store.
// The client is NOT started - the contract suite calls Register then Start.
func memClientFactory(t *testing.T) *systemplane.Client {
	t.Helper()

	s := newMemStore()
	c, err := systemplane.NewForTesting(s)
	if err != nil {
		t.Fatalf("NewForTesting: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c
}

// TestRun_WithMemStore exercises as many subtests as possible using an in-memory store.
func TestRun_WithMemStore(t *testing.T) {
	// Run the full contract suite. Use 50ms settle time to allow Subscribe
	// goroutines to register before writes fire events.
	systemplanetest.Run(t, memClientFactory,
		// Skip tenant event tests (require per-tenant changefeed routing)
		systemplanetest.SkipSubtest("TenantOnChangeReceivesSet"),
		systemplanetest.SkipSubtest("TenantOnChangeReceivesDelete"),
		systemplanetest.SkipSubtest("TenantDeleteNoOpEmitsNoEvent"),
		// Skip async event tests that require reliable changefeed delivery
		systemplanetest.SkipSubtest("OnChangeReceivesSet"),
		systemplanetest.SkipSubtest("MultipleSubscribers"),
		systemplanetest.SkipSubtest("DebounceDeliversLatestValue"),
		systemplanetest.SkipSubtest("PanicSafeSubscriberCallbacks"),
		// NilReceiverSafety already covered separately
		systemplanetest.SkipSubtest("NilReceiverSafety"),
		// Use settle time to allow Subscribe goroutines to register before Set fires
		systemplanetest.WithEventSettle(50*time.Millisecond),
	)
}

// TestRunClient_WithMemStore covers the RunClient alias.
func TestRunClient_WithMemStore(t *testing.T) {
	systemplanetest.RunClient(t, memClientFactory,
		systemplanetest.SkipSubtest("OnChangeReceivesSet"),
		systemplanetest.SkipSubtest("TenantOnChangeReceivesSet"),
		systemplanetest.SkipSubtest("TenantOnChangeReceivesDelete"),
		systemplanetest.SkipSubtest("TenantDeleteNoOpEmitsNoEvent"),
		systemplanetest.SkipSubtest("MultipleSubscribers"),
		systemplanetest.SkipSubtest("DebounceDeliversLatestValue"),
		systemplanetest.SkipSubtest("PanicSafeSubscriberCallbacks"),
		systemplanetest.SkipSubtest("NilReceiverSafety"),
		systemplanetest.WithEventSettle(50*time.Millisecond),
	)
}
