// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package discovery

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	tmredis "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/redis"
)

// mockFetcher implements ActiveTenantsFetcher for tests.
type mockFetcher struct {
	tenants []*client.TenantSummary
	err     error
}

func (m *mockFetcher) GetActiveTenantsByService(_ context.Context, _ string) ([]*client.TenantSummary, error) {
	return m.tenants, m.err
}

// --------------------------------------------------------------------------
// Constructor validation tests
// --------------------------------------------------------------------------

func TestNewTenantDiscovery_ValidConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    &mockFetcher{},
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "localhost"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)

	require.NoError(t, err)
	require.NotNil(t, td)
}

func TestNewTenantDiscovery_MissingServiceName(t *testing.T) {
	t.Parallel()

	cfg := Config{
		TMClient:    &mockFetcher{},
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "localhost"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)

	require.Error(t, err)
	assert.Nil(t, td)
	assert.Contains(t, err.Error(), "ServiceName")
}

func TestNewTenantDiscovery_MissingTMClient(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServiceName: "ledger",
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "localhost"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)

	require.Error(t, err)
	assert.Nil(t, td)
	assert.Contains(t, err.Error(), "TMClient")
}

func TestNewTenantDiscovery_MissingRedisHost(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    &mockFetcher{},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)

	require.Error(t, err)
	assert.Nil(t, td)
	assert.Contains(t, err.Error(), "RedisConfig.Host")
}

// --------------------------------------------------------------------------
// Start / Bootstrap tests
// --------------------------------------------------------------------------

func TestTenantDiscovery_Start_BootstrapSuccess(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{
			{ID: "t-aaa", Name: "Acme", Status: "active"},
			{ID: "t-bbb", Name: "Globex", Status: "active"},
		},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	ids := td.ActiveTenantIDs()
	sort.Strings(ids)

	assert.Equal(t, []string{"t-aaa", "t-bbb"}, ids)

	_ = td.Close()
}

func TestTenantDiscovery_Start_BootstrapFailure(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		err: errors.New("tenant-manager unavailable"),
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "localhost"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant-manager unavailable")
}

func TestTenantDiscovery_Start_RedisUnavailable(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{
			{ID: "t-111", Name: "Corp", Status: "active"},
		},
	}

	capLogger := testutil.NewCapturingLogger()

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      capLogger,
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	// Start should succeed even though Redis is unreachable (bootstrap-only mode)
	err = td.Start(ctx)
	require.NoError(t, err)

	// Tenants from bootstrap should still be available
	ids := td.ActiveTenantIDs()
	assert.Equal(t, []string{"t-111"}, ids)

	// Should have logged a warning about Redis
	assert.True(t, capLogger.ContainsSubstring("redis") || capLogger.ContainsSubstring("Redis") || capLogger.ContainsSubstring("pub/sub"),
		"expected warning about Redis unavailability, got: %v", capLogger.GetMessages())

	_ = td.Close()
}

// --------------------------------------------------------------------------
// ActiveTenantIDs tests
// --------------------------------------------------------------------------

func TestTenantDiscovery_ActiveTenantIDs_Empty(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    &mockFetcher{},
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "localhost"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	// Before Start, should return empty slice
	ids := td.ActiveTenantIDs()

	require.NotNil(t, ids, "should return non-nil empty slice")
	assert.Empty(t, ids)
}

func TestTenantDiscovery_ActiveTenantIDs_Snapshot(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{
			{ID: "t-snap-1", Name: "One", Status: "active"},
			{ID: "t-snap-2", Name: "Two", Status: "active"},
		},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	// Get a snapshot
	ids := td.ActiveTenantIDs()
	assert.Len(t, ids, 2)

	// Mutate the returned slice
	ids[0] = "MUTATED"

	// Get another snapshot; should be unaffected
	ids2 := td.ActiveTenantIDs()
	sort.Strings(ids2)

	assert.Equal(t, []string{"t-snap-1", "t-snap-2"}, ids2)

	_ = td.Close()
}

// --------------------------------------------------------------------------
// Callback tests (via internal event handler)
// --------------------------------------------------------------------------

func TestTenantDiscovery_OnTenantAdded_Callback(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	var mu sync.Mutex
	var addedIDs []string

	td.OnTenantAdded(func(_ context.Context, tenantID string) {
		mu.Lock()
		addedIDs = append(addedIDs, tenantID)
		mu.Unlock()
	})

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	// Simulate a tenant.service.associated event via the internal handler
	evt := event.TenantLifecycleEvent{
		EventID:   "evt-add-1",
		EventType: event.EventTenantServiceAssociated,
		TenantID:  "t-new",
		Payload:   mustMarshalServicePayload("ledger"),
	}

	handlerErr := td.HandleEvent(ctx, evt)
	require.NoError(t, handlerErr)

	// Verify callback was fired
	mu.Lock()
	assert.Equal(t, []string{"t-new"}, addedIDs)
	mu.Unlock()

	// Verify tenant is in the active set
	ids := td.ActiveTenantIDs()
	assert.Contains(t, ids, "t-new")

	_ = td.Close()
}

func TestTenantDiscovery_OnTenantRemoved_Callback(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{
			{ID: "t-del", Name: "Deletable", Status: "active"},
		},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	var mu sync.Mutex
	var removedIDs []string

	td.OnTenantRemoved(func(_ context.Context, tenantID string) {
		mu.Lock()
		removedIDs = append(removedIDs, tenantID)
		mu.Unlock()
	})

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	// Verify tenant is bootstrapped
	assert.Contains(t, td.ActiveTenantIDs(), "t-del")

	// Simulate a tenant.suspended event
	evt := event.TenantLifecycleEvent{
		EventID:   "evt-del-1",
		EventType: event.EventTenantSuspended,
		TenantID:  "t-del",
	}

	handlerErr := td.HandleEvent(ctx, evt)
	require.NoError(t, handlerErr)

	// Verify callback was fired
	mu.Lock()
	assert.Equal(t, []string{"t-del"}, removedIDs)
	mu.Unlock()

	// Verify tenant is no longer in the active set
	assert.NotContains(t, td.ActiveTenantIDs(), "t-del")

	_ = td.Close()
}

// --------------------------------------------------------------------------
// Close tests
// --------------------------------------------------------------------------

func TestTenantDiscovery_Close(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{
			{ID: "t-close", Name: "Close", Status: "active"},
		},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	// Close should succeed
	err = td.Close()
	require.NoError(t, err)

	// ActiveTenantIDs should still work after Close
	ids := td.ActiveTenantIDs()
	assert.Contains(t, ids, "t-close")
}

// --------------------------------------------------------------------------
// Event routing tests
// --------------------------------------------------------------------------

func TestTenantDiscovery_EventRouting_ServiceDisassociated(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{
			{ID: "t-rm", Name: "Remove", Status: "active"},
		},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	assert.Contains(t, td.ActiveTenantIDs(), "t-rm")

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-disassoc",
		EventType: event.EventTenantServiceDisassociated,
		TenantID:  "t-rm",
		Payload:   mustMarshalServicePayload("ledger"),
	}

	handlerErr := td.HandleEvent(ctx, evt)
	require.NoError(t, handlerErr)

	assert.NotContains(t, td.ActiveTenantIDs(), "t-rm")

	_ = td.Close()
}

func TestTenantDiscovery_EventRouting_ServiceReactivated(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	assert.Empty(t, td.ActiveTenantIDs())

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-react",
		EventType: event.EventTenantServiceReactivated,
		TenantID:  "t-reactivated",
		Payload:   mustMarshalServicePayload("ledger"),
	}

	handlerErr := td.HandleEvent(ctx, evt)
	require.NoError(t, handlerErr)

	assert.Contains(t, td.ActiveTenantIDs(), "t-reactivated")

	_ = td.Close()
}

func TestTenantDiscovery_EventRouting_TenantDeleted(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{
			{ID: "t-deleted", Name: "Del", Status: "active"},
		},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	assert.Contains(t, td.ActiveTenantIDs(), "t-deleted")

	evt := event.TenantLifecycleEvent{
		EventID:   "evt-del",
		EventType: event.EventTenantDeleted,
		TenantID:  "t-deleted",
	}

	handlerErr := td.HandleEvent(ctx, evt)
	require.NoError(t, handlerErr)

	assert.NotContains(t, td.ActiveTenantIDs(), "t-deleted")

	_ = td.Close()
}

func TestTenantDiscovery_EventRouting_NoOp(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{
			{ID: "t-noop", Name: "Noop", Status: "active"},
		},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	// tenant.created, tenant.activated, tenant.updated are no-ops for discovery
	noopEvents := []string{
		event.EventTenantCreated,
		event.EventTenantActivated,
		event.EventTenantUpdated,
	}

	for _, evtType := range noopEvents {
		evt := event.TenantLifecycleEvent{
			EventID:   "evt-noop",
			EventType: evtType,
			TenantID:  "t-noop",
		}

		handlerErr := td.HandleEvent(ctx, evt)
		require.NoError(t, handlerErr, "event type %s should be no-op", evtType)
	}

	// Tenant set should remain unchanged
	assert.Contains(t, td.ActiveTenantIDs(), "t-noop")
	assert.Len(t, td.ActiveTenantIDs(), 1)

	_ = td.Close()
}

func TestTenantDiscovery_EventRouting_ServiceMismatch(t *testing.T) {
	t.Parallel()

	fetcher := &mockFetcher{
		tenants: []*client.TenantSummary{},
	}

	cfg := Config{
		ServiceName: "ledger",
		TMClient:    fetcher,
		RedisConfig: tmredis.TenantPubSubRedisConfig{Host: "unreachable.invalid"},
		Logger:      testutil.NewMockLogger(),
	}

	td, err := NewTenantDiscovery(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	err = td.Start(ctx)
	require.NoError(t, err)

	// Service associated event for a different service should be ignored
	evt := event.TenantLifecycleEvent{
		EventID:   "evt-other",
		EventType: event.EventTenantServiceAssociated,
		TenantID:  "t-other",
		Payload:   mustMarshalServicePayload("other-service"),
	}

	handlerErr := td.HandleEvent(ctx, evt)
	require.NoError(t, handlerErr)

	// Should NOT have added the tenant
	assert.NotContains(t, td.ActiveTenantIDs(), "t-other")

	_ = td.Close()
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

func mustMarshalServicePayload(serviceName string) []byte {
	return []byte(`{"service_name":"` + serviceName + `"}`)
}
