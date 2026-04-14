// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
)

// newEnsureTestConsumer creates a consumer with a mock API server.
// It does NOT call Run() -- the caller must set parentCtx manually
// to isolate the EnsureConsumerStarted path.
func newEnsureTestConsumer(
	t *testing.T,
	apiURL string,
) *MultiTenantConsumer {
	t.Helper()

	config := newTestConfig(apiURL)
	consumer, err := NewMultiTenantConsumerWithError(config, testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
	require.NoError(t, err)

	// Simulate Run() having set parentCtx.
	ctx := context.Background()
	consumer.mu.Lock()
	consumer.parentCtx = ctx
	consumer.mu.Unlock()

	t.Cleanup(func() { consumer.Close() })

	return consumer
}

func TestEnsureConsumerStarted_UnknownTenant_LazyLoads(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-ensure-lazy-001"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLazyLoadServer(t, tenantID, config, &requestCount)

	consumer := newEnsureTestConsumer(t, server.URL)

	ctx := context.Background()

	// Tenant is NOT in knownTenants -- EnsureConsumerStarted
	// should trigger a lazy-load via TenantLoader instead of rejecting.
	consumer.EnsureConsumerStarted(ctx, tenantID)

	// Verify: TenantLoader was called (HTTP request made to API)
	assert.Equal(t, int64(1), requestCount.Load(),
		"EnsureConsumerStarted should call TenantLoader (1 HTTP request)")

	// Verify: tenant is now known
	consumer.mu.RLock()
	known := consumer.knownTenants[tenantID]
	consumer.mu.RUnlock()
	assert.True(t, known, "tenant should be marked as known after lazy-load")

	// Verify: tenant is cached
	entry, cached := consumer.cache.Get(tenantID)
	assert.True(t, cached, "tenant should be in cache after lazy-load")

	if cached && entry != nil {
		assert.Equal(t, tenantID, entry.Config.ID, "cached config ID should match")
	}
}

func TestEnsureConsumerStarted_UnknownTenant_LoadFails(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-ensure-fail-001"

	// API returns 500 for this tenant
	server := setupLazyLoadErrorServer(t, tenantID, http.StatusInternalServerError,
		map[string]string{"error": "internal server error"})

	consumer := newEnsureTestConsumer(t, server.URL)

	ctx := context.Background()

	// EnsureConsumerStarted should attempt lazy-load but it fails --
	// tenant should NOT be started, NOT be known, NOT be cached.
	consumer.EnsureConsumerStarted(ctx, tenantID)

	// Verify: tenant is NOT known
	consumer.mu.RLock()
	known := consumer.knownTenants[tenantID]
	consumer.mu.RUnlock()
	assert.False(t, known, "tenant should NOT be known after failed lazy-load")

	// Verify: tenant is NOT cached
	_, cached := consumer.cache.Get(tenantID)
	assert.False(t, cached, "tenant should NOT be in cache after failed lazy-load")

	// Verify: no active consumer for the tenant
	consumer.mu.RLock()
	_, hasConsumer := consumer.tenants[tenantID]
	consumer.mu.RUnlock()
	assert.False(t, hasConsumer, "no consumer should be started for failed lazy-load")
}

func TestEnsureConsumerStarted_ExpiredTenant_Reloads(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-ensure-expired-001"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLazyLoadServer(t, tenantID, config, &requestCount)

	consumer := newEnsureTestConsumer(t, server.URL)

	// Seed the tenant as known and in cache, but with an already-expired TTL.
	consumer.mu.Lock()
	consumer.knownTenants[tenantID] = true
	consumer.mu.Unlock()

	expiredConfig := &core.TenantConfig{
		ID:         tenantID,
		TenantSlug: tenantID + "-slug-old",
		Service:    testServiceName,
		Status:     "active",
	}
	// Set with a TTL of 1 nanosecond so it's immediately expired.
	consumer.cache.Set(tenantID, expiredConfig, 1*time.Nanosecond)

	// Wait briefly to ensure the entry expires.
	time.Sleep(5 * time.Millisecond)

	ctx := context.Background()

	// EnsureConsumerStarted should detect the expired cache entry,
	// delete it, and re-lazy-load from API.
	consumer.EnsureConsumerStarted(ctx, tenantID)

	// Verify: API was called to refresh the tenant
	assert.Equal(t, int64(1), requestCount.Load(),
		"EnsureConsumerStarted should re-load expired tenant from API")

	// Verify: tenant is in cache with fresh data
	entry, cached := consumer.cache.Get(tenantID)
	assert.True(t, cached, "tenant should be re-cached after TTL refresh")
	require.NotNil(t, entry, "cache entry should not be nil")
	assert.Equal(t, tenantID, entry.Config.ID, "re-cached config should have correct ID")
}

func TestEnsureConsumerStarted_KnownTenant_NoLazyLoad(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-ensure-known-001"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLazyLoadServer(t, tenantID, config, &requestCount)

	consumer := newEnsureTestConsumer(t, server.URL)

	// Pre-mark tenant as known (simulating a previous load)
	consumer.mu.Lock()
	consumer.knownTenants[tenantID] = true
	consumer.mu.Unlock()

	// Also seed in cache with valid TTL
	consumer.cache.Set(tenantID, config, 1*time.Hour)

	ctx := context.Background()

	// EnsureConsumerStarted should see the tenant is known and proceed normally --
	// no lazy-load call should be made.
	consumer.EnsureConsumerStarted(ctx, tenantID)

	// Verify: NO HTTP call to API (tenant already known, no lazy-load needed)
	assert.Equal(t, int64(0), requestCount.Load(),
		"known tenant should NOT trigger lazy-load API call")
}
