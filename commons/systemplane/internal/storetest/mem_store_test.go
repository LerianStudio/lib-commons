//go:build unit

package storetest

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// memStoreEntry is an in-memory entry.
type memStoreEntry struct {
	ns, key, tenantID, updatedBy string
	value                        []byte
	updatedAt                    time.Time
}

// inMemStore is a minimal in-memory implementation of store.Store for storetest.
type inMemStore struct {
	mu            sync.Mutex
	entries       []memStoreEntry
	handlers      map[int]func(store.Event)
	nextHandlerID int
	closed        bool
}

func newInMemStore() *inMemStore {
	return &inMemStore{handlers: make(map[int]func(store.Event))}
}

func (m *inMemStore) List(_ context.Context) ([]store.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []store.Entry

	for _, e := range m.entries {
		out = append(out, store.Entry{
			Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
			Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
		})
	}

	return out, nil
}

func (m *inMemStore) Get(_ context.Context, namespace, key string) (store.Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range m.entries {
		if e.ns == namespace && e.key == key && e.tenantID == store.SentinelGlobal {
			return store.Entry{
				Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
				Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
			}, true, nil
		}
	}

	return store.Entry{}, false, nil
}

func (m *inMemStore) Set(_ context.Context, e store.Entry) error {
	m.mu.Lock()

	e.TenantID = store.SentinelGlobal
	updated := false

	for i, existing := range m.entries {
		if existing.ns == e.Namespace && existing.key == e.Key && existing.tenantID == store.SentinelGlobal {
			m.entries[i] = memStoreEntry{
				ns: e.Namespace, key: e.Key, tenantID: store.SentinelGlobal,
				value: e.Value, updatedAt: e.UpdatedAt, updatedBy: e.UpdatedBy,
			}
			updated = true

			break
		}
	}

	if !updated {
		m.entries = append(m.entries, memStoreEntry{
			ns: e.Namespace, key: e.Key, tenantID: store.SentinelGlobal,
			value: e.Value, updatedAt: e.UpdatedAt, updatedBy: e.UpdatedBy,
		})
	}

	handlers := make([]func(store.Event), 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	m.mu.Unlock()

	evt := store.Event{Namespace: e.Namespace, Key: e.Key, TenantID: store.SentinelGlobal}
	for _, h := range handlers {
		h(evt)
	}

	return nil
}

func (m *inMemStore) Subscribe(ctx context.Context, handler func(store.Event)) error {
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

func (m *inMemStore) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	return nil
}

func (m *inMemStore) GetTenantValue(_ context.Context, tenantID, namespace, key string) (store.Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range m.entries {
		if e.ns == namespace && e.key == key && e.tenantID == tenantID {
			return store.Entry{
				Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
				Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
			}, true, nil
		}
	}

	return store.Entry{}, false, nil
}

func (m *inMemStore) SetTenantValue(_ context.Context, tenantID string, e store.Entry) error {
	m.mu.Lock()

	updated := false

	for i, existing := range m.entries {
		if existing.ns == e.Namespace && existing.key == e.Key && existing.tenantID == tenantID {
			m.entries[i] = memStoreEntry{
				ns: e.Namespace, key: e.Key, tenantID: tenantID,
				value: e.Value, updatedAt: e.UpdatedAt, updatedBy: e.UpdatedBy,
			}
			updated = true

			break
		}
	}

	if !updated {
		m.entries = append(m.entries, memStoreEntry{
			ns: e.Namespace, key: e.Key, tenantID: tenantID,
			value: e.Value, updatedAt: e.UpdatedAt, updatedBy: e.UpdatedBy,
		})
	}

	handlers := make([]func(store.Event), 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	m.mu.Unlock()

	evt := store.Event{Namespace: e.Namespace, Key: e.Key, TenantID: tenantID}
	for _, h := range handlers {
		h(evt)
	}

	return nil
}

func (m *inMemStore) DeleteTenantValue(_ context.Context, tenantID, namespace, key, _ string) error {
	m.mu.Lock()

	deleted := false

	for i, e := range m.entries {
		if e.ns == namespace && e.key == key && e.tenantID == tenantID {
			m.entries = append(m.entries[:i], m.entries[i+1:]...)
			deleted = true

			break
		}
	}

	handlers := make([]func(store.Event), 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	m.mu.Unlock()

	// Only fire event if an entry was actually deleted
	if deleted {
		evt := store.Event{Namespace: namespace, Key: key, TenantID: tenantID}
		for _, h := range handlers {
			h(evt)
		}
	}

	return nil
}

func (m *inMemStore) ListTenantOverrides(_ context.Context, _, _, _ string, limit int) ([]store.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []store.Entry

	for _, e := range m.entries {
		if e.tenantID != store.SentinelGlobal && e.tenantID != "" {
			result = append(result, store.Entry{
				Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
				Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
			})

			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}

	return result, nil
}

func (m *inMemStore) ListTenantsForKey(_ context.Context, namespace, key string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[string]struct{})
	var tenants []string

	for _, e := range m.entries {
		if e.ns == namespace && e.key == key && e.tenantID != store.SentinelGlobal && e.tenantID != "" {
			if _, already := seen[e.tenantID]; !already {
				seen[e.tenantID] = struct{}{}
				tenants = append(tenants, e.tenantID)
			}
		}
	}

	sort.Strings(tenants)

	if tenants == nil {
		tenants = []string{}
	}

	return tenants, nil
}

// ListTenantValues returns all entries (global + tenant overrides).
func (m *inMemStore) ListTenantValues(_ context.Context) ([]store.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []store.Entry

	for _, e := range m.entries {
		out = append(out, store.Entry{
			Namespace: e.ns, Key: e.key, TenantID: e.tenantID,
			Value: e.value, UpdatedAt: e.updatedAt, UpdatedBy: e.updatedBy,
		})
	}

	return out, nil
}

// memStoreFactory creates a fresh in-memory store for each test.
func memStoreFactory(t *testing.T) store.Store {
	t.Helper()

	return newInMemStore()
}

// TestRunTenantContracts_WithMemStore exercises the internal store contract suite
// using an in-memory store implementation.
func TestRunTenantContracts_WithMemStore(t *testing.T) {
	RunTenantContracts(t, memStoreFactory,
		// Use a short settle time to allow subscriber goroutines to register
		WithEventSettle(10*time.Millisecond),
	)
}
