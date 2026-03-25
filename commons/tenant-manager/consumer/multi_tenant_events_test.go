// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEventsTestConsumer creates a minimal MultiTenantConsumer for event handler tests.
// It sets up parentCtx and an API server that returns empty tenant lists.
func newEventsTestConsumer(t *testing.T) *MultiTenantConsumer {
	t.Helper()

	server := setupTenantManagerAPIServer(t, nil)
	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

	ctx := context.Background()
	consumer.mu.Lock()
	consumer.parentCtx = ctx
	consumer.mu.Unlock()

	t.Cleanup(func() { consumer.Close() })

	return consumer
}

// seedCacheTenant adds a tenant to the consumer's cache and knownTenants.
func seedCacheTenant(c *MultiTenantConsumer, tenantID string) {
	config := &core.TenantConfig{
		ID:         tenantID,
		TenantSlug: tenantID + "-slug",
		Service:    testServiceName,
		Status:     "active",
	}

	c.cache.Set(tenantID, config, 1*time.Hour)

	c.mu.Lock()
	c.knownTenants[tenantID] = true
	c.mu.Unlock()
}

// mustMarshalPayload marshals a payload to json.RawMessage, failing the test on error.
func mustMarshalPayload(t *testing.T, v any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err, "failed to marshal payload")

	return data
}

func TestHandleLifecycleEvent_TenantCreated_NoOp(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	ctx := context.Background()

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-001",
		EventType: event.EventTenantCreated,
		TenantID:  "tenant-created-001",
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.created should be a no-op")

	// Verify tenant was NOT added to cache (lazy-load on first request)
	_, ok := consumer.cache.Get("tenant-created-001")
	assert.False(t, ok, "tenant.created should not add tenant to cache")
}

func TestHandleLifecycleEvent_TenantSuspended_InCache(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	seedCacheTenant(consumer, "tenant-susp-001")
	ctx := context.Background()

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-002",
		EventType: event.EventTenantSuspended,
		TenantID:  "tenant-susp-001",
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.suspended should not return error")

	// Verify tenant removed from cache
	_, ok := consumer.cache.Get("tenant-susp-001")
	assert.False(t, ok, "tenant.suspended should remove tenant from cache")

	// Verify tenant removed from knownTenants
	consumer.mu.RLock()
	known := consumer.knownTenants["tenant-susp-001"]
	consumer.mu.RUnlock()
	assert.False(t, known, "tenant.suspended should remove tenant from knownTenants")
}

func TestHandleLifecycleEvent_TenantSuspended_NotInCache(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	ctx := context.Background()

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-003",
		EventType: event.EventTenantSuspended,
		TenantID:  "tenant-susp-notcached",
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.suspended for unknown tenant should be no-op")

	// Verify tenant was NOT added to any map
	_, ok := consumer.cache.Get("tenant-susp-notcached")
	assert.False(t, ok, "should not add unknown tenant to cache")
}

func TestHandleLifecycleEvent_TenantDeleted(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	seedCacheTenant(consumer, "tenant-del-001")
	ctx := context.Background()

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-004",
		EventType: event.EventTenantDeleted,
		TenantID:  "tenant-del-001",
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.deleted should not return error")

	// Verify tenant removed from cache
	_, ok := consumer.cache.Get("tenant-del-001")
	assert.False(t, ok, "tenant.deleted should remove tenant from cache")

	// Verify tenant removed from knownTenants
	consumer.mu.RLock()
	known := consumer.knownTenants["tenant-del-001"]
	consumer.mu.RUnlock()
	assert.False(t, known, "tenant.deleted should remove tenant from knownTenants")
}

func TestHandleLifecycleEvent_TenantActivated_InCache(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	seedCacheTenant(consumer, "tenant-act-001")
	ctx := context.Background()

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-005",
		EventType: event.EventTenantActivated,
		TenantID:  "tenant-act-001",
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.activated should not return error")

	// Verify tenant is still in cache (TTL touched)
	entry, ok := consumer.cache.Get("tenant-act-001")
	assert.True(t, ok, "tenant.activated should keep tenant in cache with refreshed TTL")
	assert.NotNil(t, entry, "cache entry should not be nil")
}

func TestHandleLifecycleEvent_TenantUpdated_InCache(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	seedCacheTenant(consumer, "tenant-upd-001")
	ctx := context.Background()

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-006",
		EventType: event.EventTenantUpdated,
		TenantID:  "tenant-upd-001",
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.updated should not return error")

	// Verify tenant is still in cache (TTL touched)
	entry, ok := consumer.cache.Get("tenant-upd-001")
	assert.True(t, ok, "tenant.updated should keep tenant in cache with refreshed TTL")
	assert.NotNil(t, entry, "cache entry should not be nil")
}

func TestHandleLifecycleEvent_ServiceAssociated_MatchingService(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	ctx := context.Background()

	payload := event.ServiceAssociatedPayload{
		ServiceName:   testServiceName,
		IsolationMode: "shared",
		Modules:       []string{"transaction", "onboarding"},
		SecretPaths: map[string]map[string]string{
			"transaction": {"postgresql_rw": "path/to/secret/tx-rw", "postgresql_ro": "path/to/secret/tx-ro"},
			"onboarding":  {"postgresql_rw": "path/to/secret/onb-rw"},
		},
		MessagingConfig: &event.MessagingEventConfig{
			RabbitMQSecretPath: "path/to/rabbitmq/secret",
		},
		ConnectionSettings: &event.ConnectionSettingsPayload{
			MaxOpenConns:     10,
			MaxIdleConns:     5,
			StatementTimeout: "30s",
		},
	}

	evt := event.TenantLifecycleEvent{
		EventID:    "evt-007",
		EventType:  event.EventTenantServiceAssociated,
		TenantID:   "tenant-assoc-001",
		TenantSlug: "acme",
		Payload:    mustMarshalPayload(t, payload),
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.service.associated should not return error")

	// Verify tenant is in cache
	entry, ok := consumer.cache.Get("tenant-assoc-001")
	assert.True(t, ok, "tenant.service.associated should add tenant to cache")
	require.NotNil(t, entry, "cache entry should not be nil")

	// Verify config fields were populated from payload
	cfg := entry.config
	require.NotNil(t, cfg, "tenant config should not be nil")
	assert.Equal(t, "shared", cfg.IsolationMode, "isolation mode should match payload")
	require.NotNil(t, cfg.ConnectionSettings, "connection settings should be populated")
	assert.Equal(t, 10, cfg.ConnectionSettings.MaxOpenConns)
	assert.Equal(t, 5, cfg.ConnectionSettings.MaxIdleConns)
	assert.Equal(t, "30s", cfg.ConnectionSettings.StatementTimeout)
	require.NotNil(t, cfg.Databases, "databases map should be populated from secret_paths")
	require.Len(t, cfg.Databases, 2, "should have two database entries")
	assert.NotNil(t, cfg.Databases["transaction"].PostgreSQL, "transaction should have postgresql config")
	assert.NotNil(t, cfg.Databases["transaction"].PostgreSQLReplica, "transaction should have postgresql replica config")
	assert.NotNil(t, cfg.Databases["onboarding"].PostgreSQL, "onboarding should have postgresql config")
	require.NotNil(t, cfg.Messaging, "messaging config should be populated")
	require.NotNil(t, cfg.Messaging.RabbitMQ, "rabbitmq config should be populated")
	assert.Equal(t, "path/to/rabbitmq/secret", cfg.Messaging.RabbitMQ.Host)

	// Verify tenant marked as known
	consumer.mu.RLock()
	known := consumer.knownTenants["tenant-assoc-001"]
	consumer.mu.RUnlock()
	assert.True(t, known, "tenant.service.associated should mark tenant as known")
}

func TestHandleLifecycleEvent_ServiceAssociated_DifferentService(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	ctx := context.Background()

	payload := event.ServiceAssociatedPayload{
		ServiceName:   "other-service",
		IsolationMode: "shared",
	}

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-008",
		EventType: event.EventTenantServiceAssociated,
		TenantID:  "tenant-assoc-other",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "service.associated for different service should be no-op")

	// Verify tenant was NOT added to cache
	_, ok := consumer.cache.Get("tenant-assoc-other")
	assert.False(t, ok, "different service event should not add tenant to cache")
}

func TestHandleLifecycleEvent_ServiceDisassociated(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	seedCacheTenant(consumer, "tenant-disassoc-001")
	ctx := context.Background()

	payload := event.ServiceDisassociatedPayload{
		ServiceName: testServiceName,
	}

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-009",
		EventType: event.EventTenantServiceDisassociated,
		TenantID:  "tenant-disassoc-001",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.service.disassociated should not return error")

	// Verify tenant removed from cache
	_, ok := consumer.cache.Get("tenant-disassoc-001")
	assert.False(t, ok, "tenant.service.disassociated should remove tenant from cache")
}

func TestHandleLifecycleEvent_ServiceSuspended(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	seedCacheTenant(consumer, "tenant-svcsusp-001")
	ctx := context.Background()

	payload := event.ServiceSuspendedPayload{
		ServiceName: testServiceName,
	}

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-010",
		EventType: event.EventTenantServiceSuspended,
		TenantID:  "tenant-svcsusp-001",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.service.suspended should not return error")

	// Verify tenant removed from cache
	_, ok := consumer.cache.Get("tenant-svcsusp-001")
	assert.False(t, ok, "tenant.service.suspended should remove tenant from cache")
}

func TestHandleLifecycleEvent_ServicePurged(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	seedCacheTenant(consumer, "tenant-purge-001")
	ctx := context.Background()

	payload := event.ServicePurgedPayload{
		ServiceName: testServiceName,
	}

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-011",
		EventType: event.EventTenantServicePurged,
		TenantID:  "tenant-purge-001",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.service.purged should not return error")

	// Verify tenant removed from cache
	_, ok := consumer.cache.Get("tenant-purge-001")
	assert.False(t, ok, "tenant.service.purged should remove tenant from cache")
}

func TestHandleLifecycleEvent_ServiceReactivated(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	ctx := context.Background()

	payload := event.ServiceReactivatedPayload{
		ServiceName:    testServiceName,
		PreviousStatus: "purged",
		ReProvisioned:  true,
		SecretPaths: map[string]map[string]string{
			"transaction": {"postgresql_rw": "new/path/tx-rw"},
		},
		ConnectionSettings: &event.ConnectionSettingsPayload{
			MaxOpenConns: 10,
			MaxIdleConns: 5,
		},
	}

	evt := event.TenantLifecycleEvent{
		EventID:    "evt-012",
		EventType:  event.EventTenantServiceReactivated,
		TenantID:   "tenant-react-001",
		TenantSlug: "acme",
		Payload:    mustMarshalPayload(t, payload),
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.service.reactivated should not return error")

	// Verify tenant is in cache (re-added)
	entry, ok := consumer.cache.Get("tenant-react-001")
	assert.True(t, ok, "tenant.service.reactivated should add tenant to cache")
	require.NotNil(t, entry, "cache entry should not be nil")

	// Verify config fields were populated from payload
	cfg := entry.config
	require.NotNil(t, cfg, "tenant config should not be nil")
	assert.Equal(t, testServiceName, cfg.Service, "service name should match payload")
	require.NotNil(t, cfg.ConnectionSettings, "connection settings should be populated")
	assert.Equal(t, 10, cfg.ConnectionSettings.MaxOpenConns)
	assert.Equal(t, 5, cfg.ConnectionSettings.MaxIdleConns)
	require.NotNil(t, cfg.Databases, "databases map should be populated from secret_paths")
	require.Len(t, cfg.Databases, 1, "should have one database entry")
	assert.NotNil(t, cfg.Databases["transaction"].PostgreSQL, "transaction should have postgresql config")

	// Verify tenant marked as known
	consumer.mu.RLock()
	known := consumer.knownTenants["tenant-react-001"]
	consumer.mu.RUnlock()
	assert.True(t, known, "tenant.service.reactivated should mark tenant as known")
}

func TestHandleLifecycleEvent_ConnectionsUpdated(t *testing.T) {
	t.Parallel()

	consumer := newEventsTestConsumer(t)
	seedCacheTenant(consumer, "tenant-conn-001")
	ctx := context.Background()

	payload := event.ConnectionsUpdatedPayload{
		ServiceName:  testServiceName,
		Module:       "onboarding",
		MaxOpenConns: 50,
		MaxIdleConns: 10,
	}

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-013",
		EventType: event.EventTenantConnectionsUpdated,
		TenantID:  "tenant-conn-001",
		Payload:   mustMarshalPayload(t, payload),
	}

	// handleLifecycleEvent should not error; it applies settings if postgres is set.
	// Without a real postgres manager, it just logs and skips.
	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "tenant.connections.updated should not return error")
}

func TestHandleLifecycleEvent_UnknownEventType(t *testing.T) {
	t.Parallel()

	capLogger := testutil.NewCapturingLogger()
	server := setupTenantManagerAPIServer(t, nil)
	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), capLogger)
	t.Cleanup(func() { consumer.Close() })

	ctx := context.Background()

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-014",
		EventType: "tenant.unknown.action",
		TenantID:  "tenant-unknown-001",
	}

	err := consumer.handleLifecycleEvent(ctx, evt)
	require.NoError(t, err, "unknown event type should not return error")

	// Verify warning was logged
	assert.True(t, capLogger.ContainsSubstring("unknown event type"),
		"should log warning for unknown event type")
}

func TestHandleLifecycleEvent_ServiceEvent_FiltersMismatchedService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType string
		payload   any
	}{
		{
			name:      "disassociated different service",
			eventType: event.EventTenantServiceDisassociated,
			payload:   event.ServiceDisassociatedPayload{ServiceName: "other-service"},
		},
		{
			name:      "suspended different service",
			eventType: event.EventTenantServiceSuspended,
			payload:   event.ServiceSuspendedPayload{ServiceName: "other-service"},
		},
		{
			name:      "purged different service",
			eventType: event.EventTenantServicePurged,
			payload:   event.ServicePurgedPayload{ServiceName: "other-service"},
		},
		{
			name:      "reactivated different service",
			eventType: event.EventTenantServiceReactivated,
			payload:   event.ServiceReactivatedPayload{ServiceName: "other-service"},
		},
		{
			name:      "connections updated different service",
			eventType: event.EventTenantConnectionsUpdated,
			payload:   event.ConnectionsUpdatedPayload{ServiceName: "other-service"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			consumer := newEventsTestConsumer(t)
			seedCacheTenant(consumer, "tenant-filter-001")
			ctx := context.Background()

			evt := event.TenantLifecycleEvent{
				EventID:   "evt-filter",
				EventType: tt.eventType,
				TenantID:  "tenant-filter-001",
				Payload:   mustMarshalPayload(t, tt.payload),
			}

			err := consumer.handleLifecycleEvent(ctx, evt)
			require.NoError(t, err, "mismatched service event should be skipped without error")

			// Verify tenant was NOT removed (event was for different service)
			entry, ok := consumer.cache.Get("tenant-filter-001")
			assert.True(t, ok, "tenant should still be in cache after mismatched service event")
			assert.NotNil(t, entry, "cache entry should not be nil")
		})
	}
}

func TestHandleLifecycleEvent_CredentialsRotated(t *testing.T) {
	t.Parallel()

	t.Run("eager_reload_succeeds", func(t *testing.T) {
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

			// The /connections endpoint is used by GetTenantConfig (loadTenant)
			if strings.Contains(r.URL.Path, "/connections") {
				if err := json.NewEncoder(w).Encode(newConfig); err != nil {
					t.Errorf("failed to encode tenant config response: %v", err)
				}

				return
			}

			// Default: return empty tenant list for other endpoints (e.g. tenant sync)
			if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
				t.Errorf("failed to encode empty response: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())
		consumer.mu.Lock()
		consumer.parentCtx = context.Background()
		consumer.mu.Unlock()
		t.Cleanup(func() { consumer.Close() })

		seedCacheTenant(consumer, "tenant-cred-001")
		ctx := context.Background()

		payload := event.CredentialsRotatedPayload{
			ServiceName:    testServiceName,
			CredentialType: "database",
			NewSecretPath:  "/secrets/new-path",
		}

		evt := event.TenantLifecycleEvent{
			EventID:   "evt-cred",
			EventType: event.EventTenantCredentialsRotated,
			TenantID:  "tenant-cred-001",
			Payload:   mustMarshalPayload(t, payload),
		}

		err := consumer.handleLifecycleEvent(ctx, evt)
		require.NoError(t, err, "tenant.credentials.rotated should not return error")

		// Verify tenant is back in cache (eager reload via loadTenant fetched new credentials)
		entry, ok := consumer.cache.Get("tenant-cred-001")
		assert.True(t, ok, "tenant.credentials.rotated should eagerly reload tenant into cache")
		require.NotNil(t, entry, "cache entry should not be nil after eager reload")
		assert.Equal(t, "acme-rotated", entry.config.TenantSlug, "cache should contain the reloaded config")

		// Verify tenant marked as known
		consumer.mu.RLock()
		known := consumer.knownTenants["tenant-cred-001"]
		consumer.mu.RUnlock()
		assert.True(t, known, "tenant.credentials.rotated should mark tenant as known after reload")
	})

	t.Run("eager_reload_fails_gracefully", func(t *testing.T) {
		t.Parallel()

		// Set up an API server that returns 500 on the /connections endpoint,
		// simulating a temporary failure. The handler should still return nil.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			if strings.Contains(r.URL.Path, "/connections") {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"temporary failure"}`))

				return
			}

			if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
				t.Errorf("failed to encode empty response: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())
		consumer.mu.Lock()
		consumer.parentCtx = context.Background()
		consumer.mu.Unlock()
		t.Cleanup(func() { consumer.Close() })

		seedCacheTenant(consumer, "tenant-cred-002")
		ctx := context.Background()

		payload := event.CredentialsRotatedPayload{
			ServiceName:    testServiceName,
			CredentialType: "database",
			NewSecretPath:  "/secrets/new-path",
		}

		evt := event.TenantLifecycleEvent{
			EventID:   "evt-cred-fail",
			EventType: event.EventTenantCredentialsRotated,
			TenantID:  "tenant-cred-002",
			Payload:   mustMarshalPayload(t, payload),
		}

		err := consumer.handleLifecycleEvent(ctx, evt)
		require.NoError(t, err, "tenant.credentials.rotated should return nil even when eager reload fails")

		// Verify tenant is NOT in cache (eager reload failed, fallback to lazy-load on next request)
		_, ok := consumer.cache.Get("tenant-cred-002")
		assert.False(t, ok, "tenant should not be in cache when eager reload fails (lazy-load fallback)")
	})
}
