//go:build unit

package systemplane

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// tenantKey uniquely addresses a row in tenantTestStore.
type tenantKey struct {
	tenantID  string
	namespace string
	key       string
}

// tenantTestStore is a minimal map-backed TestStore that supports all five
// tenant methods. It is intentionally single-goroutine-friendly (one mutex
// over everything) because the smoke tests exercise it from a single
// writer.
//
// Comprehensive concurrency tests land in Task 7's contract suite; this
// fake exists only to verify the Client's dispatch + cache + subscriber
// wiring end-to-end.
type tenantTestStore struct {
	mu       sync.Mutex
	rows     map[tenantKey]TestEntry
	handlers []func(TestEvent)
	subReady chan struct{}
}

func newTenantTestStore() *tenantTestStore {
	return &tenantTestStore{
		rows:     make(map[tenantKey]TestEntry),
		subReady: make(chan struct{}),
	}
}

func (s *tenantTestStore) fire(evt TestEvent) {
	s.mu.Lock()
	handlers := make([]func(TestEvent), len(s.handlers))
	copy(handlers, s.handlers)
	s.mu.Unlock()

	for _, h := range handlers {
		h(evt)
	}
}

func (s *tenantTestStore) List(_ context.Context) ([]TestEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]TestEntry, 0, len(s.rows))
	for _, e := range s.rows {
		if e.TenantID == "_global" {
			out = append(out, e)
		}
	}

	return out, nil
}

func (s *tenantTestStore) Get(_ context.Context, namespace, key string) (TestEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.rows[tenantKey{tenantID: "_global", namespace: namespace, key: key}]

	return e, ok, nil
}

func (s *tenantTestStore) Set(_ context.Context, e TestEntry) error {
	e.TenantID = "_global"

	s.mu.Lock()
	s.rows[tenantKey{tenantID: "_global", namespace: e.Namespace, key: e.Key}] = e
	s.mu.Unlock()

	s.fire(TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: "_global"})

	return nil
}

func (s *tenantTestStore) Subscribe(ctx context.Context, handler func(TestEvent)) error {
	s.mu.Lock()
	first := len(s.handlers) == 0
	s.handlers = append(s.handlers, handler)
	s.mu.Unlock()

	if first {
		close(s.subReady)
	}

	<-ctx.Done()

	return nil
}

func (s *tenantTestStore) Close() error { return nil }

func (s *tenantTestStore) GetTenantValue(_ context.Context, tenantID, namespace, key string) (TestEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.rows[tenantKey{tenantID: tenantID, namespace: namespace, key: key}]

	return e, ok, nil
}

func (s *tenantTestStore) SetTenantValue(_ context.Context, tenantID string, e TestEntry) error {
	e.TenantID = tenantID

	s.mu.Lock()
	s.rows[tenantKey{tenantID: tenantID, namespace: e.Namespace, key: e.Key}] = e
	s.mu.Unlock()

	s.fire(TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: tenantID})

	return nil
}

func (s *tenantTestStore) DeleteTenantValue(_ context.Context, tenantID, namespace, key, _ string) error {
	s.mu.Lock()
	delete(s.rows, tenantKey{tenantID: tenantID, namespace: namespace, key: key})
	s.mu.Unlock()

	s.fire(TestEvent{Namespace: namespace, Key: key, TenantID: tenantID})

	return nil
}

func (s *tenantTestStore) ListTenantValues(_ context.Context) ([]TestEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]TestEntry, 0, len(s.rows))
	for _, e := range s.rows {
		out = append(out, e)
	}

	return out, nil
}

func (s *tenantTestStore) ListTenantsForKey(_ context.Context, namespace, key string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})

	for k := range s.rows {
		if k.namespace == namespace && k.key == key && k.tenantID != "_global" {
			seen[k.tenantID] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}

	sort.Strings(out)

	return out, nil
}

// newStartedClient builds a Client with the tenantTestStore, registers the
// given tenant-scoped key, and calls Start. Debouncing is disabled by the
// NewForTesting default, so changefeed echoes reach subscribers
// synchronously.
func newStartedClient(t *testing.T, namespace, key string, defaultValue any) (*Client, *tenantTestStore) {
	t.Helper()

	fs := newTenantTestStore()

	c, err := NewForTesting(fs)
	require.NoError(t, err, "NewForTesting")

	require.NoError(t, c.RegisterTenantScoped(namespace, key, defaultValue), "RegisterTenantScoped")

	require.NoError(t, c.Start(context.Background()), "Start")

	// Wait for Subscribe to register its handler so fired events reach us.
	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register")
	}

	t.Cleanup(func() { _ = c.Close() })

	return c, fs
}

func tenantCtx(tenantID string) context.Context {
	return core.ContextWithTenantID(context.Background(), tenantID)
}

func TestSetForTenant_HappyPath(t *testing.T) {
	t.Parallel()

	c, _ := newStartedClient(t, "global", "fee.rate", 0.0)

	ctx := tenantCtx("tenant-A")

	err := c.SetForTenant(ctx, "global", "fee.rate", 0.05, "admin")
	require.NoError(t, err, "SetForTenant should succeed")

	// GetForTenant must return the override, not the default.
	v, found, err := c.GetForTenant(ctx, "global", "fee.rate")
	require.NoError(t, err, "GetForTenant")
	assert.True(t, found, "found must be true")
	assert.InDelta(t, 0.05, v, 0.0001, "GetForTenant should return the override")
}

func TestGetForTenant_FallsThroughToGlobal(t *testing.T) {
	t.Parallel()

	c, _ := newStartedClient(t, "global", "fee.rate", 0.01)

	// Write to the legacy global (no tenant override).
	require.NoError(t, c.Set(context.Background(), "global", "fee.rate", 0.10, "admin"), "Set")

	// Tenant read with NO override: should see the global value, not the default.
	v, found, err := c.GetForTenant(tenantCtx("tenant-A"), "global", "fee.rate")
	require.NoError(t, err, "GetForTenant")
	assert.True(t, found)
	assert.InDelta(t, 0.10, v, 0.0001, "GetForTenant without an override should fall through to global")
}

func TestDeleteForTenant_RevertsToDefault(t *testing.T) {
	t.Parallel()

	c, _ := newStartedClient(t, "global", "fee.rate", 0.42)

	ctx := tenantCtx("tenant-A")

	require.NoError(t, c.SetForTenant(ctx, "global", "fee.rate", 0.99, "admin"), "SetForTenant")
	require.NoError(t, c.DeleteForTenant(ctx, "global", "fee.rate", "admin"), "DeleteForTenant")

	v, found, err := c.GetForTenant(ctx, "global", "fee.rate")
	require.NoError(t, err, "GetForTenant after delete")
	assert.True(t, found, "found must be true even after delete")
	assert.InDelta(t, 0.42, v, 0.0001, "GetForTenant after delete should return the registered default")
}

func TestListTenantsForKey_SortsAndDedupes(t *testing.T) {
	t.Parallel()

	c, _ := newStartedClient(t, "global", "fee.rate", 0.0)

	require.NoError(t, c.SetForTenant(tenantCtx("tenant-B"), "global", "fee.rate", 0.10, "admin"))
	require.NoError(t, c.SetForTenant(tenantCtx("tenant-A"), "global", "fee.rate", 0.20, "admin"))
	// Double-set for tenant-A — should dedupe.
	require.NoError(t, c.SetForTenant(tenantCtx("tenant-A"), "global", "fee.rate", 0.30, "admin"))

	tenants := c.ListTenantsForKey("global", "fee.rate")
	assert.Equal(t, []string{"tenant-A", "tenant-B"}, tenants, "ListTenantsForKey should be sorted and deduplicated")
}

// TestOnTenantChange_FiresOnTenantWrite_OnChangeStaysSilent is the AC8 pin.
// It asserts that:
//  1. OnTenantChange fires when SetForTenant is called.
//  2. OnChange does NOT fire on tenant writes.
//  3. OnChange still fires on legacy Set (global writes).
func TestOnTenantChange_FiresOnTenantWrite_OnChangeStaysSilent(t *testing.T) {
	t.Parallel()

	c, _ := newStartedClient(t, "global", "fee.rate", 0.0)

	var (
		mu            sync.Mutex
		onTenantFires []struct {
			tenantID string
			value    any
		}
		onChangeFires []any
	)

	onTenantDone := make(chan struct{}, 2)
	onChangeDone := make(chan struct{}, 2)

	unsubTenant := c.OnTenantChange("global", "fee.rate", func(_, _, tenantID string, newValue any) {
		mu.Lock()
		onTenantFires = append(onTenantFires, struct {
			tenantID string
			value    any
		}{tenantID, newValue})
		mu.Unlock()
		onTenantDone <- struct{}{}
	})
	defer unsubTenant()

	unsubGlobal := c.OnChange("global", "fee.rate", func(newValue any) {
		mu.Lock()
		onChangeFires = append(onChangeFires, newValue)
		mu.Unlock()
		onChangeDone <- struct{}{}
	})
	defer unsubGlobal()

	// Tenant write: should fire OnTenantChange, NOT OnChange.
	require.NoError(t, c.SetForTenant(tenantCtx("tenant-A"), "global", "fee.rate", 0.05, "admin"))

	select {
	case <-onTenantDone:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTenantChange did not fire on SetForTenant")
	}

	// Give OnChange a chance to fire — it must NOT. A bounded sleep is
	// acceptable here because we are asserting the negative. A 200ms
	// window well exceeds the synchronous dispatch path.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	require.Len(t, onTenantFires, 1, "OnTenantChange should have fired exactly once for tenant write")
	assert.Equal(t, "tenant-A", onTenantFires[0].tenantID)
	assert.InDelta(t, 0.05, onTenantFires[0].value, 0.0001)
	require.Empty(t, onChangeFires, "OnChange MUST NOT fire on tenant writes (AC8)")
	mu.Unlock()

	// Legacy global write: should fire OnChange, NOT OnTenantChange.
	require.NoError(t, c.Set(context.Background(), "global", "fee.rate", 0.15, "admin"))

	select {
	case <-onChangeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("OnChange did not fire on legacy Set")
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	assert.Len(t, onChangeFires, 1, "OnChange should have fired exactly once for global write")
	assert.InDelta(t, 0.15, onChangeFires[0], 0.0001)
	// OnTenantChange MUST NOT have grown — still just the tenant-A entry.
	require.Len(t, onTenantFires, 1, "OnTenantChange must not fire on global writes")
	mu.Unlock()
}

// TestOnTenantChange_FiresOnDelete asserts that DeleteForTenant causes
// OnTenantChange to fire with the registered default — the tenant reverted
// to global/default per TRD §4.4 / PRD AC9.
func TestOnTenantChange_FiresOnDelete(t *testing.T) {
	t.Parallel()

	c, _ := newStartedClient(t, "global", "fee.rate", 0.42)

	var (
		mu    sync.Mutex
		fires []any
	)

	done := make(chan struct{}, 2)

	unsub := c.OnTenantChange("global", "fee.rate", func(_, _, _ string, newValue any) {
		mu.Lock()
		fires = append(fires, newValue)
		mu.Unlock()
		done <- struct{}{}
	})
	defer unsub()

	ctx := tenantCtx("tenant-A")

	require.NoError(t, c.SetForTenant(ctx, "global", "fee.rate", 0.99, "admin"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTenantChange did not fire on SetForTenant")
	}

	require.NoError(t, c.DeleteForTenant(ctx, "global", "fee.rate", "admin"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTenantChange did not fire on DeleteForTenant")
	}

	mu.Lock()
	require.Len(t, fires, 2)
	assert.InDelta(t, 0.99, fires[0], 0.0001, "first fire is the set value")
	assert.InDelta(t, 0.42, fires[1], 0.0001, "second fire is the registered default (delete reverts to default)")
	mu.Unlock()
}
