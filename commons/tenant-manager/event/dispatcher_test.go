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

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/tenantcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testServiceName is the service name used across dispatcher tests.
const testServiceName = "test-service"

// mustMarshalPayload marshals a value to json.RawMessage, failing the test on error.
func mustMarshalPayload(t *testing.T, v any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err, "failed to marshal payload")

	return data
}

// newTestDispatcher creates a minimal EventDispatcher for tests with the given options.
func newTestDispatcher(t *testing.T, opts ...DispatcherOption) *EventDispatcher {
	t.Helper()

	cache := tenantcache.NewTenantCache()

	// Create a test API server that returns empty responses
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
			t.Errorf("failed to encode empty response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	loader := newTestLoader(t, cache, server.URL)

	allOpts := []DispatcherOption{
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1 * time.Hour),
	}
	allOpts = append(allOpts, opts...)

	return NewEventDispatcher(cache, loader, testServiceName, allOpts...)
}

// newTestPMClient creates a client.Client pointing at the test server.
func newTestPMClient(t *testing.T, serverURL string) *client.Client {
	t.Helper()

	c, err := client.NewClient(serverURL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err, "failed to create test client")

	return c
}

// newTestLoader creates a TenantLoader backed by the given test server.
func newTestLoader(t *testing.T, cache *tenantcache.TenantCache, serverURL string) *tenantcache.TenantLoader {
	t.Helper()

	return tenantcache.NewTenantLoader(
		newTestPMClient(t, serverURL),
		cache,
		testServiceName,
		1*time.Hour,
		testutil.NewMockLogger(),
	)
}

// seedTestCache adds a tenant config to the cache for tests that require a pre-existing tenant.
func seedTestCache(cache *tenantcache.TenantCache, tenantID string) {
	config := &core.TenantConfig{
		ID:         tenantID,
		TenantSlug: tenantID + "-slug",
		Service:    testServiceName,
		Status:     "active",
	}

	cache.Set(tenantID, config, 1*time.Hour)
}

func TestEventDispatcher_HandleEvent_TenantCreated_NoOp(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)
	ctx := context.Background()

	evt := TenantLifecycleEvent{
		EventID:   "evt-001",
		EventType: EventTenantCreated,
		TenantID:  "tenant-created-001",
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "tenant.created should be a no-op")

	// Verify tenant was NOT added to cache
	_, ok := d.cache.Get("tenant-created-001")
	assert.False(t, ok, "tenant.created should not add tenant to cache")
}

func TestEventDispatcher_HandleEvent_TenantSuspended_RemovesTenant(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	seedTestCache(cache, "tenant-susp-001")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
			t.Errorf("failed to encode empty response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	loader := newTestLoader(t, cache, server.URL)

	d := NewEventDispatcher(cache, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	ctx := context.Background()

	evt := TenantLifecycleEvent{
		EventID:   "evt-002",
		EventType: EventTenantSuspended,
		TenantID:  "tenant-susp-001",
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "tenant.suspended should not return error")

	// Verify tenant removed from cache
	_, ok := cache.Get("tenant-susp-001")
	assert.False(t, ok, "tenant.suspended should remove tenant from cache")
}

func TestEventDispatcher_HandleEvent_ServiceAssociated_MatchingService(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)
	ctx := context.Background()

	payload := ServiceAssociatedPayload{
		ServiceName:   testServiceName,
		IsolationMode: "shared",
		Modules:       []string{"transaction", "onboarding"},
		SecretPaths: map[string]map[string]string{
			"transaction": {"postgresql_rw": "path/to/secret/tx-rw", "postgresql_ro": "path/to/secret/tx-ro"},
			"onboarding":  {"postgresql_rw": "path/to/secret/onb-rw"},
		},
		MessagingConfig: &MessagingEventConfig{
			RabbitMQSecretPath: "path/to/rabbitmq/secret",
		},
		ConnectionSettings: &ConnectionSettingsPayload{
			MaxOpenConns:     10,
			MaxIdleConns:     5,
			StatementTimeout: "30s",
		},
	}

	evt := TenantLifecycleEvent{
		EventID:    "evt-007",
		EventType:  EventTenantServiceAssociated,
		TenantID:   "tenant-assoc-001",
		TenantSlug: "acme",
		Payload:    mustMarshalPayload(t, payload),
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "tenant.service.associated should not return error")

	// Verify tenant is in cache
	entry, ok := d.cache.Get("tenant-assoc-001")
	assert.True(t, ok, "tenant.service.associated should add tenant to cache")
	require.NotNil(t, entry, "cache entry should not be nil")

	// Verify config fields
	cfg := entry.Config
	require.NotNil(t, cfg, "tenant config should not be nil")
	assert.Equal(t, "shared", cfg.IsolationMode, "isolation mode should match payload")
	require.NotNil(t, cfg.ConnectionSettings, "connection settings should be populated")
	assert.Equal(t, 10, cfg.ConnectionSettings.MaxOpenConns)
	assert.Equal(t, 5, cfg.ConnectionSettings.MaxIdleConns)
	assert.Equal(t, "30s", cfg.ConnectionSettings.StatementTimeout)
	require.NotNil(t, cfg.Databases, "databases map should be populated from secret_paths")
	require.Len(t, cfg.Databases, 2, "should have two database entries")
	assert.NotNil(t, cfg.Databases["transaction"].PostgreSQL, "transaction should have postgresql config")
	assert.NotNil(t, cfg.Databases["transaction"].PostgreSQLReplica, "transaction should have replica config")
	assert.NotNil(t, cfg.Databases["onboarding"].PostgreSQL, "onboarding should have postgresql config")
	require.NotNil(t, cfg.Messaging, "messaging config should be populated")
	require.NotNil(t, cfg.Messaging.RabbitMQ, "rabbitmq config should be populated")
	assert.Equal(t, "path/to/rabbitmq/secret", cfg.Messaging.RabbitMQ.Host)
}

func TestEventDispatcher_HandleEvent_ServiceAssociated_DifferentService_Skipped(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)
	ctx := context.Background()

	payload := ServiceAssociatedPayload{
		ServiceName:   "other-service",
		IsolationMode: "shared",
	}

	evt := TenantLifecycleEvent{
		EventID:   "evt-008",
		EventType: EventTenantServiceAssociated,
		TenantID:  "tenant-assoc-other",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "service.associated for different service should be no-op")

	// Verify tenant was NOT added to cache
	_, ok := d.cache.Get("tenant-assoc-other")
	assert.False(t, ok, "different service event should not add tenant to cache")
}

func TestEventDispatcher_HandleEvent_CredentialsRotated_EagerReload(t *testing.T) {
	t.Parallel()

	// Set up an API server that returns a valid TenantConfig on the /connections endpoint.
	newConfig := &core.TenantConfig{
		ID:         "tenant-cred-001",
		TenantSlug: "acme-rotated",
		Service:    testServiceName,
		Status:     "active",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/connections") {
			if err := json.NewEncoder(w).Encode(newConfig); err != nil {
				t.Errorf("failed to encode tenant config response: %v", err)
			}

			return
		}

		if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
			t.Errorf("failed to encode empty response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	cache := tenantcache.NewTenantCache()
	seedTestCache(cache, "tenant-cred-001")

	loader := newTestLoader(t, cache, server.URL)

	d := NewEventDispatcher(cache, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	ctx := context.Background()

	payload := CredentialsRotatedPayload{
		ServiceName:    testServiceName,
		CredentialType: "database",
		NewSecretPath:  "/secrets/new-path",
	}

	evt := TenantLifecycleEvent{
		EventID:   "evt-cred",
		EventType: EventTenantCredentialsRotated,
		TenantID:  "tenant-cred-001",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "tenant.credentials.rotated should not return error")

	// Verify tenant is back in cache (eager reload)
	entry, ok := cache.Get("tenant-cred-001")
	assert.True(t, ok, "tenant.credentials.rotated should eagerly reload tenant into cache")
	require.NotNil(t, entry, "cache entry should not be nil after eager reload")
	assert.Equal(t, "acme-rotated", entry.Config.TenantSlug, "cache should contain the reloaded config")
}

func TestEventDispatcher_HandleEvent_ConnectionsUpdated(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	seedTestCache(cache, "tenant-conn-001")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
			t.Errorf("failed to encode empty response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	loader := newTestLoader(t, cache, server.URL)

	d := NewEventDispatcher(cache, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
	)

	ctx := context.Background()

	payload := ConnectionsUpdatedPayload{
		ServiceName:  testServiceName,
		Module:       "onboarding",
		MaxOpenConns: 50,
		MaxIdleConns: 10,
	}

	evt := TenantLifecycleEvent{
		EventID:   "evt-013",
		EventType: EventTenantConnectionsUpdated,
		TenantID:  "tenant-conn-001",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "tenant.connections.updated should not return error")
}

func TestEventDispatcher_HandleEvent_UnknownEvent(t *testing.T) {
	t.Parallel()

	capLogger := testutil.NewCapturingLogger()

	d := newTestDispatcher(t, WithDispatcherLogger(capLogger))
	ctx := context.Background()

	evt := TenantLifecycleEvent{
		EventID:   "evt-014",
		EventType: "tenant.unknown.action",
		TenantID:  "tenant-unknown-001",
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "unknown event type should not return error")

	// Verify warning was logged
	assert.True(t, capLogger.ContainsSubstring("unknown event type"),
		"should log warning for unknown event type")
}

func TestEventDispatcher_OnTenantAdded_Callback(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex

	var addedTenantID string

	d := newTestDispatcher(t,
		WithOnTenantAdded(func(_ context.Context, tenantID string) {
			mu.Lock()
			addedTenantID = tenantID
			mu.Unlock()
		}),
	)

	ctx := context.Background()

	payload := ServiceAssociatedPayload{
		ServiceName:   testServiceName,
		IsolationMode: "shared",
	}

	evt := TenantLifecycleEvent{
		EventID:    "evt-cb-add",
		EventType:  EventTenantServiceAssociated,
		TenantID:   "tenant-callback-001",
		TenantSlug: "acme",
		Payload:    mustMarshalPayload(t, payload),
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "HandleEvent should not return error")

	mu.Lock()
	result := addedTenantID
	mu.Unlock()

	assert.Equal(t, "tenant-callback-001", result,
		"onTenantAdded callback should be invoked with the correct tenant ID")
}

func TestEventDispatcher_OnTenantRemoved_Callback(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex

	var removedTenantID string

	cache := tenantcache.NewTenantCache()
	seedTestCache(cache, "tenant-callback-002")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
			t.Errorf("failed to encode empty response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	loader := newTestLoader(t, cache, server.URL)

	d := NewEventDispatcher(cache, loader, testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
		WithCacheTTL(1*time.Hour),
		WithOnTenantRemoved(func(_ context.Context, tenantID string) {
			mu.Lock()
			removedTenantID = tenantID
			mu.Unlock()
		}),
	)

	ctx := context.Background()

	evt := TenantLifecycleEvent{
		EventID:   "evt-cb-remove",
		EventType: EventTenantSuspended,
		TenantID:  "tenant-callback-002",
	}

	err := d.HandleEvent(ctx, evt)
	require.NoError(t, err, "HandleEvent should not return error")

	mu.Lock()
	result := removedTenantID
	mu.Unlock()

	assert.Equal(t, "tenant-callback-002", result,
		"onTenantRemoved callback should be invoked with the correct tenant ID")
}
