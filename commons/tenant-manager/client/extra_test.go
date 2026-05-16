//go:build unit

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/cache"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithSkipCache covers the WithSkipCache option (was 0%).
func TestWithSkipCache(t *testing.T) {
	t.Parallel()

	opts := &getConfigOpts{}
	WithSkipCache()(opts)
	assert.True(t, opts.skipCache)
}

// TestWithCache covers the WithCache option (was 0%).
func TestWithCache_NilDoesNotOverride(t *testing.T) {
	t.Parallel()

	// WithCache with nil should be a no-op; existing cache is preserved.
	c := mustNewClient(t, "http://localhost:8080")

	origCache := c.cache

	opt := WithCache(nil)
	opt(c)

	assert.Equal(t, origCache, c.cache)
}

// TestWithCacheTTL covers the WithCacheTTL option (was 0%).
func TestWithCacheTTL(t *testing.T) {
	t.Parallel()

	c := mustNewClient(t, "http://localhost:8080", WithCacheTTL(5*time.Minute))
	assert.Equal(t, 5*time.Minute, c.cacheTTL)
}

// TestInvalidateConfig covers the InvalidateConfig method (was 0%).
func TestInvalidateConfig_NilCache(t *testing.T) {
	t.Parallel()

	c := mustNewClient(t, "http://localhost:8080")
	c.cache = nil

	err := c.InvalidateConfig(context.Background(), "tenant-1", "ledger")
	require.NoError(t, err, "InvalidateConfig with nil cache should return nil")
}

// TestClose_NoCacheCloser covers Close when cache does not implement io.Closer.
func TestClose_NoCacheCloser(t *testing.T) {
	t.Parallel()

	c := mustNewClient(t, "http://localhost:8080")
	// Default in-memory cache does not implement Close, so Close should return nil.
	err := c.Close()
	require.NoError(t, err)
}

// TestClose_NilCache covers Close when cache is nil.
func TestClose_NilCache(t *testing.T) {
	t.Parallel()

	c := mustNewClient(t, "http://localhost:8080")
	c.cache = nil
	err := c.Close()
	require.NoError(t, err)
}

// TestGetTenantConfig_CacheMissAndHTTPFetch covers the main fetch path (was 75.5%).
func TestGetTenantConfig_FetchFromServer(t *testing.T) {
	t.Parallel()

	cfg := newTestTenantConfig()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(cfg); err != nil {
			t.Errorf("encode error: %v", err)
		}
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	result, err := c.GetTenantConfig(context.Background(), "tenant-123", "ledger")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "tenant-123", result.ID)
}

// TestGetTenantConfig_NotFound covers the 404 path.
func TestGetTenantConfig_NotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	_, err := c.GetTenantConfig(context.Background(), "tenant-missing", "ledger")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTenantNotFound)
}

// TestGetTenantConfig_BadRequest covers the 400 path which falls through default case.
func TestGetTenantConfig_BadRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	_, err := c.GetTenantConfig(context.Background(), "tenant-bad", "ledger")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

// TestGetTenantConfig_WithSkipCache bypasses cache on second call.
func TestGetTenantConfig_WithSkipCache_BypassesCache(t *testing.T) {
	t.Parallel()

	callCount := 0
	cfg := newTestTenantConfig()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(cfg); err != nil {
			t.Errorf("encode error: %v", err)
		}
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	// First call - cache miss, fetches from server
	_, err := c.GetTenantConfig(context.Background(), "tenant-123", "ledger")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Second call with skip cache - should fetch again
	_, err = c.GetTenantConfig(context.Background(), "tenant-123", "ledger", WithSkipCache())
	require.NoError(t, err)
	assert.Equal(t, 2, callCount, "WithSkipCache should bypass cache and fetch again")
}

// TestGetActiveTenantsByService covers the main path.
func TestGetActiveTenantsByService_Success(t *testing.T) {
	t.Parallel()

	summaries := []*TenantSummary{
		{ID: "t1", Name: "t1-name", Status: "active"},
		{ID: "t2", Name: "t2-name", Status: "active"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "service=ledger")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(summaries); err != nil {
			t.Errorf("encode error: %v", err)
		}
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	result, err := c.GetActiveTenantsByService(context.Background(), "ledger")
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "t1", result[0].ID)
}

// TestGetActiveTenantsByService_NotFound covers 404 response.
func TestGetActiveTenantsByService_NotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	_, err := c.GetActiveTenantsByService(context.Background(), "unknown-service")
	require.Error(t, err)
}

// TestTruncateBody covers the truncateBody helper.
func TestTruncateBody(t *testing.T) {
	t.Parallel()

	t.Run("short body is unchanged", func(t *testing.T) {
		body := []byte("short")
		assert.Equal(t, "short", truncateBody(body, 256))
	})

	t.Run("long body is truncated", func(t *testing.T) {
		body := make([]byte, 300)
		for i := range body {
			body[i] = 'x'
		}

		result := truncateBody(body, 256)
		// Truncated to 256 bytes + "...(truncated)" suffix
		assert.LessOrEqual(t, len(result), 270, "result should fit within maxLen + suffix")
		assert.Contains(t, result, "truncated", "truncated body should contain truncation marker")
	})
}

// TestGetTenantConfig_ForbiddenStatus covers 403 response mapping.
func TestGetTenantConfig_ForbiddenStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	_, err := c.GetTenantConfig(context.Background(), "tenant-forbidden", "ledger")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTenantServiceAccessDenied)
}

// TestCacheTenantConfig_InvalidateConfig covers cache set and invalidate path.
func TestInvalidateConfig_WithCacheMiss(t *testing.T) {
	t.Parallel()

	cfg := newTestTenantConfig()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(cfg); err != nil {
			t.Errorf("encode error: %v", err)
		}
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	// Populate cache
	_, err := c.GetTenantConfig(context.Background(), "tenant-123", "ledger")
	require.NoError(t, err)

	// Invalidate
	err = c.InvalidateConfig(context.Background(), "tenant-123", "ledger")
	require.NoError(t, err)
}

// TestWithCircuitBreaker_AppliesThreshold covers the WithCircuitBreaker option.
func TestWithCircuitBreaker_AppliesThreshold(t *testing.T) {
	t.Parallel()

	c := mustNewClient(t, "http://localhost:8080",
		WithCircuitBreaker(3, 30*time.Second),
	)
	assert.Equal(t, 3, c.cbThreshold)
	assert.Equal(t, 30*time.Second, c.cbTimeout)
}

// TestGetTenantConfig_ServerError covers 500 response.
func TestGetTenantConfig_InternalServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL)

	_, err := c.GetTenantConfig(context.Background(), "tenant-err", "ledger")
	require.Error(t, err)
}

// TestNewClient_WithNilLogger_DefaultsToNop tests the nil logger path.
func TestNewClient_WithNilLogger_DefaultsToNop(t *testing.T) {
	t.Parallel()

	c, err := NewClient("http://localhost:8080", nil,
		WithAllowInsecureHTTP(),
		WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// TestNewClient_TypedNilCache covers the typed-nil cache validation.
func TestNewClient_TypedNilCache(t *testing.T) {
	t.Parallel()

	var nilCache *cache.InMemoryCache // typed-nil

	_, err := NewClient("http://localhost:8080", testutil.NewMockLogger(),
		WithAllowInsecureHTTP(),
		WithServiceAPIKey("test-key"),
		WithCache(nilCache),
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNilCache)
}
