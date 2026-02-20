package tenantmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockLogger struct{}

func (m *mockLogger) Info(args ...any)                                  {}
func (m *mockLogger) Infof(format string, args ...any)                  {}
func (m *mockLogger) Infoln(args ...any)                                {}
func (m *mockLogger) Error(args ...any)                                 {}
func (m *mockLogger) Errorf(format string, args ...any)                 {}
func (m *mockLogger) Errorln(args ...any)                               {}
func (m *mockLogger) Warn(args ...any)                                  {}
func (m *mockLogger) Warnf(format string, args ...any)                  {}
func (m *mockLogger) Warnln(args ...any)                                {}
func (m *mockLogger) Debug(args ...any)                                 {}
func (m *mockLogger) Debugf(format string, args ...any)                 {}
func (m *mockLogger) Debugln(args ...any)                               {}
func (m *mockLogger) Fatal(args ...any)                                 {}
func (m *mockLogger) Fatalf(format string, args ...any)                 {}
func (m *mockLogger) Fatalln(args ...any)                               {}
func (m *mockLogger) WithFields(fields ...any) libLog.Logger            { return m }
func (m *mockLogger) WithDefaultMessageTemplate(s string) libLog.Logger { return m }
func (m *mockLogger) Sync() error                                       { return nil }

func TestNewClient(t *testing.T) {
	t.Run("creates client with defaults", func(t *testing.T) {
		client := NewClient("http://localhost:8080", &mockLogger{})

		assert.NotNil(t, client)
		assert.Equal(t, "http://localhost:8080", client.baseURL)
		assert.Equal(t, 30*time.Second, client.httpClient.Timeout)
	})

	t.Run("creates client with custom timeout", func(t *testing.T) {
		client := NewClient("http://localhost:8080", &mockLogger{}, WithTimeout(60*time.Second))

		assert.Equal(t, 60*time.Second, client.httpClient.Timeout)
	})

	t.Run("creates client with custom http client", func(t *testing.T) {
		customClient := &http.Client{Timeout: 10 * time.Second}
		client := NewClient("http://localhost:8080", &mockLogger{}, WithHTTPClient(customClient))

		assert.Equal(t, customClient, client.httpClient)
	})

	t.Run("WithHTTPClient_nil_preserves_default", func(t *testing.T) {
		client := NewClient("http://localhost:8080", &mockLogger{}, WithHTTPClient(nil))

		assert.NotNil(t, client.httpClient, "nil HTTPClient should be ignored, default preserved")
		assert.Equal(t, 30*time.Second, client.httpClient.Timeout)
	})

	t.Run("WithTimeout_after_nil_HTTPClient_does_not_panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			NewClient("http://localhost:8080", &mockLogger{},
				WithHTTPClient(nil),
				WithTimeout(45*time.Second),
			)
		})
	})
}

func TestClient_GetTenantConfig(t *testing.T) {
	t.Run("successful response", func(t *testing.T) {
		config := TenantConfig{
			ID:            "tenant-123",
			TenantSlug:    "test-tenant",
			TenantName:    "Test Tenant",
			Service:       "ledger",
			Status:        "active",
			IsolationMode: "database",
			Databases: map[string]DatabaseConfig{
				"onboarding": {
					PostgreSQL: &PostgreSQLConfig{
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

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/tenants/tenant-123/services/ledger/settings", r.URL.Path)

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(config))
		}))
		defer server.Close()

		client := NewClient(server.URL, &mockLogger{})
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

		client := NewClient(server.URL, &mockLogger{})
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "non-existent", "ledger")

		assert.Nil(t, result)
		assert.ErrorIs(t, err, ErrTenantNotFound)
	})

	t.Run("server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
		}))
		defer server.Close()

		client := NewClient(server.URL, &mockLogger{})
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})

	t.Run("tenant service suspended returns TenantSuspendedError", func(t *testing.T) {
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

		client := NewClient(server.URL, &mockLogger{})
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		require.Error(t, err)
		assert.True(t, IsTenantSuspendedError(err))

		var suspErr *TenantSuspendedError
		require.ErrorAs(t, err, &suspErr)
		assert.Equal(t, "tenant-123", suspErr.TenantID)
		assert.Equal(t, "suspended", suspErr.Status)
		assert.Equal(t, "service ledger is suspended for this tenant", suspErr.Message)
	})

	t.Run("tenant service purged returns TenantSuspendedError", func(t *testing.T) {
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

		client := NewClient(server.URL, &mockLogger{})
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		require.Error(t, err)

		var suspErr *TenantSuspendedError
		require.ErrorAs(t, err, &suspErr)
		assert.Equal(t, "purged", suspErr.Status)
	})

	t.Run("403 with unparseable body returns generic error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("not json"))
		}))
		defer server.Close()

		client := NewClient(server.URL, &mockLogger{})
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		require.Error(t, err)
		assert.False(t, IsTenantSuspendedError(err))
		assert.Contains(t, err.Error(), "access denied")
	})

	t.Run("403 with empty status falls back to generic error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
				"code":  "SOME-OTHER",
				"error": "something else",
			}))
		}))
		defer server.Close()

		client := NewClient(server.URL, &mockLogger{})
		ctx := context.Background()

		result, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")

		assert.Nil(t, result)
		require.Error(t, err)
		assert.False(t, IsTenantSuspendedError(err))
		assert.Contains(t, err.Error(), "access denied")
	})
}

func TestNewClient_WithCircuitBreaker(t *testing.T) {
	t.Run("creates client with circuit breaker option", func(t *testing.T) {
		client := NewClient("http://localhost:8080", &mockLogger{},
			WithCircuitBreaker(5, 30*time.Second),
		)

		assert.Equal(t, 5, client.cbThreshold)
		assert.Equal(t, 30*time.Second, client.cbTimeout)
		assert.Equal(t, cbClosed, client.cbState)
		assert.Equal(t, 0, client.cbFailures)
	})

	t.Run("default client has circuit breaker disabled", func(t *testing.T) {
		client := NewClient("http://localhost:8080", &mockLogger{})

		assert.Equal(t, 0, client.cbThreshold)
		assert.Equal(t, time.Duration(0), client.cbTimeout)
	})
}

func TestClient_CircuitBreaker_StaysClosedOnSuccess(t *testing.T) {
	config := TenantConfig{
		ID:         "tenant-123",
		TenantSlug: "test-tenant",
		Service:    "ledger",
		Status:     "active",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(config))
	}))
	defer server.Close()

	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(3, 30*time.Second))
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
	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// Send threshold number of requests that trigger server errors
	for i := 0; i < threshold; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrCircuitBreakerOpen, "should not be circuit breaker error yet on failure %d", i+1)
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
	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, 30*time.Second))
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
		assert.ErrorIs(t, err, ErrCircuitBreakerOpen)
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
	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, cbTimeout))
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
	assert.NotErrorIs(t, err, ErrCircuitBreakerOpen, "request should pass through in half-open state")
}

func TestClient_CircuitBreaker_ClosesOnSuccessfulHalfOpenRequest(t *testing.T) {
	var shouldSucceed atomic.Bool

	config := TenantConfig{
		ID:         "tenant-123",
		TenantSlug: "test-tenant",
		Service:    "ledger",
		Status:     "active",
	}

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
	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, cbTimeout))
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
	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// Multiple 404s should NOT trigger the circuit breaker
	for i := 0; i < threshold+2; i++ {
		_, err := client.GetTenantConfig(ctx, "non-existent", "ledger")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrTenantNotFound)
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
	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, 30*time.Second))
	ctx := context.Background()

	// Multiple 403s should NOT trigger the circuit breaker
	for i := 0; i < threshold+2; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
		assert.True(t, IsTenantSuspendedError(err))
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
	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, 30*time.Second))
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
	client := NewClient(server.URL, &mockLogger{})
	ctx := context.Background()

	// Even after many failures, requests should still go through
	for i := 0; i < 10; i++ {
		_, err := client.GetTenantConfig(ctx, "tenant-123", "ledger")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrCircuitBreakerOpen)
		assert.Contains(t, err.Error(), "500")
	}

	assert.Equal(t, cbClosed, client.cbState, "circuit breaker should remain closed when disabled")
	assert.Equal(t, 0, client.cbFailures, "failures should not be counted when circuit breaker is disabled")
}

func TestClient_CircuitBreaker_GetActiveTenantsByService(t *testing.T) {
	t.Run("opens on server errors", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("service unavailable"))
		}))
		defer server.Close()

		threshold := 2
		client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, 30*time.Second))
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
		assert.ErrorIs(t, err, ErrCircuitBreakerOpen)
	})

	t.Run("shared state with GetTenantConfig", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway"))
		}))
		defer server.Close()

		threshold := 3
		client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, 30*time.Second))
		ctx := context.Background()

		// Mix failures from both methods - they share the same circuit breaker
		_, _ = client.GetTenantConfig(ctx, "t1", "ledger")     // failure 1
		_, _ = client.GetActiveTenantsByService(ctx, "ledger") // failure 2
		_, _ = client.GetTenantConfig(ctx, "t2", "ledger")     // failure 3 -> opens

		assert.Equal(t, cbOpen, client.cbState)

		// Both methods should fail fast
		_, err := client.GetTenantConfig(ctx, "t3", "ledger")
		assert.ErrorIs(t, err, ErrCircuitBreakerOpen)

		_, err = client.GetActiveTenantsByService(ctx, "ledger")
		assert.ErrorIs(t, err, ErrCircuitBreakerOpen)
	})
}

func TestClient_CircuitBreaker_NetworkErrorCountsAsFailure(t *testing.T) {
	// Use a URL that will definitely fail to connect
	client := NewClient("http://127.0.0.1:1", &mockLogger{},
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
	assert.ErrorIs(t, err, ErrCircuitBreakerOpen)
}

func TestClient_CircuitBreaker_SuccessResetsAfterPartialFailures(t *testing.T) {
	var requestCount atomic.Int32

	config := TenantConfig{
		ID:         "tenant-123",
		TenantSlug: "test-tenant",
		Service:    "ledger",
		Status:     "active",
	}

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
	client := NewClient(server.URL, &mockLogger{}, WithCircuitBreaker(threshold, 30*time.Second))
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

func TestIsCircuitBreakerOpenError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error returns false", nil, false},
		{"ErrCircuitBreakerOpen returns true", ErrCircuitBreakerOpen, true},
		{"other error returns false", ErrTenantNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsCircuitBreakerOpenError(tt.err))
		})
	}
}
