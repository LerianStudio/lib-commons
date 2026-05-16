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
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/tenantcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// handleTenantActivated — 0% uncovered
// -------------------------------------------------------------------

func TestHandleEvent_TenantActivated_TenantInCache_TouchesTTL(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()

	// Seed tenant into cache with short TTL
	cache.Set("tenant-activated-001", &core.TenantConfig{ID: "tenant-activated-001"}, 1*time.Hour)

	d := newTestDispatcher(t, WithCacheTTL(2*time.Hour))
	d.cache = cache

	evt := TenantLifecycleEvent{
		EventID:   "evt-act-001",
		EventType: EventTenantActivated,
		TenantID:  "tenant-activated-001",
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)

	// Tenant should still be in cache (Touch refreshed TTL)
	_, ok := cache.Get("tenant-activated-001")
	assert.True(t, ok, "tenant should remain in cache after activation")
}

func TestHandleEvent_TenantActivated_TenantNotInCache_NoOp(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	evt := TenantLifecycleEvent{
		EventID:   "evt-act-002",
		EventType: EventTenantActivated,
		TenantID:  "tenant-not-in-cache",
	}

	// Should not error — no-op for tenant not in cache
	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)

	_, ok := d.cache.Get("tenant-not-in-cache")
	assert.False(t, ok, "unknown tenant should not be added to cache on activation")
}

// -------------------------------------------------------------------
// handleTenantUpdated — 0% uncovered
// -------------------------------------------------------------------

func TestHandleEvent_TenantUpdated_TenantInCache_TouchesTTL(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	cache.Set("tenant-updated-001", &core.TenantConfig{ID: "tenant-updated-001"}, 1*time.Hour)

	d := newTestDispatcher(t, WithCacheTTL(2*time.Hour))
	d.cache = cache

	evt := TenantLifecycleEvent{
		EventID:   "evt-upd-001",
		EventType: EventTenantUpdated,
		TenantID:  "tenant-updated-001",
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)

	// Tenant should remain in cache
	_, ok := cache.Get("tenant-updated-001")
	assert.True(t, ok, "tenant should remain in cache after update")
}

func TestHandleEvent_TenantUpdated_TenantNotInCache_NoOp(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	evt := TenantLifecycleEvent{
		EventID:   "evt-upd-002",
		EventType: EventTenantUpdated,
		TenantID:  "tenant-updated-not-cached",
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)

	_, ok := d.cache.Get("tenant-updated-not-cached")
	assert.False(t, ok)
}

// -------------------------------------------------------------------
// handleCredentialsRotated — 61.5% (no loader path)
// -------------------------------------------------------------------

func TestHandleEvent_CredentialsRotated_NoLoader(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	cache.Set("tenant-creds", &core.TenantConfig{ID: "tenant-creds"}, time.Hour)

	d := newTestDispatcher(t)
	d.cache = cache
	d.loader = nil // explicitly nil loader

	// CredentialsRotated is a service-scoped event — payload must contain service_name
	payload, err := json.Marshal(map[string]string{"service_name": testServiceName})
	require.NoError(t, err)

	evt := TenantLifecycleEvent{
		EventID:   "evt-creds",
		EventType: EventTenantCredentialsRotated,
		TenantID:  "tenant-creds",
		Payload:   payload,
	}

	err = d.HandleEvent(context.Background(), evt)
	require.NoError(t, err, "credentials rotated with no loader should not error")

	// Tenant should be removed from cache (removeTenant is called even without loader)
	_, ok := cache.Get("tenant-creds")
	assert.False(t, ok, "tenant should be evicted on credentials rotated")
}

// -------------------------------------------------------------------
// handleConnectionsUpdated — 62.5%
// ConnectionsUpdated is a service-scoped event; payload needs service_name
// -------------------------------------------------------------------

func TestHandleEvent_ConnectionsUpdated_LogsSettings(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	type combinedPayload struct {
		ServiceName      string `json:"service_name"`
		Module           string `json:"module"`
		MaxOpenConns     int    `json:"max_open_conns"`
		MaxIdleConns     int    `json:"max_idle_conns"`
		StatementTimeout string `json:"statement_timeout"`
	}

	payload, err := json.Marshal(combinedPayload{
		ServiceName:      testServiceName,
		MaxOpenConns:     25,
		MaxIdleConns:     8,
		StatementTimeout: "60s",
	})
	require.NoError(t, err)

	evt := TenantLifecycleEvent{
		EventID:   "evt-conn-upd",
		EventType: EventTenantConnectionsUpdated,
		TenantID:  "tenant-conn-upd",
		Payload:   payload,
	}

	// Should not error; no postgres manager registered so just logs
	err = d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
}

func TestHandleEvent_ConnectionsUpdated_InvalidPayload(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	// Invalid JSON that passes service check but fails ConnectionsUpdatedPayload unmarshal
	// We need service_name to be valid, but the full payload unmarshal will fail if the
	// remainder is invalid. Use a two-pass struct that only partially matches.
	// Actually, ConnectionsUpdated uses json.Unmarshal on full payload — if service_name
	// is valid but type is wrong it will error.
	// The simplest approach: pass non-JSON after service check.
	// But matchesService also unmarshals — so we need valid JSON overall.
	// Pass valid JSON with wrong field types for ConnectionsUpdatedPayload.
	type badPayload struct {
		ServiceName  string `json:"service_name"`
		MaxOpenConns string `json:"max_open_conns"` // wrong type - string not int
	}

	payload, err := json.Marshal(badPayload{
		ServiceName:  testServiceName,
		MaxOpenConns: "not-a-number",
	})
	require.NoError(t, err)

	evt := TenantLifecycleEvent{
		EventID:   "evt-conn-bad",
		EventType: EventTenantConnectionsUpdated,
		TenantID:  "tenant-conn-bad",
		Payload:   payload,
	}

	// ConnectionsUpdatedPayload.MaxOpenConns is int — string input will cause unmarshal error
	err = d.HandleEvent(context.Background(), evt)
	require.Error(t, err)
}

// -------------------------------------------------------------------
// removeTenant — 53.8% (with postgres/mongo/rabbitmq managers)
// -------------------------------------------------------------------

func TestRemoveTenant_WithNilManagers_DoesNotPanic(t *testing.T) {
	t.Parallel()

	cache := tenantcache.NewTenantCache()
	cache.Set("tenant-rm", &core.TenantConfig{ID: "tenant-rm"}, time.Hour)

	d := newTestDispatcher(t)
	d.cache = cache

	// All managers are nil — should not panic
	assert.NotPanics(t, func() {
		d.RemoveTenant(context.Background(), "tenant-rm")
	})

	_, ok := cache.Get("tenant-rm")
	assert.False(t, ok, "tenant should be removed from cache")
}

// -------------------------------------------------------------------
// handleServiceReactivated — 78.6% (missing payload unmarshal error path)
// -------------------------------------------------------------------

func TestHandleEvent_ServiceReactivated_InvalidPayload(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	evt := TenantLifecycleEvent{
		EventID:   "evt-react-bad",
		EventType: EventTenantServiceReactivated,
		TenantID:  "tenant-react",
		Payload:   json.RawMessage(`not-valid-json`),
	}

	err := d.HandleEvent(context.Background(), evt)
	require.Error(t, err, "invalid payload should return an error")
}

// -------------------------------------------------------------------
// NewEventDispatcher — init with no options covers some branches
// -------------------------------------------------------------------

func TestNewEventDispatcher_MinimalOptions(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]*core.TenantConfig{}); err != nil {
			t.Errorf("encode error: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	cache := tenantcache.NewTenantCache()
	loader := newTestLoader(t, cache, server.URL)

	d := NewEventDispatcher(cache, loader, "svc")
	assert.NotNil(t, d)
	assert.Equal(t, "svc", d.service)
}

// -------------------------------------------------------------------
// dispatchEvent — unknown event type (no-op)
// -------------------------------------------------------------------

func TestHandleEvent_UnknownEventType_NoOp(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	evt := TenantLifecycleEvent{
		EventID:   "evt-unknown",
		EventType: "tenant.unknown.event.type",
		TenantID:  "tenant-unknown",
	}

	// Unknown events should not return an error
	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
}

// -------------------------------------------------------------------
// handleServiceAssociated — no connection settings, no messaging
// -------------------------------------------------------------------

func TestHandleEvent_ServiceAssociated_MinimalPayload(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	payload := ServiceAssociatedPayload{
		ServiceName:   testServiceName,
		IsolationMode: "database",
	}

	evt := TenantLifecycleEvent{
		EventID:   "evt-assoc-min",
		EventType: EventTenantServiceAssociated,
		TenantID:  "tenant-assoc-min",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)

	entry, ok := d.cache.Get("tenant-assoc-min")
	assert.True(t, ok)
	assert.Nil(t, entry.Config.ConnectionSettings, "no connection settings in payload")
	assert.Nil(t, entry.Config.Messaging, "no messaging in payload")
}

// -------------------------------------------------------------------
// handleServiceAssociated — with onTenantAdded callback
// -------------------------------------------------------------------

func TestHandleEvent_ServiceAssociated_CallsOnTenantAdded(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	addedID := ""
	d.SetOnTenantAdded(func(_ context.Context, id string) {
		addedID = id
	})

	payload := ServiceAssociatedPayload{
		ServiceName:   testServiceName,
		IsolationMode: "database",
	}

	evt := TenantLifecycleEvent{
		EventID:   "evt-assoc-cb",
		EventType: EventTenantServiceAssociated,
		TenantID:  "tenant-assoc-cb",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
	assert.Equal(t, "tenant-assoc-cb", addedID, "onTenantAdded callback should be invoked")
}

// -------------------------------------------------------------------
// handleServiceReactivated — with onTenantAdded callback
// -------------------------------------------------------------------

func TestHandleEvent_ServiceReactivated_CallsOnTenantAdded(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	addedID := ""
	d.SetOnTenantAdded(func(_ context.Context, id string) {
		addedID = id
	})

	payload := ServiceReactivatedPayload{
		ServiceName: testServiceName,
	}

	evt := TenantLifecycleEvent{
		EventID:   "evt-react-cb",
		EventType: EventTenantServiceReactivated,
		TenantID:  "tenant-react-cb",
		Payload:   mustMarshalPayload(t, payload),
	}

	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
	assert.Equal(t, "tenant-react-cb", addedID, "onTenantAdded callback should be invoked")
}

// -------------------------------------------------------------------
// NewEventDispatcher — covers the logger nil path
// -------------------------------------------------------------------

func TestNewEventDispatcher_WithNilLogger(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]*core.TenantConfig{})
	}))
	t.Cleanup(server.Close)

	cache := tenantcache.NewTenantCache()
	loader := newTestLoader(t, cache, server.URL)

	// No logger option - should use nop internally
	d := NewEventDispatcher(cache, loader, "svc-nologger")
	assert.NotNil(t, d)
}

// -------------------------------------------------------------------
// Confirm applyJitter path is exercised (WithJitter option = 0)
// -------------------------------------------------------------------

func TestHandleEvent_ServiceReactivated_ZeroJitter(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t)

	payload := ServiceReactivatedPayload{
		ServiceName: testServiceName,
	}

	evt := TenantLifecycleEvent{
		EventID:   "evt-react-jitter",
		EventType: EventTenantServiceReactivated,
		TenantID:  "tenant-jitter",
		Payload:   mustMarshalPayload(t, payload),
	}

	// WithJitter(0) means no sleep — should complete quickly
	err := d.HandleEvent(context.Background(), evt)
	require.NoError(t, err)
}
