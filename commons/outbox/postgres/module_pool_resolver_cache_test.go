//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
)

type countingTenantPoolResolver struct {
	*modulePoolResolverStub
	listCalls atomic.Int32
}

func TestNewModulePoolResolverWithConfig_TopologyRefreshInterval_NormalizesDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		interval     time.Duration
		wantInterval time.Duration
	}{
		{name: "zero uses default", wantInterval: time.Minute},
		{name: "negative uses default", interval: -time.Second, wantInterval: time.Minute},
		{name: "positive is retained", interval: 90 * time.Second, wantInterval: 90 * time.Second},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver, err := NewModulePoolResolverWithConfig(
				&modulePoolResolverStub{},
				"default-tenant",
				func(context.Context, string) (*tmcore.TenantConfig, error) {
					return tenantConfigWithDatabases("generic", "consignado"), nil
				},
				ModulePoolResolverConfig{TopologyRefreshInterval: test.interval},
			)

			require.NoError(t, err)
			assert.Equal(t, test.wantInterval, resolver.refreshInterval)
		})
	}
}

func (resolver *countingTenantPoolResolver) ListTenants(ctx context.Context) ([]string, error) {
	resolver.listCalls.Add(1)

	return resolver.modulePoolResolverStub.ListTenants(ctx)
}

func TestModulePoolResolver_ListTenantDispatchScopes_ReusesTopologyWithinRefreshInterval(t *testing.T) {
	t.Parallel()

	generic := &countingTenantPoolResolver{modulePoolResolverStub: &modulePoolResolverStub{tenants: []string{"tenant-a"}}}
	var loadCalls atomic.Int32
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			loadCalls.Add(1)

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)

	first, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	second, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, int32(1), generic.listCalls.Load())
	assert.Equal(t, int32(1), loadCalls.Load())
}

func TestModulePoolResolver_ListTenantDispatchScopes_ConcurrentMissCoalescesRefresh(t *testing.T) {
	t.Parallel()

	const callers = 8

	generic := &countingTenantPoolResolver{modulePoolResolverStub: &modulePoolResolverStub{tenants: []string{"tenant-a"}}}
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	var loadCalls atomic.Int32
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			if loadCalls.Add(1) == 1 {
				close(loadStarted)
			}
			<-releaseLoad

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)

	results := make(chan []outbox.TenantDispatchScope, callers)
	errors := make(chan error, callers)
	var waitGroup sync.WaitGroup
	for range callers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()

			scopes, listErr := resolver.ListTenantDispatchScopes(t.Context())
			results <- scopes
			errors <- listErr
		}()
	}
	<-loadStarted
	close(releaseLoad)
	waitGroup.Wait()
	close(results)
	close(errors)

	for listErr := range errors {
		require.NoError(t, listErr)
	}
	for scopes := range results {
		assert.Equal(t, []outbox.TenantDispatchScope{
			{TenantID: "tenant-a"},
			{TenantID: "tenant-a", PoolKey: "consignado"},
		}, scopes)
	}
	assert.Equal(t, int32(1), generic.listCalls.Load())
	assert.Equal(t, int32(1), loadCalls.Load())
}

func TestModulePoolResolver_ListTenantDispatchScopes_RefreshesAfterTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	generic := &countingTenantPoolResolver{modulePoolResolverStub: &modulePoolResolverStub{tenants: []string{"tenant-a"}}}
	var loadCalls atomic.Int32
	resolver, err := NewModulePoolResolverWithConfig(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			loadCalls.Add(1)

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePoolResolverConfig{TopologyRefreshInterval: time.Minute},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)
	resolver.now = func() time.Time { return now }

	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	now = now.Add(59 * time.Second)
	_, err = resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	now = now.Add(time.Second)
	_, err = resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, int32(2), generic.listCalls.Load())
	assert.Equal(t, int32(2), loadCalls.Load())
}

func TestModulePoolResolver_InvalidateTopology_RefreshesTenantAddAndRemove(t *testing.T) {
	t.Parallel()

	generic := &countingTenantPoolResolver{modulePoolResolverStub: &modulePoolResolverStub{tenants: []string{"default-tenant", "tenant-a"}}}
	resolver, err := NewModulePoolResolver(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)

	initial, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	require.Contains(t, initial, outbox.TenantDispatchScope{TenantID: "default-tenant"})

	generic.setTenants([]string{"default-tenant", "tenant-a", "tenant-b"})
	resolver.InvalidateTopology()
	withAddedTenant, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	assert.Contains(t, withAddedTenant, outbox.TenantDispatchScope{TenantID: "tenant-b"})

	generic.setTenants([]string{"default-tenant", "tenant-b"})
	resolver.EvictTenant("tenant-a")
	withoutRemovedTenant, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.NotContains(t, withoutRemovedTenant, outbox.TenantDispatchScope{TenantID: "tenant-a"})
	assert.Contains(t, withoutRemovedTenant, outbox.TenantDispatchScope{TenantID: "default-tenant"})
	assert.Equal(t, int32(3), generic.listCalls.Load())
}

func TestModulePoolResolver_ListTenantDispatchScopes_FailedOwnershipRetriesAfterTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	generic := &countingTenantPoolResolver{modulePoolResolverStub: &modulePoolResolverStub{tenants: []string{"tenant-a"}}}
	var available atomic.Bool
	var loadCalls atomic.Int32
	resolver, err := NewModulePoolResolverWithConfig(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			loadCalls.Add(1)
			if !available.Load() {
				return nil, assert.AnError
			}

			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePoolResolverConfig{TopologyRefreshInterval: time.Minute},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)
	resolver.now = func() time.Time { return now }

	failed, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	require.Empty(t, failed)
	available.Store(true)
	now = now.Add(59 * time.Second)
	stillCached, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	require.Empty(t, stillCached)
	now = now.Add(time.Second)
	refreshed, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []outbox.TenantDispatchScope{
		{TenantID: "tenant-a"},
		{TenantID: "tenant-a", PoolKey: "consignado"},
	}, refreshed)
	assert.Equal(t, int32(2), loadCalls.Load())
}

func TestModulePoolResolver_ListTenantDispatchScopes_StaleRosterRetriesAfterTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	generic := &countingTenantPoolResolver{modulePoolResolverStub: &modulePoolResolverStub{tenants: []string{"tenant-a"}}}
	resolver, err := NewModulePoolResolverWithConfig(
		generic,
		"default-tenant",
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return tenantConfigWithDatabases("generic", "consignado"), nil
		},
		ModulePoolResolverConfig{TopologyRefreshInterval: time.Minute},
		ModulePool{Name: "consignado", Resolver: &modulePoolResolverStub{}},
	)
	require.NoError(t, err)
	resolver.now = func() time.Time { return now }

	initial, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	generic.mu.Lock()
	generic.listErr = assert.AnError
	generic.mu.Unlock()
	resolver.InvalidateTopology()
	stale, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	assert.Equal(t, initial, stale)

	now = now.Add(59 * time.Second)
	stillStale, err := resolver.ListTenantDispatchScopes(t.Context())
	require.NoError(t, err)
	assert.Equal(t, initial, stillStale)
	generic.mu.Lock()
	generic.listErr = nil
	generic.tenants = []string{"tenant-b"}
	generic.mu.Unlock()
	now = now.Add(time.Second)
	refreshed, err := resolver.ListTenantDispatchScopes(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []outbox.TenantDispatchScope{
		{TenantID: "tenant-b"},
		{TenantID: "tenant-b", PoolKey: "consignado"},
	}, refreshed)
	assert.Equal(t, int32(3), generic.listCalls.Load())
}
