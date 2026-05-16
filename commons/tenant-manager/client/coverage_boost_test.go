//go:build unit

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// WithTimeout — init branch (httpClient nil before option applied)
// -------------------------------------------------------------------

func TestWithTimeout_InitializesHTTPClient(t *testing.T) {
	t.Parallel()

	c := mustNewClient(t, "http://localhost:8080", WithTimeout(45*time.Second))
	assert.Equal(t, 45*time.Second, c.httpClient.Timeout)
}

// -------------------------------------------------------------------
// cacheTenantConfig — marshal path + getCachedTenantConfig hit
// -------------------------------------------------------------------

func TestCacheTenantConfig_CacheHitOnSecondCall(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		cfg := newTestTenantConfig()
		if err := json.NewEncoder(w).Encode(cfg); err != nil {
			t.Errorf("encode error: %v", err)
		}
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	// First call populates the cache via cacheTenantConfig
	result, err := c.GetTenantConfig(context.Background(), "tenant-cache-boost", "ledger")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, callCount)

	// Second call should hit the cache
	result2, err := c.GetTenantConfig(context.Background(), "tenant-cache-boost", "ledger")
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.Equal(t, 1, callCount, "second call should use cache, not make another HTTP request")
}

// -------------------------------------------------------------------
// getCachedTenantConfig — missing key path
// -------------------------------------------------------------------

func TestGetCachedTenantConfig_NotInCache(t *testing.T) {
	t.Parallel()

	c := mustNewClient(t, "http://localhost:8080")
	cached, ok := c.getCachedTenantConfig(context.Background(), "key-miss", "tenant-x", "ledger")
	assert.False(t, ok)
	assert.Nil(t, cached)
}

// -------------------------------------------------------------------
// GetActiveTenantsByService — server error (5xx)
// -------------------------------------------------------------------

func TestGetActiveTenantsByService_ServerError5xx(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal server error"}`))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)
	_, err := c.GetActiveTenantsByService(context.Background(), "ledger")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

// -------------------------------------------------------------------
// GetActiveTenantsByService — circuit breaker opens
// -------------------------------------------------------------------

func TestGetActiveTenantsByService_CircuitBreakerOpens(t *testing.T) {
	t.Parallel()

	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Circuit opens after 2 failures
	c := mustNewClient(t, server.URL, WithCircuitBreaker(2, 30*time.Second))

	// First 2 calls hit the server and fail (recording failures)
	for i := 0; i < 2; i++ {
		_, _ = c.GetActiveTenantsByService(context.Background(), "ledger")
	}

	// Third call should fail fast (circuit open)
	_, err := c.GetActiveTenantsByService(context.Background(), "ledger")
	require.Error(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount), "third call should fail fast without hitting upstream")
}

// -------------------------------------------------------------------
// GetActiveTenantsByService — parse error path
// -------------------------------------------------------------------

func TestGetActiveTenantsByService_ParseError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not-valid-json`))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)
	_, err := c.GetActiveTenantsByService(context.Background(), "ledger")
	require.Error(t, err)
}

// -------------------------------------------------------------------
// GetTenantConfig — suspended tenant 403
// -------------------------------------------------------------------

func TestGetTenantConfig_SuspendedTenant(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"code":"TS-SUSPENDED","error":"service suspended","status":"suspended"}`))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)
	_, err := c.GetTenantConfig(context.Background(), "tenant-susp", "ledger")
	require.Error(t, err)
	// The error should be recognizable as a suspended error
	assert.True(t, core.IsTenantSuspendedError(err))
}

// -------------------------------------------------------------------
// truncateBody
// -------------------------------------------------------------------

func TestTruncateBody_ShortBody(t *testing.T) {
	t.Parallel()

	body := []byte("short body")
	result := truncateBody(body, 512)
	assert.Equal(t, "short body", result)
}

func TestTruncateBody_LongBody(t *testing.T) {
	t.Parallel()

	body := make([]byte, 600)
	for i := range body {
		body[i] = 'x'
	}

	result := truncateBody(body, 512)
	assert.True(t, len(result) <= 512+len("... (truncated)"))
	assert.Contains(t, result, "truncated")
}

// -------------------------------------------------------------------
// newDefaultHTTPClient
// -------------------------------------------------------------------

func TestNewDefaultHTTPClient_ReturnsClient(t *testing.T) {
	t.Parallel()

	httpClient := newDefaultHTTPClient()
	require.NotNil(t, httpClient)
	assert.Equal(t, 30*time.Second, httpClient.Timeout)
}
