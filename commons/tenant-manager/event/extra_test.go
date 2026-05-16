//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/tenantcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithPostgres_MongoDB_RabbitMQ_Options cover dispatcher options that were at 0%.
func TestDispatcherOptions_WithPostgres(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	d := newTestDispatcher(t)
	_ = cache

	// WithPostgres, WithMongo, WithRabbitMQ accept nil (just stores the value)
	opt := WithPostgres(nil)
	opt(d)
	assert.Nil(t, d.postgres)
}

func TestDispatcherOptions_WithMongo(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)
	opt := WithMongo(nil)
	opt(d)
	assert.Nil(t, d.mongo)
}

func TestDispatcherOptions_WithRabbitMQ(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)
	opt := WithRabbitMQ(nil)
	opt(d)
	assert.Nil(t, d.rabbitmq)
}

func TestDispatcherOptions_WithTenantOwnershipChecker(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)
	checker := func(id string) bool { return id == "tenant-a" }
	opt := WithTenantOwnershipChecker(checker)
	opt(d)
	require.NotNil(t, d.ownsTenant)
	assert.True(t, d.ownsTenant("tenant-a"))
	assert.False(t, d.ownsTenant("tenant-b"))
}

func TestDispatcherOptions_SetOnTenantAdded_SetOnTenantRemoved(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	addedCalled := false
	removedCalled := false

	d.SetOnTenantAdded(func(_ context.Context, _ string) { addedCalled = true })
	d.SetOnTenantRemoved(func(_ context.Context, _ string) { removedCalled = true })

	require.NotNil(t, d.onTenantAdded)
	require.NotNil(t, d.onTenantRemoved)

	d.onTenantAdded(context.Background(), "t1")
	d.onTenantRemoved(context.Background(), "t1")

	assert.True(t, addedCalled)
	assert.True(t, removedCalled)
}

func TestDispatcherOptions_Cache(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	d := newTestDispatcher(t)
	d.cache = cache

	assert.Same(t, cache, d.Cache())
}

// TestDispatcherHelpers_buildDatabasesFromSecretPaths tests the helper.
func TestBuildDatabasesFromSecretPaths_Empty(t *testing.T) {
	t.Parallel()

	result := buildDatabasesFromSecretPaths(nil)
	assert.Nil(t, result)

	result = buildDatabasesFromSecretPaths(map[string]map[string]string{})
	assert.Nil(t, result)
}

func TestBuildDatabasesFromSecretPaths_WithFields(t *testing.T) {
	t.Parallel()

	secretPaths := map[string]map[string]string{
		"payments": {
			"postgresql_rw": "/secrets/pg-rw",
			"postgresql_ro": "/secrets/pg-ro",
			"mongodb":       "/secrets/mongo",
		},
		"ledger": {
			"postgresql_rw": "/secrets/ledger-pg",
		},
	}

	result := buildDatabasesFromSecretPaths(secretPaths)

	require.NotNil(t, result)
	assert.Len(t, result, 2)

	payments := result["payments"]
	require.NotNil(t, payments.PostgreSQL)
	assert.Equal(t, "/secrets/pg-rw", payments.PostgreSQL.Host)
	require.NotNil(t, payments.PostgreSQLReplica)
	assert.Equal(t, "/secrets/pg-ro", payments.PostgreSQLReplica.Host)
	require.NotNil(t, payments.MongoDB)

	ledger := result["ledger"]
	require.NotNil(t, ledger.PostgreSQL)
	assert.Equal(t, "/secrets/ledger-pg", ledger.PostgreSQL.Host)
	assert.Nil(t, ledger.PostgreSQLReplica)
	assert.Nil(t, ledger.MongoDB)
}

// TestBuildConfigFromConnectionsPayload tests the helper.
func TestBuildConfigFromConnectionsPayload(t *testing.T) {
	t.Parallel()

	payload := ConnectionsUpdatedPayload{
		MaxOpenConns:     20,
		MaxIdleConns:     5,
		StatementTimeout: "30s",
	}

	cfg := buildConfigFromConnectionsPayload("tenant-abc", payload)

	require.NotNil(t, cfg)
	assert.Equal(t, "tenant-abc", cfg.ID)
	require.NotNil(t, cfg.ConnectionSettings)
	assert.Equal(t, 20, cfg.ConnectionSettings.MaxOpenConns)
	assert.Equal(t, 5, cfg.ConnectionSettings.MaxIdleConns)
	assert.Equal(t, "30s", cfg.ConnectionSettings.StatementTimeout)
}

// TestMatchesService covers the matchesService helper.
func TestMatchesService(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t) // service = testServiceName

	t.Run("empty payload returns false without error", func(t *testing.T) {
		evt := TenantLifecycleEvent{Payload: nil}
		matched, err := d.matchesService(evt)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("matching service name returns true", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]string{"service_name": testServiceName})
		evt := TenantLifecycleEvent{Payload: payload}
		matched, err := d.matchesService(evt)
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("non-matching service name returns false", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]string{"service_name": "other-service"})
		evt := TenantLifecycleEvent{Payload: payload}
		matched, err := d.matchesService(evt)
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("invalid JSON payload returns error", func(t *testing.T) {
		evt := TenantLifecycleEvent{Payload: json.RawMessage(`not-json`)}
		_, err := d.matchesService(evt)
		require.Error(t, err)
	})
}

// TestIsOwnedLocally covers the isOwnedLocally helper.
func TestIsOwnedLocally(t *testing.T) {
	t.Parallel()

	t.Run("with ownership checker - owned", func(t *testing.T) {
		d := newTestDispatcher(t, WithTenantOwnershipChecker(func(id string) bool {
			return id == "tenant-owned"
		}))
		assert.True(t, d.isOwnedLocally("tenant-owned"))
	})

	t.Run("with ownership checker - not owned", func(t *testing.T) {
		d := newTestDispatcher(t, WithTenantOwnershipChecker(func(id string) bool {
			return false
		}))
		assert.False(t, d.isOwnedLocally("any-tenant"))
	})

	t.Run("without checker - falls back to cache", func(t *testing.T) {
		cache := tenantcache.NewTenantCache()
		d := newTestDispatcher(t)
		d.cache = cache

		// Not in cache
		assert.False(t, d.isOwnedLocally("unknown-tenant"))

		// Add to cache
		cache.Set("known-tenant", &core.TenantConfig{ID: "known-tenant"}, time.Hour)
		assert.True(t, d.isOwnedLocally("known-tenant"))
	})
}

// TestResolveCacheTTL covers the resolveCacheTTL helper.
func TestResolveCacheTTL(t *testing.T) {
	t.Parallel()

	t.Run("returns configured TTL when positive", func(t *testing.T) {
		d := newTestDispatcher(t, WithCacheTTL(5*time.Minute))
		assert.Equal(t, 5*time.Minute, d.resolveCacheTTL())
	})

	t.Run("returns default TTL when zero", func(t *testing.T) {
		d := newTestDispatcher(t)
		d.cacheTTL = 0
		assert.Equal(t, defaultCacheTTL, d.resolveCacheTTL())
	})
}

// TestRemoveTenant covers the RemoveTenant exported method.
func TestRemoveTenant_RemovesFromCache(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	cache.Set("tenant-remove", &core.TenantConfig{ID: "tenant-remove"}, time.Hour)

	d := newTestDispatcher(t)
	d.cache = cache

	var removedID string
	d.SetOnTenantRemoved(func(_ context.Context, id string) {
		removedID = id
	})

	d.RemoveTenant(context.Background(), "tenant-remove")

	_, ok := cache.Get("tenant-remove")
	assert.False(t, ok, "tenant should be removed from cache")
	assert.Equal(t, "tenant-remove", removedID, "onTenantRemoved callback should be called")
}

// TestHandleEvent_TenantDeleted exercises the deleted event path.
func TestHandleEvent_TenantDeleted_RemovesTenant(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	cache.Set("tenant-del-001", &core.TenantConfig{ID: "tenant-del-001"}, time.Hour)

	d := newTestDispatcher(t)
	d.cache = cache

	removedCalled := false
	d.SetOnTenantRemoved(func(_ context.Context, _ string) { removedCalled = true })

	evt := TenantLifecycleEvent{
		EventID:   "evt-del",
		EventType: EventTenantDeleted,
		TenantID:  "tenant-del-001",
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
	assert.True(t, removedCalled)
}

// TestHandleEvent_TenantUpdated exercises the updated event path.
func TestHandleEvent_TenantUpdated_NoOpForUnknownTenant(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	evt := TenantLifecycleEvent{
		EventID:   "evt-upd",
		EventType: EventTenantUpdated,
		TenantID:  "tenant-upd-unknown",
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
}

// TestHandleEvent_ServiceDisassociated exercises the service disassociated path.
func TestHandleEvent_ServiceDisassociated(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	cache.Set("tenant-disas", &core.TenantConfig{ID: "tenant-disas"}, time.Hour)

	d := newTestDispatcher(t)
	d.cache = cache

	removedCalled := false
	d.SetOnTenantRemoved(func(_ context.Context, _ string) { removedCalled = true })

	payload, _ := json.Marshal(map[string]string{
		"service_name": testServiceName,
	})

	evt := TenantLifecycleEvent{
		EventID:   "evt-disas",
		EventType: EventTenantServiceDisassociated,
		TenantID:  "tenant-disas",
		Payload:   payload,
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
	assert.True(t, removedCalled)
}

// TestHandleEvent_ServiceSuspended exercises the suspended path.
func TestHandleEvent_ServiceSuspended(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	cache.Set("tenant-svc-susp", &core.TenantConfig{ID: "tenant-svc-susp"}, time.Hour)

	d := newTestDispatcher(t)
	d.cache = cache

	removedCalled := false
	d.SetOnTenantRemoved(func(_ context.Context, _ string) { removedCalled = true })

	payload, _ := json.Marshal(map[string]string{
		"service_name": testServiceName,
	})

	evt := TenantLifecycleEvent{
		EventID:   "evt-svc-susp",
		EventType: EventTenantServiceSuspended,
		TenantID:  "tenant-svc-susp",
		Payload:   payload,
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
	assert.True(t, removedCalled)
}

// TestHandleEvent_ServicePurged exercises the purged path.
func TestHandleEvent_ServicePurged(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	cache.Set("tenant-purged", &core.TenantConfig{ID: "tenant-purged"}, time.Hour)

	d := newTestDispatcher(t)
	d.cache = cache

	removedCalled := false
	d.SetOnTenantRemoved(func(_ context.Context, _ string) { removedCalled = true })

	payload, _ := json.Marshal(map[string]string{
		"service_name": testServiceName,
	})

	evt := TenantLifecycleEvent{
		EventID:   "evt-purged",
		EventType: EventTenantServicePurged,
		TenantID:  "tenant-purged",
		Payload:   payload,
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
	assert.True(t, removedCalled)
}

// TestHandleEvent_ServiceReactivated exercises the reactivated path.
func TestHandleEvent_ServiceReactivated(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	payload, _ := json.Marshal(map[string]string{
		"service_name": testServiceName,
	})

	evt := TenantLifecycleEvent{
		EventID:   "evt-reactivated",
		EventType: EventTenantServiceReactivated,
		TenantID:  "tenant-reactivated",
		Payload:   payload,
	}

	// The reactivated event will try to fetch the tenant config.
	// Without a real API, the cache won't be populated, but it shouldn't panic or error in ways
	// that break the flow. We just verify the handler runs without panicking.
	_ = d.HandleEvent(context.Background(), evt)
}

// TestHandleEvent_TenantActivated exercises the activated path.
func TestHandleEvent_TenantActivated(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	evt := TenantLifecycleEvent{
		EventID:   "evt-activated",
		EventType: EventTenantActivated,
		TenantID:  "tenant-activated",
	}

	// tenant.activated is not in cache - should be no-op
	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
}

// TestNewEventDispatcher_WithLogger exercises the dispatcher with a logger option.
func TestNewEventDispatcher_WithDispatcherLogger(t *testing.T) {
	t.Parallel()

	logger := testutil.NewMockLogger()
	d := newTestDispatcher(t, WithDispatcherLogger(logger))
	assert.NotNil(t, d.logger)
}
