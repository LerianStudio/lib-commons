//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/cache"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/tenantcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spyConfigCache is a cache.ConfigCache test double that records Del calls.
type spyConfigCache struct {
	mu      sync.Mutex
	store   map[string]string
	delKeys []string
}

func newSpyConfigCache() *spyConfigCache {
	return &spyConfigCache{store: make(map[string]string)}
}

func (s *spyConfigCache) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.store[key]
	if !ok {
		return "", cache.ErrCacheMiss
	}

	return v, nil
}

func (s *spyConfigCache) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.store[key] = value

	return nil
}

func (s *spyConfigCache) Del(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.delKeys = append(s.delKeys, key)
	delete(s.store, key)

	return nil
}

func (s *spyConfigCache) delCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0

	for _, k := range s.delKeys {
		if k == key {
			n++
		}
	}

	return n
}

func tier2Key(tenantID string) string {
	return "tenant-connections:" + tenantID + ":" + testServiceName
}

// newSpyLoader builds a TenantLoader whose pmClient is backed by the spy cache
// and points at the given test server. The spy is seeded with the tenant's
// tier-2 entry so Del has something to remove and record.
func newSpyLoader(t *testing.T, cacheStore *tenantcache.TenantCache, serverURL, tenantID string, spy *spyConfigCache) *tenantcache.TenantLoader {
	t.Helper()

	pmClient, err := client.NewClient(serverURL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
		client.WithCache(spy),
	)
	require.NoError(t, err, "spy-backed client should be created")

	t.Cleanup(func() { _ = pmClient.Close() })

	require.NoError(t, spy.Set(context.Background(), tier2Key(tenantID), "{}", time.Hour),
		"seed tier-2 entry")

	return tenantcache.NewTenantLoader(pmClient, cacheStore, testServiceName, 1*time.Hour, testutil.NewMockLogger())
}

// emptyConnectionsServer returns a server that serves an empty list for non
// /connections paths (i.e. reload returns no config, leaving tier-1 empty).
func emptyConnectionsServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
			t.Errorf("failed to encode empty response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

// connectionsServer returns a server that serves the given config on
// /connections and an empty list elsewhere.
func connectionsServer(t *testing.T, cfg *core.TenantConfig) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/connections") {
			if err := json.NewEncoder(w).Encode(cfg); err != nil {
				t.Errorf("failed to encode config response: %v", err)
			}

			return
		}

		if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
			t.Errorf("failed to encode empty response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

// T1.4 — removeTenant must also clear tier-2.
func TestRemoveTenant_ClearsTier2(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-removetier2-001"

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID)

	server := emptyConnectionsServer(t)
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	d.RemoveTenant(context.Background(), tenantID)

	// Tier-1 evicted.
	_, ok := cacheStore.Get(tenantID)
	assert.False(t, ok, "removeTenant should evict tier-1")

	// Tier-2 evicted via loader.InvalidateClientCache.
	assert.Equal(t, 1, spy.delCount(tier2Key(tenantID)),
		"removeTenant should invalidate the client (tier-2) cache exactly once")
}

// T1.5(a) — owned tenant + matching service: evict both tiers then eager reload.
func TestHandleCacheInvalidate_Owned_EvictsAndReloads(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-owned-001"

	reloaded := &core.TenantConfig{
		ID:         tenantID,
		TenantSlug: "acme-reloaded",
		Service:    testServiceName,
		Status:     "active",
	}

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID) // owned: present in cache

	server := connectionsServer(t, reloaded)
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-owned",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: testServiceName, Reason: "manual"}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	// Tier-1 reloaded with fresh config.
	entry, ok := cacheStore.Get(tenantID)
	require.True(t, ok, "owned tenant should be eagerly reloaded into tier-1")
	require.NotNil(t, entry)
	assert.Equal(t, "acme-reloaded", entry.Config.TenantSlug, "tier-1 should hold the reloaded config")

	// Tier-2 was evicted.
	assert.Equal(t, 1, spy.delCount(tier2Key(tenantID)), "tier-2 should be invalidated on cache.invalidate")
}

// T1.5(b) — service mismatch: no-op (filtered by HandleEvent service gate).
func TestHandleCacheInvalidate_ServiceMismatch_NoOp(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-mismatch-001"

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID)

	server := connectionsServer(t, &core.TenantConfig{ID: tenantID, Service: testServiceName})
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-mismatch",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: "other-service"}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	// Tier-1 untouched (still seeded).
	_, ok := cacheStore.Get(tenantID)
	assert.True(t, ok, "service mismatch must not evict tier-1")

	// No tier-2 Del.
	assert.Equal(t, 0, spy.delCount(tier2Key(tenantID)), "service mismatch must not invalidate tier-2")
}

// T1.5(c) — not owned + matching service: evict tier-2 but do NOT reload.
func TestHandleCacheInvalidate_NotOwned_EvictsOnly(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-notowned-001"

	// NOT seeded into cache and no ownership checker => isOwnedLocally false.
	cacheStore := tenantcache.NewTenantCache()

	// Server WOULD serve a config on /connections if reload ran; assert it does not.
	server := connectionsServer(t, &core.TenantConfig{
		ID: tenantID, TenantSlug: "should-not-load", Service: testServiceName, Status: "active",
	})
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-notowned",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: testServiceName}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	// Tier-2 evicted.
	assert.Equal(t, 1, spy.delCount(tier2Key(tenantID)), "not-owned still evicts tier-2")

	// No reload: tier-1 stays empty.
	_, ok := cacheStore.Get(tenantID)
	assert.False(t, ok, "not-owned tenant must NOT be reloaded into tier-1")
}

// T1.6 — cache.invalidate is service-scoped: an unknown service_name is filtered
// out by HandleEvent before reaching the handler (no tier-1/tier-2 effects).
func TestHandleEvent_CacheInvalidate_UnknownService_Filtered(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-filtered-001"

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID)

	server := connectionsServer(t, &core.TenantConfig{ID: tenantID, Service: testServiceName})
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-filtered",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: "some-unrelated-service"}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	_, ok := cacheStore.Get(tenantID)
	assert.True(t, ok, "filtered cache.invalidate must not evict tier-1")
	assert.Equal(t, 0, spy.delCount(tier2Key(tenantID)), "filtered cache.invalidate must not invalidate tier-2")
}

// T1.6 — isServiceScopedEvent must classify cache.invalidate as service-scoped.
func TestIsServiceScopedEvent_CacheInvalidate(t *testing.T) {
	t.Parallel()

	assert.True(t, isServiceScopedEvent(EventTenantCacheInvalidate),
		"tenant.cache.invalidate must be service-scoped so it is filtered by service_name")
}

// T1.7 — regression: tenant.credentials.rotated now also clears tier-2 (F3 fix).
// This encodes the T1.4 fix; it would FAIL against pre-T1.4 code where
// removeTenant only cleared tier-1.
func TestHandleCredentialsRotated_ClearsTier2(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-credtier2-001"

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID)

	server := connectionsServer(t, &core.TenantConfig{
		ID: tenantID, TenantSlug: "rotated", Service: testServiceName, Status: "active",
	})
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-credtier2",
		EventType: EventTenantCredentialsRotated,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CredentialsRotatedPayload{ServiceName: testServiceName}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	assert.Equal(t, 1, spy.delCount(tier2Key(tenantID)),
		"tenant.credentials.rotated must invalidate tier-2 (no stale entry)")
}

// T1.7 — regression: tenant.service.disassociated now also clears tier-2 (F3 fix).
func TestHandleServiceDisassociated_ClearsTier2(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-disassoctier2-001"

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID)

	server := emptyConnectionsServer(t)
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-disassoctier2",
		EventType: EventTenantServiceDisassociated,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, ServiceDisassociatedPayload{ServiceName: testServiceName}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	assert.Equal(t, 1, spy.delCount(tier2Key(tenantID)),
		"tenant.service.disassociated must invalidate tier-2 (no stale entry)")
}

// T1.8 — race-detector test: concurrent cache.invalidate handling alongside
// cache.Get and loader.LoadTenant on the same tenant. Uses the not-owned path
// (tenant absent, no ownership checker) so reload+jitter are skipped, keeping
// the test fast while still exercising removeTenant + tier-2 Del + concurrent
// reads. Must pass under -race. No t.Parallel() to avoid interfering with the
// contention being exercised.
func TestHandleCacheInvalidate_Concurrent_Race(t *testing.T) { //nolint:tparallel // contention test; no t.Parallel by design
	tenantID := "tenant-cacheinv-race-001"

	// Not seeded => not owned => no reload/jitter.
	cacheStore := tenantcache.NewTenantCache()

	server := emptyConnectionsServer(t)
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-race",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: testServiceName}),
	}

	const n = 8

	ctx := context.Background()

	var wg sync.WaitGroup

	wg.Add(n * 3)

	for range n {
		go func() {
			defer wg.Done()

			_ = d.HandleEvent(ctx, evt)
		}()

		go func() {
			defer wg.Done()

			_, _ = cacheStore.Get(tenantID)
		}()

		go func() {
			defer wg.Done()

			_, _ = loader.LoadTenant(ctx, tenantID)
		}()
	}

	wg.Wait()

	// Sanity: no panic, dispatcher still usable.
	require.NoError(t, d.HandleEvent(ctx, evt), "dispatcher should remain healthy after concurrent load")
}

// owned tenant but nil loader: evict only, log warn, no panic, no reload.
func TestHandleCacheInvalidate_Owned_NilLoader_EvictOnly(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-nilloader-001"

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID) // owned

	// loader nil: events still work for eviction; no eager reload possible.
	d := NewEventDispatcher(cacheStore, nil, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-nilloader",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: testServiceName}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt), "nil loader must be non-fatal")

	_, ok := cacheStore.Get(tenantID)
	assert.False(t, ok, "tier-1 should be evicted even with nil loader")
}

// owned tenant where eager reload fails (server 500): non-fatal, tier-1 stays empty.
func TestHandleCacheInvalidate_Owned_ReloadFails_NonFatal(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-reloadfail-001"

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID) // owned

	// Server returns 500 on /connections so LoadTenant errors.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/connections") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*core.TenantConfig{})
	}))
	t.Cleanup(server.Close)

	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-reloadfail",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: testServiceName}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt), "reload failure must be non-fatal")

	// Tier-2 still evicted.
	assert.Equal(t, 1, spy.delCount(tier2Key(tenantID)), "tier-2 should be evicted before reload attempt")

	// Reload failed: tier-1 stays empty.
	_, ok := cacheStore.Get(tenantID)
	assert.False(t, ok, "failed reload must leave tier-1 empty")
}

// M2 (PRIMARY) — owned cache.invalidate reload must invoke the onTenantAdded
// callback exactly once with the correct tenant ID, so the consumer starts its
// goroutine after the eager reload.
func TestHandleCacheInvalidate_Owned_InvokesOnTenantAdded(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-cb-001"

	reloaded := &core.TenantConfig{
		ID:         tenantID,
		TenantSlug: "acme-cb",
		Service:    testServiceName,
		Status:     "active",
	}

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID) // owned: present in cache

	server := connectionsServer(t, reloaded)
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	var (
		mu        sync.Mutex
		addedID   string
		callCount int
	)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
		WithOnTenantAdded(func(_ context.Context, id string) {
			mu.Lock()
			addedID = id
			callCount++
			mu.Unlock()
		}),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-cb",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: testServiceName, Reason: "manual"}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	mu.Lock()
	gotID := addedID
	gotCount := callCount
	mu.Unlock()

	assert.Equal(t, 1, gotCount, "onTenantAdded must be invoked exactly once on owned reload")
	assert.Equal(t, tenantID, gotID, "onTenantAdded must receive the correct tenant ID")
}

// M2 (low gap a) — ownership resolved via the explicit WithTenantOwnershipChecker,
// NOT the cache fallback. The tenant is absent from cache (cache fallback would
// say not-owned), but the explicit checker reports owned, so the owned path runs:
// tier-1 reloaded and tier-2 evicted.
func TestHandleCacheInvalidate_OwnedViaChecker_EvictsAndReloads(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-checker-001"

	reloaded := &core.TenantConfig{
		ID:         tenantID,
		TenantSlug: "acme-checker",
		Service:    testServiceName,
		Status:     "active",
	}

	// NOT seeded: cache fallback would report not-owned.
	cacheStore := tenantcache.NewTenantCache()

	server := connectionsServer(t, reloaded)
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
		WithTenantOwnershipChecker(func(id string) bool { return id == tenantID }),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-checker",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   mustMarshalPayload(t, CacheInvalidatePayload{ServiceName: testServiceName}),
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	// Owned path ran: tier-1 reloaded with fresh config.
	entry, ok := cacheStore.Get(tenantID)
	require.True(t, ok, "explicit ownership checker should drive the owned reload path")
	require.NotNil(t, entry)
	assert.Equal(t, "acme-checker", entry.Config.TenantSlug, "tier-1 should hold the reloaded config")

	// Tier-2 evicted exactly once.
	assert.Equal(t, 1, spy.delCount(tier2Key(tenantID)),
		"owned-via-checker path must invalidate tier-2")
}

// M2 (low gap b) — an empty payload is filtered by the service gate before the
// handler runs. matchesService treats a zero-length payload as a non-match, so
// no tier-1 eviction and no tier-2 invalidation occur.
func TestHandleCacheInvalidate_EmptyPayload_Filtered(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-cacheinv-emptypayload-001"

	cacheStore := tenantcache.NewTenantCache()
	seedTestCache(cacheStore, tenantID) // owned

	server := connectionsServer(t, &core.TenantConfig{ID: tenantID, Service: testServiceName})
	spy := newSpyConfigCache()
	loader := newSpyLoader(t, cacheStore, server.URL, tenantID, spy)

	d := NewEventDispatcher(cacheStore, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	evt := TenantLifecycleEvent{
		EventID:   "evt-cacheinv-emptypayload",
		EventType: EventTenantCacheInvalidate,
		TenantID:  tenantID,
		Payload:   json.RawMessage(nil), // empty payload => service gate filters it out
	}

	require.NoError(t, d.HandleEvent(context.Background(), evt))

	// Tier-1 still present: handler never ran.
	_, ok := cacheStore.Get(tenantID)
	assert.True(t, ok, "empty-payload cache.invalidate must not evict tier-1")

	// Tier-2 not evicted.
	assert.Equal(t, 0, spy.delCount(tier2Key(tenantID)),
		"empty-payload cache.invalidate must not invalidate tier-2")
}
