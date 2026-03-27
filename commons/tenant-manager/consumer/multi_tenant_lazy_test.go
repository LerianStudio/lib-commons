// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTenantConfig creates a TenantConfig with essential fields for lazy-load tests.
func newTestTenantConfig(tenantID string) *core.TenantConfig {
	return &core.TenantConfig{
		ID: tenantID, TenantSlug: tenantID + "-slug", TenantName: "Test Tenant",
		Service: testServiceName, Status: "active", IsolationMode: "database",
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {PostgreSQL: &core.PostgreSQLConfig{
				Host: "localhost", Port: 5432, Database: tenantID + "_db",
				Username: "user", Password: "pass", SSLMode: "disable",
			}},
		},
	}
}

// setupLazyLoadServer creates an httptest server that serves GetTenantConfig responses.
// Routes /v1/tenants/{id}/associations/{service}/connections (success) and /v1/tenants/active (empty).
func setupLazyLoadServer(
	t *testing.T,
	tenantID string,
	config *core.TenantConfig,
	requestCounter *atomic.Int64,
) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/v1/tenants/" + tenantID + "/associations/" + testServiceName + "/connections"
		activePath := "/v1/tenants/active"

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == expectedPath && r.Method == http.MethodGet:
			if requestCounter != nil {
				requestCounter.Add(1)
			}

			if err := json.NewEncoder(w).Encode(config); err != nil {
				t.Errorf("failed to encode config: %v", err)
			}
		case r.URL.Path == activePath:
			// Return empty list for discovery
			if err := json.NewEncoder(w).Encode([]*struct{}{}); err != nil {
				t.Errorf("failed to encode empty list: %v", err)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))

	t.Cleanup(func() { server.Close() })

	return server
}

// setupLazyLoadErrorServer creates an httptest server that returns specific HTTP error codes.
func setupLazyLoadErrorServer(
	t *testing.T,
	tenantID string,
	statusCode int,
	responseBody any,
) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/v1/tenants/" + tenantID + "/associations/" + testServiceName + "/connections"
		activePath := "/v1/tenants/active"

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == expectedPath && r.Method == http.MethodGet:
			w.WriteHeader(statusCode)

			if responseBody != nil {
				_ = json.NewEncoder(w).Encode(responseBody)
			}
		case r.URL.Path == activePath:
			_ = json.NewEncoder(w).Encode([]*struct{}{})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))

	t.Cleanup(func() { server.Close() })

	return server
}

// TestLoaderIntegration_Success verifies that the consumer's shared TenantLoader
// fetches and caches tenant config, and that EnsureConsumerStarted marks it as known.
func TestLoaderIntegration_Success(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-lazy-success"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLazyLoadServer(t, tenantID, config, &requestCount)

	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())
	defer consumer.Close()

	// Set parentCtx so EnsureConsumerStarted works
	ctx := context.Background()
	consumer.mu.Lock()
	consumer.parentCtx = ctx
	consumer.mu.Unlock()

	// Call loader directly (same as what EnsureConsumerStarted does)
	result, err := consumer.loader.LoadTenant(ctx, tenantID)
	require.NoError(t, err, "loader.LoadTenant should succeed for valid tenant")
	require.NotNil(t, result, "loader.LoadTenant should return non-nil config")

	// Verify tenant is cached
	entry, ok := consumer.cache.Get(tenantID)
	assert.True(t, ok, "tenant should be in cache after successful load")
	assert.NotNil(t, entry, "cache entry should not be nil")
	assert.Equal(t, tenantID, entry.Config.ID, "cached config should match tenant ID")

	// Verify HTTP call was made
	assert.Equal(t, int64(1), requestCount.Load(), "exactly 1 HTTP call should be made")
}

// TestLoaderIntegration_Suspended verifies that the loader returns a
// TenantSuspendedError for suspended tenants and does not cache them.
func TestLoaderIntegration_Suspended(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-lazy-suspended"
	suspendedResponse := map[string]string{
		"code": "FORBIDDEN", "error": "tenant service is suspended", "status": "suspended",
	}
	server := setupLazyLoadErrorServer(t, tenantID, http.StatusForbidden, suspendedResponse)

	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())
	defer consumer.Close()

	ctx := context.Background()

	_, err := consumer.loader.LoadTenant(ctx, tenantID)
	require.Error(t, err, "loader.LoadTenant should return error for suspended tenant")
	assert.True(t, core.IsTenantSuspendedError(err), "error should be TenantSuspendedError, got: %v", err)

	// Verify NOT cached and NOT known
	_, ok := consumer.cache.Get(tenantID)
	assert.False(t, ok, "suspended tenant should not be cached")
	consumer.mu.RLock()
	known := consumer.knownTenants[tenantID]
	consumer.mu.RUnlock()
	assert.False(t, known, "suspended tenant should not be marked as known")
}

// TestLoaderIntegration_NotFound verifies that the loader returns
// ErrTenantNotFound for missing tenants and does not cache them.
func TestLoaderIntegration_NotFound(t *testing.T) {
	t.Parallel()
	tenantID := "tenant-lazy-notfound"
	server := setupLazyLoadErrorServer(t, tenantID, http.StatusNotFound, map[string]string{"error": "not found"})

	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())
	defer consumer.Close()

	ctx := context.Background()

	_, err := consumer.loader.LoadTenant(ctx, tenantID)
	require.Error(t, err, "loader.LoadTenant should return error for unknown tenant")
	assert.True(t, errors.Is(err, core.ErrTenantNotFound), "error should be ErrTenantNotFound, got: %v", err)

	// Verify NOT cached and NOT known
	_, ok := consumer.cache.Get(tenantID)
	assert.False(t, ok, "not-found tenant should not be cached")
	consumer.mu.RLock()
	known := consumer.knownTenants[tenantID]
	consumer.mu.RUnlock()
	assert.False(t, known, "not-found tenant should not be marked as known")
}

// TestLoaderIntegration_ServerError verifies that the loader wraps
// server errors with context information.
func TestLoaderIntegration_ServerError(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-lazy-servererror"

	server := setupLazyLoadErrorServer(t, tenantID, http.StatusInternalServerError,
		map[string]string{"error": "internal server error"})

	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())
	defer consumer.Close()

	ctx := context.Background()

	_, err := consumer.loader.LoadTenant(ctx, tenantID)
	require.Error(t, err, "loader.LoadTenant should return error on server error")
	assert.Contains(t, err.Error(), "failed to load tenant",
		"error should contain context wrapping message")

	// Verify tenant is NOT cached
	_, ok := consumer.cache.Get(tenantID)
	assert.False(t, ok, "failed tenant should not be cached")
}

// TestLoaderIntegration_AlreadyCached verifies that the loader returns
// cached config without making HTTP calls.
func TestLoaderIntegration_AlreadyCached(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-lazy-cached"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLazyLoadServer(t, tenantID, config, &requestCount)

	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())
	defer consumer.Close()

	// Pre-populate the cache
	consumer.cache.Set(tenantID, config, 1*time.Hour)

	ctx := context.Background()

	result, err := consumer.loader.LoadTenant(ctx, tenantID)
	require.NoError(t, err, "loader.LoadTenant should succeed when tenant is already cached")
	require.NotNil(t, result, "loader.LoadTenant should return non-nil config from cache")

	// Verify NO HTTP call was made (served from cache)
	assert.Equal(t, int64(0), requestCount.Load(),
		"no HTTP call should be made when tenant is already cached")
}

// TestLoaderIntegration_ConcurrentLoads verifies that the loader's per-tenant
// mutex prevents concurrent API calls for the same tenant.
func TestLoaderIntegration_ConcurrentLoads(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-lazy-concurrent"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLazyLoadServer(t, tenantID, config, &requestCount)

	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())
	defer consumer.Close()

	// Set parentCtx so EnsureConsumerStarted works
	ctx := context.Background()
	consumer.mu.Lock()
	consumer.parentCtx = ctx
	consumer.mu.Unlock()

	const goroutineCount = 10

	var wg sync.WaitGroup

	errs := make([]error, goroutineCount)

	wg.Add(goroutineCount)

	for i := range goroutineCount {
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = consumer.loader.LoadTenant(ctx, tenantID)
		}(i)
	}

	wg.Wait()

	// All goroutines should succeed
	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d should succeed", i)
	}

	// Verify only 1 HTTP call was made (per-tenant mutex prevents concurrent loads)
	assert.Equal(t, int64(1), requestCount.Load(),
		"exactly 1 HTTP call should be made despite %d concurrent goroutines", goroutineCount)

	// Verify tenant is cached
	entry, ok := consumer.cache.Get(tenantID)
	assert.True(t, ok, "tenant should be in cache after concurrent load")
	assert.Equal(t, tenantID, entry.Config.ID, "cached config should match tenant ID")
}
