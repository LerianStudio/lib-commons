// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package tenantcache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testServiceName is the service name used for loader tests.
const testServiceName = "test-service"

// testServiceAPIKey is the API key used for loader tests.
const testServiceAPIKey = "test-api-key"

// newTestTenantConfig creates a TenantConfig with essential fields for loader tests.
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

// setupLoaderServer creates an httptest server that serves GetTenantConfig responses.
func setupLoaderServer(
	t *testing.T,
	tenantID string,
	config *core.TenantConfig,
	requestCounter *atomic.Int64,
) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/v1/tenants/" + tenantID + "/associations/" + testServiceName + "/connections"

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == expectedPath && r.Method == http.MethodGet:
			if requestCounter != nil {
				requestCounter.Add(1)
			}

			if err := json.NewEncoder(w).Encode(config); err != nil {
				t.Errorf("failed to encode config: %v", err)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))

	t.Cleanup(func() { server.Close() })

	return server
}

// setupLoaderErrorServer creates an httptest server that returns specific HTTP error codes.
func setupLoaderErrorServer(
	t *testing.T,
	tenantID string,
	statusCode int,
	responseBody any,
) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/v1/tenants/" + tenantID + "/associations/" + testServiceName + "/connections"

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == expectedPath && r.Method == http.MethodGet:
			w.WriteHeader(statusCode)

			if responseBody != nil {
				_ = json.NewEncoder(w).Encode(responseBody)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))

	t.Cleanup(func() { server.Close() })

	return server
}

// newTestClient creates a client.Client pointing at the given test server URL.
func newTestClient(t *testing.T, serverURL string) *client.Client {
	t.Helper()

	c, err := client.NewClient(
		serverURL,
		log.NewNop(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey(testServiceAPIKey),
	)
	require.NoError(t, err)

	t.Cleanup(func() { c.Close() })

	return c
}

// NOTE: Goroutine leak detection is handled by TestMain in tenant_cache_test.go
// via goleak.VerifyTestMain(m). Per-test goleak.VerifyNone is not used because
// it falsely detects other parallel test goroutines as leaks.

func TestLoadTenant_Success(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-success"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLoaderServer(t, tenantID, config, &requestCount)

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()
	loader := NewTenantLoader(pmClient, cache, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	ctx := context.Background()

	result, err := loader.LoadTenant(ctx, tenantID)
	require.NoError(t, err, "LoadTenant should succeed for valid tenant")
	require.NotNil(t, result, "LoadTenant should return non-nil config")
	assert.Equal(t, tenantID, result.ID, "returned config should match tenant ID")

	// Verify tenant is cached
	entry, ok := cache.Get(tenantID)
	assert.True(t, ok, "tenant should be in cache after successful load")
	assert.NotNil(t, entry, "cache entry should not be nil")
	assert.Equal(t, tenantID, entry.Config.ID, "cached config should match tenant ID")

	// Verify HTTP call was made
	assert.Equal(t, int64(1), requestCount.Load(), "exactly 1 HTTP call should be made")
}

func TestLoadTenant_Suspended(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-suspended"
	suspendedResponse := map[string]string{
		"code": "FORBIDDEN", "error": "tenant service is suspended", "status": "suspended",
	}
	server := setupLoaderErrorServer(t, tenantID, http.StatusForbidden, suspendedResponse)

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()
	loader := NewTenantLoader(pmClient, cache, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	ctx := context.Background()

	result, err := loader.LoadTenant(ctx, tenantID)
	require.Error(t, err, "LoadTenant should return error for suspended tenant")
	assert.Nil(t, result, "LoadTenant should return nil config for suspended tenant")
	assert.True(t, core.IsTenantSuspendedError(err), "error should be TenantSuspendedError, got: %v", err)

	// Verify NOT cached
	_, ok := cache.Get(tenantID)
	assert.False(t, ok, "suspended tenant should not be cached")
}

func TestLoadTenant_NotFound(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-notfound"
	server := setupLoaderErrorServer(t, tenantID, http.StatusNotFound, map[string]string{"error": "not found"})

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()
	loader := NewTenantLoader(pmClient, cache, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	ctx := context.Background()

	result, err := loader.LoadTenant(ctx, tenantID)
	require.Error(t, err, "LoadTenant should return error for unknown tenant")
	assert.Nil(t, result, "LoadTenant should return nil config for not-found tenant")
	assert.ErrorIs(t, err, core.ErrTenantNotFound, "error should be ErrTenantNotFound, got: %v", err)

	// Verify NOT cached
	_, ok := cache.Get(tenantID)
	assert.False(t, ok, "not-found tenant should not be cached")
}

func TestLoadTenant_AlreadyCached(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-cached"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLoaderServer(t, tenantID, config, &requestCount)

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()
	loader := NewTenantLoader(pmClient, cache, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	// Pre-populate the cache
	cache.Set(tenantID, config, 1*time.Hour)

	ctx := context.Background()

	result, err := loader.LoadTenant(ctx, tenantID)
	require.NoError(t, err, "LoadTenant should succeed when tenant is already cached")
	require.NotNil(t, result, "LoadTenant should return non-nil config from cache")
	assert.Equal(t, tenantID, result.ID, "returned config should match tenant ID")

	// Verify NO HTTP call was made (served from cache)
	assert.Equal(t, int64(0), requestCount.Load(),
		"no HTTP call should be made when tenant is already cached")
}

func TestLoadTenant_ConcurrentLoads(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-concurrent"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLoaderServer(t, tenantID, config, &requestCount)

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()
	loader := NewTenantLoader(pmClient, cache, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	ctx := context.Background()

	const goroutineCount = 10

	var wg sync.WaitGroup

	results := make([]*core.TenantConfig, goroutineCount)
	errs := make([]error, goroutineCount)

	wg.Add(goroutineCount)

	for i := range goroutineCount {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = loader.LoadTenant(ctx, tenantID)
		}(i)
	}

	wg.Wait()

	// All goroutines should succeed
	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d should succeed", i)
		assert.NotNil(t, results[i], "goroutine %d should get non-nil config", i)
	}

	// Verify only 1 HTTP call was made (per-tenant mutex prevents concurrent loads)
	assert.Equal(t, int64(1), requestCount.Load(),
		"exactly 1 HTTP call should be made despite %d concurrent goroutines", goroutineCount)

	// Verify tenant is cached
	entry, ok := cache.Get(tenantID)
	assert.True(t, ok, "tenant should be in cache after concurrent load")
	assert.Equal(t, tenantID, entry.Config.ID, "cached config should match tenant ID")
}

func TestTenantLoader_OnTenantLoaded_CalledAfterSuccess(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-callback-success"
	config := newTestTenantConfig(tenantID)

	server := setupLoaderServer(t, tenantID, config, nil)

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()
	loader := NewTenantLoader(pmClient, cache, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	var callbackTenantID string
	var callbackCalled atomic.Bool

	loader.SetOnTenantLoaded(func(_ context.Context, tid string) {
		callbackTenantID = tid
		callbackCalled.Store(true)
	})

	ctx := context.Background()

	result, err := loader.LoadTenant(ctx, tenantID)
	require.NoError(t, err, "LoadTenant should succeed")
	require.NotNil(t, result, "LoadTenant should return non-nil config")

	assert.True(t, callbackCalled.Load(), "onTenantLoaded callback should be called after successful load")
	assert.Equal(t, tenantID, callbackTenantID, "callback should receive the correct tenant ID")
}

func TestTenantLoader_OnTenantLoaded_NotCalledOnError(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-callback-error"
	server := setupLoaderErrorServer(t, tenantID, http.StatusNotFound, map[string]string{"error": "not found"})

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()
	loader := NewTenantLoader(pmClient, cache, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	var callbackCalled atomic.Bool

	loader.SetOnTenantLoaded(func(_ context.Context, _ string) {
		callbackCalled.Store(true)
	})

	ctx := context.Background()

	result, err := loader.LoadTenant(ctx, tenantID)
	require.Error(t, err, "LoadTenant should return error for unknown tenant")
	assert.Nil(t, result, "LoadTenant should return nil config for not-found tenant")

	assert.False(t, callbackCalled.Load(), "onTenantLoaded callback should NOT be called when load fails")
}

func TestTenantLoader_OnTenantLoaded_NotCalledWhenNil(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-callback-nil"
	config := newTestTenantConfig(tenantID)

	server := setupLoaderServer(t, tenantID, config, nil)

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()
	loader := NewTenantLoader(pmClient, cache, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	ctx := context.Background()

	result, err := loader.LoadTenant(ctx, tenantID)
	require.NoError(t, err, "LoadTenant should succeed without panic when callback is nil")
	require.NotNil(t, result, "LoadTenant should return non-nil config")
	assert.Equal(t, tenantID, result.ID, "returned config should match tenant ID")
}

func TestNewTenantLoader_DefaultTTL(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-loader-default-ttl"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLoaderServer(t, tenantID, config, &requestCount)

	pmClient := newTestClient(t, server.URL)
	cache := NewTenantCache()

	// Pass 0 TTL -- loader should use DefaultTenantCacheTTL
	loader := NewTenantLoader(pmClient, cache, testServiceName, 0, log.NewNop())

	ctx := context.Background()

	result, err := loader.LoadTenant(ctx, tenantID)
	require.NoError(t, err, "LoadTenant should succeed")
	require.NotNil(t, result, "LoadTenant should return non-nil config")

	// Verify tenant is cached and entry is NOT expired (default TTL = 12h)
	entry, ok := cache.Get(tenantID)
	require.True(t, ok, "tenant should be in cache")
	assert.False(t, entry.IsExpired(), "cache entry should not be expired (default TTL should be used)")
}
