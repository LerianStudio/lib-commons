package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMinimalTenantConfig returns a TenantConfig with only essential fields set.
// Used by circuit breaker tests that do not inspect database configuration.
func newMinimalTenantConfig() core.TenantConfig {
	return core.TenantConfig{
		ID:         "tenant-123",
		TenantSlug: "test-tenant",
		Service:    "ledger",
		Status:     "active",
	}
}

// newTestTenantConfig returns a fully populated TenantConfig for test assertions.
// Callers can override fields after construction for specific test scenarios.
func newTestTenantConfig() core.TenantConfig {
	return core.TenantConfig{
		ID:            "tenant-123",
		TenantSlug:    "test-tenant",
		TenantName:    "Test Tenant",
		Service:       "ledger",
		Status:        "active",
		IsolationMode: "database",
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				PostgreSQL: &core.PostgreSQLConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test_db",
					Username: "user",
					Password: "pass",
					SSLMode:  "disable",
				},
			},
		},
	}
}

func mustNewClient(t *testing.T, baseURL string, opts ...ClientOption) *Client {
	t.Helper()

	// Tests use httptest servers which are http://, so allow insecure by default.
	// A default test API key is provided so tests that don't care about API key auth still pass.
	allOpts := append([]ClientOption{WithAllowInsecureHTTP(), WithServiceAPIKey("test-api-key")}, opts...)

	c, err := NewClient(baseURL, testutil.NewMockLogger(), allOpts...)
	require.NoError(t, err)

	return c
}

func TestNewClient(t *testing.T) {
	t.Run("creates client with defaults", func(t *testing.T) {
		client := mustNewClient(t, "http://localhost:8080")

		assert.NotNil(t, client)
		assert.Equal(t, "http://localhost:8080", client.baseURL)
		assert.Equal(t, 30*time.Second, client.httpClient.Timeout)
	})

	t.Run("creates client with custom timeout", func(t *testing.T) {
		client := mustNewClient(t, "http://localhost:8080", WithTimeout(60*time.Second))

		assert.Equal(t, 60*time.Second, client.httpClient.Timeout)
	})

	t.Run("creates client with custom http client", func(t *testing.T) {
		customClient := &http.Client{Timeout: 10 * time.Second}
		client := mustNewClient(t, "http://localhost:8080", WithHTTPClient(customClient))

		assert.Equal(t, customClient, client.httpClient)
	})

	t.Run("WithHTTPClient_nil_preserves_default", func(t *testing.T) {
		client := mustNewClient(t, "http://localhost:8080", WithHTTPClient(nil))

		assert.NotNil(t, client.httpClient, "nil HTTPClient should be ignored, default preserved")
		assert.Equal(t, 30*time.Second, client.httpClient.Timeout)
	})

	t.Run("WithTimeout_after_nil_HTTPClient_does_not_panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			_, _ = NewClient("http://localhost:8080", testutil.NewMockLogger(),
				WithAllowInsecureHTTP(),
				WithServiceAPIKey("test-key"),
				WithHTTPClient(nil),
				WithTimeout(45*time.Second),
			)
		})
	})

	t.Run("rejects http URL without WithAllowInsecureHTTP", func(t *testing.T) {
		_, err := NewClient("http://localhost:8080", testutil.NewMockLogger(),
			WithServiceAPIKey("test-key"),
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, core.ErrInsecureHTTP)
	})

	t.Run("accepts https URL by default", func(t *testing.T) {
		c, err := NewClient("https://localhost:8080", testutil.NewMockLogger(),
			WithServiceAPIKey("test-key"),
		)
		require.NoError(t, err)
		assert.NotNil(t, c)
	})
}

func TestNewClient_ValidationErrors(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		expectErr bool
	}{
		{
			name:      "empty baseURL returns error",
			baseURL:   "",
			expectErr: true,
		},
		{
			name:      "URL without scheme returns error",
			baseURL:   "localhost:8080",
			expectErr: true,
		},
		{
			name:      "URL without host returns error",
			baseURL:   "http://",
			expectErr: true,
		},
		{
			name:      "invalid URL syntax returns error",
			baseURL:   "://bad-url",
			expectErr: true,
		},
		{
			name:      "http URL without opt-in returns error",
			baseURL:   "http://localhost:8080",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Pass nil logger to also verify the nil-logger defaulting path
			client, err := NewClient(tt.baseURL, nil)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, client)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

func TestClient_GetTenantConfig(t *testing.T) {
	t.Run("successful response", func(t *testing.T) {
		config := newTestTenantConfig()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/tenants/tenant-123/services/ledger/connections", r.URL.Path)

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(config))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		require.NoError(t, err)
		assert.Equal(t, "tenant-123", result.ID)
		assert.Equal(t, "test-tenant", result.TenantSlug)
		pgConfig := result.GetPostgreSQLConfig("ledger", "onboarding")
		assert.NotNil(t, pgConfig)
		assert.Equal(t, "localhost", pgConfig.Host)
	})

	t.Run("tenant not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "non-existent", "ledger")

		assert.Nil(t, result)
		assert.ErrorIs(t, err, core.ErrTenantNotFound)
	})

	t.Run("server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})

	t.Run("tenant service suspended returns TenantSuspendedError wrapping ErrTenantServiceAccessDenied", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
				"code":   "TS-SUSPENDED",
				"error":  "service ledger is suspended for this tenant",
				"status": "suspended",
			}))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		require.Error(t, err)
		// All 403s should be detectable via ErrTenantServiceAccessDenied
		assert.ErrorIs(t, err, core.ErrTenantServiceAccessDenied)
		// Enriched 403s also carry TenantSuspendedError
		assert.True(t, core.IsTenantSuspendedError(err))

		var suspErr *core.TenantSuspendedError
		require.ErrorAs(t, err, &suspErr)
		assert.Equal(t, "tenant-123", suspErr.TenantID)
		assert.Equal(t, "suspended", suspErr.Status)
		assert.Equal(t, "service ledger is suspended for this tenant", suspErr.Message)
	})

	t.Run("tenant service purged returns TenantSuspendedError wrapping ErrTenantServiceAccessDenied", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
				"code":   "TS-SUSPENDED",
				"error":  "service ledger is purged for this tenant",
				"status": "purged",
			}))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		require.Error(t, err)
		assert.ErrorIs(t, err, core.ErrTenantServiceAccessDenied)

		var suspErr *core.TenantSuspendedError
		require.ErrorAs(t, err, &suspErr)
		assert.Equal(t, "purged", suspErr.Status)
	})

	t.Run("403 with unparseable body wraps ErrTenantServiceAccessDenied", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("not json"))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		require.Error(t, err)
		// Non-parseable 403 should still wrap ErrTenantServiceAccessDenied
		assert.ErrorIs(t, err, core.ErrTenantServiceAccessDenied)
		// But should NOT be a TenantSuspendedError (no status info)
		assert.False(t, core.IsTenantSuspendedError(err))
	})

	t.Run("403 with empty status wraps ErrTenantServiceAccessDenied", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
				"code":  "SOME-OTHER",
				"error": "something else",
			}))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		require.Error(t, err)
		// All 403s wrap ErrTenantServiceAccessDenied
		assert.ErrorIs(t, err, core.ErrTenantServiceAccessDenied)
		// Empty status = no TenantSuspendedError
		assert.False(t, core.IsTenantSuspendedError(err))
	})
}

func TestNewClient_WithCircuitBreaker(t *testing.T) {
	t.Run("creates client with circuit breaker option", func(t *testing.T) {
		client := mustNewClient(t, "http://localhost:8080",
			WithCircuitBreaker(5, 30*time.Second),
		)

		assert.Equal(t, 5, client.cbThreshold)
		assert.Equal(t, 30*time.Second, client.cbTimeout)
		assert.Equal(t, cbClosed, client.cbState)
		assert.Equal(t, 0, client.cbFailures)
	})

	t.Run("default client has circuit breaker disabled", func(t *testing.T) {
		client := mustNewClient(t, "http://localhost:8080")

		assert.Equal(t, 0, client.cbThreshold)
		assert.Equal(t, time.Duration(0), client.cbTimeout)
	})
}

func TestClient_CircuitBreaker_StaysClosedOnSuccess(t *testing.T) {
	config := newMinimalTenantConfig()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(config))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL, WithCircuitBreaker(3, 30*time.Second))
	ctx := context.Background()

	// Multiple successful requests should keep circuit breaker closed
	for i := 0; i < 5; i++ {
		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.NoError(t, err)
		assert.Equal(t, "tenant-123", result.ID)
	}

	assert.Equal(t, cbClosed, client.cbState)
	assert.Equal(t, 0, client.cbFailures)
}

func TestClient_CircuitBreaker_OpensAfterThresholdFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	threshold := 3
	client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// Send threshold number of requests that trigger server errors
	for i := 0; i < threshold; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
		assert.NotErrorIs(t, err, core.ErrCircuitBreakerOpen, "should not be circuit breaker error yet on failure %d", i+1)
	}

	// Circuit breaker should now be open
	assert.Equal(t, cbOpen, client.cbState)
	assert.Equal(t, threshold, client.cbFailures)
}

func TestClient_CircuitBreaker_ReturnsErrCircuitBreakerOpenWhenOpen(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	threshold := 2
	client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// Trigger circuit breaker to open
	for i := 0; i < threshold; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
	}

	assert.Equal(t, cbOpen, client.cbState)
	countAfterOpen := requestCount.Load()

	// Subsequent requests should fail fast without hitting the server
	for i := 0; i < 5; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
		assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
	}

	// No additional requests should have reached the server
	assert.Equal(t, countAfterOpen, requestCount.Load(), "no additional HTTP requests should reach server when circuit is open")
}

func TestClient_CircuitBreaker_TransitionsToHalfOpenAfterTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	threshold := 2
	cbTimeout := 50 * time.Millisecond
	client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, cbTimeout))
	ctx := context.Background()

	// Trigger circuit breaker to open
	for i := 0; i < threshold; i++ {
		_, _ = client.GetTenantConfig(ctx, "tenant-123", "ledger")
	}

	assert.Equal(t, cbOpen, client.cbState)

	// Wait for the timeout to expire
	time.Sleep(cbTimeout + 10*time.Millisecond)

	// The next request should be allowed through (half-open probe)
	// It will fail (server still returns 500), but the request should reach the server
	_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrCircuitBreakerOpen, "request should pass through in half-open state")
}

func TestClient_CircuitBreaker_ClosesOnSuccessfulHalfOpenRequest(t *testing.T) {
	var shouldSucceed atomic.Bool

	config := newMinimalTenantConfig()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldSucceed.Load() {
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(config))
			return
		}

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	threshold := 2
	cbTimeout := 50 * time.Millisecond
	client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, cbTimeout))
	ctx := context.Background()

	// Trigger circuit breaker to open
	for i := 0; i < threshold; i++ {
		_, _ = client.GetTenantConfig(ctx, "tenant-123", "ledger")
	}

	assert.Equal(t, cbOpen, client.cbState)

	// Wait for timeout, then make the server return success
	time.Sleep(cbTimeout + 10*time.Millisecond)
	shouldSucceed.Store(true)

	// Half-open probe should succeed and close the circuit breaker
	result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
	require.NoError(t, err)
	assert.Equal(t, "tenant-123", result.ID)
	assert.Equal(t, cbClosed, client.cbState)
	assert.Equal(t, 0, client.cbFailures)
}

func TestClient_CircuitBreaker_404DoesNotCountAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	threshold := 3
	client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// Multiple 404s should NOT trigger the circuit breaker
	for i := 0; i < threshold+2; i++ {
		_, err := client.GetTenantConfig(ctx, "non-existent", "ledger")
		require.Error(t, err)
		assert.ErrorIs(t, err, core.ErrTenantNotFound)
	}

	assert.Equal(t, cbClosed, client.cbState, "404 responses should not open the circuit breaker")
	assert.Equal(t, 0, client.cbFailures, "404 responses should not count as failures")
}

func TestClient_CircuitBreaker_403DoesNotCountAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
			"code":   "TS-SUSPENDED",
			"error":  "service ledger is suspended for this tenant",
			"status": "suspended",
		}))
	}))
	defer server.Close()

	threshold := 3
	client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// Multiple 403s should NOT trigger the circuit breaker
	for i := 0; i < threshold+2; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
		assert.True(t, core.IsTenantSuspendedError(err))
	}

	assert.Equal(t, cbClosed, client.cbState, "403 responses should not open the circuit breaker")
	assert.Equal(t, 0, client.cbFailures, "403 responses should not count as failures")
}

func TestClient_CircuitBreaker_400DoesNotCountAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	threshold := 3
	client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// Multiple 400s should NOT trigger the circuit breaker
	for i := 0; i < threshold+2; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "400")
	}

	assert.Equal(t, cbClosed, client.cbState, "400 responses should not open the circuit breaker")
	assert.Equal(t, 0, client.cbFailures, "400 responses should not count as failures")
}

func TestClient_CircuitBreaker_DisabledByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	// No WithCircuitBreaker option - threshold is 0, circuit breaker disabled
	client := mustNewClient(t, server.URL)
	ctx := context.Background()

	// Even after many failures, requests should still go through
	for i := 0; i < 10; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
		assert.NotErrorIs(t, err, core.ErrCircuitBreakerOpen)
		assert.Contains(t, err.Error(), "500")
	}

	assert.Equal(t, cbClosed, client.cbState, "circuit breaker should remain closed when disabled")
	assert.Equal(t, 0, client.cbFailures, "failures should not be counted when circuit breaker is disabled")
}

func TestClient_GetActiveTenantsByService_Success(t *testing.T) {
	tenants := []*TenantSummary{
		{ID: "tenant-1", Name: "Acme Corp", Status: "active"},
		{ID: "tenant-2", Name: "Globex Inc", Status: "active"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/tenants/active", r.URL.Path)
		assert.Equal(t, "ledger", r.URL.Query().Get("service"))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(tenants))
	}))
	defer server.Close()

	client := mustNewClient(t, server.URL)
	ctx := context.Background()

	result, err := client.GetActiveTenantsByService(ctx, "ledger")

	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "tenant-1", result[0].ID)
	assert.Equal(t, "Acme Corp", result[0].Name)
	assert.Equal(t, "active", result[0].Status)
	assert.Equal(t, "tenant-2", result[1].ID)
	assert.Equal(t, "Globex Inc", result[1].Name)
	assert.Equal(t, "active", result[1].Status)
}

func TestClient_CircuitBreaker_GetActiveTenantsByService(t *testing.T) {
	t.Run("opens on server errors", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("service unavailable"))
		}))
		defer server.Close()

		threshold := 2
		client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
		ctx := context.Background()

		// Trigger circuit breaker via GetActiveTenantsByService
		for i := 0; i < threshold; i++ {
			_, err := client.GetActiveTenantsByService(ctx, "ledger")
			require.Error(t, err)
		}

		assert.Equal(t, cbOpen, client.cbState)

		// Should fail fast
		_, err := client.GetActiveTenantsByService(ctx, "ledger")
		require.Error(t, err)
		assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
	})

	t.Run("shared state with GetTenantConfig", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway"))
		}))
		defer server.Close()

		threshold := 3
		client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
		ctx := context.Background()

		// Mix failures from both methods - they share the same circuit breaker
		_, _ = client.GetTenantConfig(ctx, "t1", "ledger")     // failure 1
		_, _ = client.GetActiveTenantsByService(ctx, "ledger") // failure 2
		_, _ = client.GetTenantConfig(ctx, "t2", "ledger")     // failure 3 -> opens

		assert.Equal(t, cbOpen, client.cbState)

		// Both methods should fail fast
		_, err := client.GetTenantConfig(ctx, "t3", "ledger")
		assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)

		_, err = client.GetActiveTenantsByService(ctx, "ledger")
		assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
	})
}

func TestClient_CircuitBreaker_NetworkErrorCountsAsFailure(t *testing.T) {
	// Use a URL that will definitely fail to connect
	client := mustNewClient(t, "http://127.0.0.1:1",
		WithCircuitBreaker(2, 30*time.Second),
		WithTimeout(100*time.Millisecond),
	)
	ctx := context.Background()

	// Network errors should count as failures
	for i := 0; i < 2; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
	}

	assert.Equal(t, cbOpen, client.cbState, "network errors should trigger circuit breaker")

	// Should fail fast now
	_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
}

func TestClient_CircuitBreaker_SuccessResetsAfterPartialFailures(t *testing.T) {
	var requestCount atomic.Int32

	config := newMinimalTenantConfig()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		// Fail on first 2 requests, succeed on the rest
		if count <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal error"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(config))
	}))
	defer server.Close()

	threshold := 3
	client := mustNewClient(t, server.URL, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// 2 failures (below threshold)
	_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
	require.Error(t, err)
	_, err = client.GetTenantConfig(ctx, "tenant-123", "ledger")
	require.Error(t, err)
	assert.Equal(t, 2, client.cbFailures)
	assert.Equal(t, cbClosed, client.cbState, "should still be closed - below threshold")

	// A success resets the counter
	result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
	require.NoError(t, err)
	assert.Equal(t, "tenant-123", result.ID)
	assert.Equal(t, 0, client.cbFailures, "success should reset failure count")
	assert.Equal(t, cbClosed, client.cbState)
}

func TestIsServerError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		expected   bool
	}{
		{"200 OK is not server error", http.StatusOK, false},
		{"400 Bad Request is not server error", http.StatusBadRequest, false},
		{"403 Forbidden is not server error", http.StatusForbidden, false},
		{"404 Not Found is not server error", http.StatusNotFound, false},
		{"499 is not server error", 499, false},
		{"500 Internal Server Error is server error", http.StatusInternalServerError, true},
		{"502 Bad Gateway is server error", http.StatusBadGateway, true},
		{"503 Service Unavailable is server error", http.StatusServiceUnavailable, true},
		{"504 Gateway Timeout is server error", http.StatusGatewayTimeout, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isServerError(tt.statusCode))
		})
	}
}

func TestWithServiceAPIKey(t *testing.T) {
	t.Run("sets serviceAPIKey on client", func(t *testing.T) {
		client := mustNewClient(t, "http://localhost:8080",
			WithServiceAPIKey("my-secret-key"),
		)

		assert.Equal(t, "my-secret-key", client.serviceAPIKey)
	})

	t.Run("missing WithServiceAPIKey returns error", func(t *testing.T) {
		_, err := NewClient("http://localhost:8080", testutil.NewMockLogger(),
			WithAllowInsecureHTTP(),
		)

		require.Error(t, err)
		assert.ErrorIs(t, err, core.ErrServiceAPIKeyRequired)
	})

	t.Run("empty string returns error", func(t *testing.T) {
		_, err := NewClient("http://localhost:8080", testutil.NewMockLogger(),
			WithAllowInsecureHTTP(),
			WithServiceAPIKey(""),
		)

		require.Error(t, err)
		assert.ErrorIs(t, err, core.ErrServiceAPIKeyRequired)
	})

	t.Run("valid key succeeds", func(t *testing.T) {
		c, err := NewClient("http://localhost:8080", testutil.NewMockLogger(),
			WithAllowInsecureHTTP(),
			WithServiceAPIKey("valid-key"),
		)

		require.NoError(t, err)
		assert.Equal(t, "valid-key", c.serviceAPIKey)
	})
}

func TestClient_GetTenantConfig_APIKeyHeader(t *testing.T) {
	t.Run("sends X-API-Key header when serviceAPIKey is set", func(t *testing.T) {
		config := newMinimalTenantConfig()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "my-secret-key", r.Header.Get("X-API-Key"))

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(config))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL, WithServiceAPIKey("my-secret-key"))
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		require.NoError(t, err)
		assert.Equal(t, "tenant-123", result.ID)
	})

	t.Run("sends default test API key from mustNewClient", func(t *testing.T) {
		config := newMinimalTenantConfig()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "test-api-key", r.Header.Get("X-API-Key"),
				"mustNewClient provides a default test API key")

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(config))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		require.NoError(t, err)
		assert.Equal(t, "tenant-123", result.ID)
	})
}

func TestClient_GetActiveTenantsByService_APIKeyHeader(t *testing.T) {
	t.Run("sends X-API-Key header when serviceAPIKey is set", func(t *testing.T) {
		tenants := []*TenantSummary{
			{ID: "tenant-1", Name: "Acme Corp", Status: "active"},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "my-secret-key", r.Header.Get("X-API-Key"))

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(tenants))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL, WithServiceAPIKey("my-secret-key"))
		ctx := context.Background()

		result, err := client.GetActiveTenantsByService(ctx, "ledger")

		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, "tenant-1", result[0].ID)
	})

	t.Run("sends default test API key from mustNewClient", func(t *testing.T) {
		tenants := []*TenantSummary{
			{ID: "tenant-1", Name: "Acme Corp", Status: "active"},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "test-api-key", r.Header.Get("X-API-Key"),
				"mustNewClient provides a default test API key")

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(tenants))
		}))
		defer server.Close()

		client := mustNewClient(t, server.URL)
		ctx := context.Background()

		result, err := client.GetActiveTenantsByService(ctx, "ledger")

		require.NoError(t, err)
		require.Len(t, result, 1)
	})
}

func TestCacheKeyPrefix(t *testing.T) {
	t.Run("uses tenant-connections prefix", func(t *testing.T) {
		assert.Equal(t, "tenant-connections", cacheKeyPrefix,
			"cacheKeyPrefix must match the renamed /connections endpoint")
	})
}

func TestIsCircuitBreakerOpenError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error returns false", nil, false},
		{"ErrCircuitBreakerOpen returns true", core.ErrCircuitBreakerOpen, true},
		{"other error returns false", core.ErrTenantNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, core.IsCircuitBreakerOpenError(tt.err))
		})
	}
}
