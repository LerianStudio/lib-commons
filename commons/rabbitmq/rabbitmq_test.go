// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v3/commons/log"
	"github.com/stretchr/testify/assert"
)

// mockRabbitMQConnection extends RabbitMQConnection to allow mocking for tests
type mockRabbitMQConnection struct {
	RabbitMQConnection
	connectError    bool
	healthyResponse bool
	authFails       bool
}

func (m *mockRabbitMQConnection) setupMockServer() *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check basic auth
		username, password, ok := r.BasicAuth()
		if !ok || username != m.User || password != m.Pass {
			// When auth fails, return a 200 but with error status in JSON
			// This tests how the HealthCheck method parses the response
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"not_authorized"}`))
			return
		}

		// Set content type for JSON response
		w.Header().Set("Content-Type", "application/json")

		// Return appropriate status based on test case
		if m.healthyResponse {
			w.Write([]byte(`{"status":"ok"}`))
		} else {
			w.Write([]byte(`{"status":"error"}`))
		}
	}))

	return server
}

func TestRabbitMQConnection_Connect(t *testing.T) {
	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	// We can't easily test the actual connection in unit tests
	// So we'll focus on testing the error handling

	tests := []struct {
		name              string
		connectionString  string
		expectError       bool
		skipDetailedCheck bool
	}{
		{
			name:              "invalid connection string",
			connectionString:  "amqp://invalid-host:5672",
			expectError:       true,
			skipDetailedCheck: true, // The detailed connection check would never be reached
		},
		{
			name:              "valid format but unreachable",
			connectionString:  "amqp://guest:guest@localhost:5999",
			expectError:       true,
			skipDetailedCheck: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &RabbitMQConnection{
				ConnectionStringSource: tt.connectionString,
				Logger:                 logger,
			}

			// This will always fail in a unit test environment without a real RabbitMQ
			// We're just testing the error handling
			err := conn.Connect()

			if tt.expectError {
				assert.Error(t, err)
				assert.False(t, conn.Connected)
				assert.Nil(t, conn.Channel)
			} else {
				// We don't expect this branch to be taken in unit tests
				assert.NoError(t, err)
				assert.True(t, conn.Connected)
				assert.NotNil(t, conn.Channel)
			}
		})
	}
}

func TestRabbitMQConnection_GetNewConnect(t *testing.T) {
	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	t.Run("not connected - will try to connect", func(t *testing.T) {
		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5999", // Unreachable
			Logger:                 logger,
			Connected:              false,
		}

		ch, err := conn.GetNewConnect()
		assert.Error(t, err)
		assert.Nil(t, ch)
		assert.False(t, conn.Connected)
	})

	t.Run("already connected", func(t *testing.T) {
		// This test requires mocking the Channel which is difficult
		// since we can't create a real AMQP channel in a unit test
		t.Skip("Requires integration testing with a real RabbitMQ instance")
	})
}

func TestRabbitMQConnection_HealthCheck(t *testing.T) {
	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	tests := []struct {
		name           string
		setupServer    bool
		mockResponse   string
		expectHealthy  bool
		invalidRequest bool
	}{
		{
			name:          "healthy server",
			setupServer:   true,
			mockResponse:  `{"status":"ok"}`,
			expectHealthy: true,
		},
		{
			name:          "unhealthy server",
			setupServer:   true,
			mockResponse:  `{"status":"error"}`,
			expectHealthy: false,
		},
		{
			name:           "invalid request",
			setupServer:    false,
			invalidRequest: true,
			expectHealthy:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &RabbitMQConnection{
				HealthCheckURL: "localhost",
				Host:           "localhost",
				User:           "worg",
				Pass:           "pass",
				Logger:         logger,
			}

			if tt.invalidRequest {
				// Invalid host/port for request to fail
				conn.Host = "invalid::/host"
				conn.Port = "invalid"

				isHealthy := conn.HealthCheck()
				assert.False(t, isHealthy)
				return
			}

			if tt.setupServer {
				// Setup a test server that returns the mock response
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(tt.mockResponse))
				}))
				defer server.Close()

				// Parse the server URL to get host and port
				hostParts := strings.SplitN(server.URL, ":", 2)
				conn.Host = hostParts[0]
				if len(hostParts) > 1 {
					conn.Port = hostParts[1]
				}
				conn.HealthCheckURL = server.URL

				// Run the test
				isHealthy := conn.HealthCheck()
				assert.Equal(t, tt.expectHealthy, isHealthy)
			}
		})
	}
}

func TestRabbitMQConnection_HealthCheck_Authentication(t *testing.T) {
	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	// Create test server with authentication check
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check basic auth
		username, password, ok := r.BasicAuth()
		if !ok || username != "correct" || password != "correct" {
			// Return unauthorized status
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Valid auth, return healthy response
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// Parse the server URL
	hostParts := strings.SplitN(server.URL, ":", 2)
	host := hostParts[0]
	var port string
	if len(hostParts) > 1 {
		port = hostParts[1]
	}

	// Test with incorrect credentials
	badAuthConn := &RabbitMQConnection{
		Host:   host,
		Port:   port,
		User:   "wrong",
		Pass:   "wrong",
		Logger: logger,
	}

	isHealthy := badAuthConn.HealthCheck()
	assert.False(t, isHealthy, "HealthCheck should return false with invalid credentials")

	// Test with correct credentials
	goodAuthConn := &RabbitMQConnection{
		HealthCheckURL: server.URL,
		Host:           host,
		Port:           port,
		User:           "correct",
		Pass:           "correct",
		Logger:         logger,
	}

	isHealthy = goodAuthConn.HealthCheck()
	assert.True(t, isHealthy, "HealthCheck should return true with valid credentials")
}

func TestBuildRabbitMQConnectionString(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		user     string
		pass     string
		host     string
		port     string
		vhost    string
		expected string
	}{
		{
			name:     "empty vhost - backward compatibility",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "",
			expected: "amqp://guest:guest@localhost:5672",
		},
		{
			name:     "custom vhost - production",
			protocol: "amqp",
			user:     "admin",
			pass:     "secret",
			host:     "rabbitmq.example.com",
			port:     "5672",
			vhost:    "production",
			expected: "amqp://admin:secret@rabbitmq.example.com:5672/production",
		},
		{
			name:     "custom vhost - staging",
			protocol: "amqps",
			user:     "user",
			pass:     "pass",
			host:     "secure.rabbitmq.io",
			port:     "5671",
			vhost:    "staging",
			expected: "amqps://user:pass@secure.rabbitmq.io:5671/staging",
		},
		{
			name:     "root vhost explicit - URL encoded as %2F",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "/",
			expected: "amqp://guest:guest@localhost:5672/%2F",
		},
		{
			name:     "vhost with special characters - spaces",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "my vhost",
			expected: "amqp://guest:guest@localhost:5672/my%20vhost",
		},
		{
			name:     "vhost with special characters - slashes",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "env/prod/region1",
			expected: "amqp://guest:guest@localhost:5672/env%2Fprod%2Fregion1",
		},
		{
			name:     "vhost with special characters - hash and ampersand",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "test#1&2",
			expected: "amqp://guest:guest@localhost:5672/test%231%262",
		},
		{
			name:     "password with special characters",
			protocol: "amqp",
			user:     "admin",
			pass:     "p@ss:word/123",
			host:     "localhost",
			port:     "5672",
			vhost:    "production",
			expected: "amqp://admin:p%40ss%3Aword%2F123@localhost:5672/production",
		},
		{
			name:     "username with special characters",
			protocol: "amqp",
			user:     "admin@domain:user",
			pass:     "secret",
			host:     "localhost",
			port:     "5672",
			vhost:    "production",
			expected: "amqp://admin%40domain%3Auser:secret@localhost:5672/production",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildRabbitMQConnectionString(tt.protocol, tt.user, tt.pass, tt.host, tt.port, tt.vhost)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestEnsureChannelWithContext_ReturnsErrorOnCancelledContext verifies that
// EnsureChannelWithContext respects context cancellation.
func TestEnsureChannelWithContext_ReturnsErrorOnCancelledContext(t *testing.T) {
	logger := &log.GoLogger{Level: log.InfoLevel}

	conn := &RabbitMQConnection{
		ConnectionStringSource: "amqp://guest:guest@localhost:5999", // Unreachable
		Logger:                 logger,
	}

	// Create already cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := conn.EnsureChannelWithContext(ctx)

	// Should return context.Canceled error
	assert.ErrorIs(t, err, context.Canceled)
}

func TestEnsureChannelWithContext_ReturnsErrorOnDeadlineExceeded(t *testing.T) {
	logger := &log.GoLogger{Level: log.InfoLevel}

	conn := &RabbitMQConnection{
		ConnectionStringSource: "amqp://guest:guest@localhost:5999", // Unreachable
		Logger:                 logger,
	}

	// Create context with very short deadline that's already expired
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(10 * time.Millisecond) // Let deadline expire

	err := conn.EnsureChannelWithContext(ctx)

	// Should return context.DeadlineExceeded error
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestEnsureChannelWithContext_TimeoutDuringDial(t *testing.T) {
	logger := &log.GoLogger{Level: log.InfoLevel}

	conn := &RabbitMQConnection{
		// Use a non-routable IP to ensure connection hangs (doesn't immediately fail)
		ConnectionStringSource: "amqp://guest:guest@10.255.255.1:5672",
		Logger:                 logger,
	}

	// Use short timeout - this should NOT take 30 seconds
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := conn.EnsureChannelWithContext(ctx)
	elapsed := time.Since(start)

	// Should fail with context deadline exceeded or i/o timeout
	assert.Error(t, err)

	// Should complete within reasonable time (not 30 seconds)
	assert.Less(t, elapsed, 500*time.Millisecond,
		"EnsureChannelWithContext should respect context timeout, took %v", elapsed)
}

func TestEnsureChannelWithContext_UsesConnectionTimeoutField(t *testing.T) {
	logger := &log.GoLogger{Level: log.InfoLevel}

	conn := &RabbitMQConnection{
		// Use non-routable IP to ensure connection hangs
		ConnectionStringSource: "amqp://guest:guest@10.255.255.1:5672",
		Logger:                 logger,
		ConnectionTimeout:      50 * time.Millisecond, // Short custom timeout
	}

	// Use context without deadline - should use ConnectionTimeout field
	ctx := context.Background()

	start := time.Now()
	err := conn.EnsureChannelWithContext(ctx)
	elapsed := time.Since(start)

	// Should fail with connection error
	assert.Error(t, err)

	// Should complete around ConnectionTimeout duration (with some buffer)
	assert.Less(t, elapsed, 200*time.Millisecond,
		"Should respect ConnectionTimeout field, took %v", elapsed)
	assert.Greater(t, elapsed, 40*time.Millisecond,
		"Should take at least ConnectionTimeout duration, took %v", elapsed)
}

func TestEnsureChannelWithContext_ChecksContextAfterLockAcquisition(t *testing.T) {
	logger := &log.GoLogger{Level: log.InfoLevel}

	conn := &RabbitMQConnection{
		// Use non-routable IP so connection hangs until context is cancelled
		ConnectionStringSource: "amqp://guest:guest@10.255.255.1:5672",
		Logger:                 logger,
	}

	// Create context that we'll cancel after a short delay
	ctx, cancel := context.WithCancel(context.Background())

	// Start goroutine that cancels context after a tiny delay
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	// Call should detect cancellation and return quickly
	start := time.Now()
	err := conn.EnsureChannelWithContext(ctx)
	elapsed := time.Since(start)

	// Should return an error (context.Canceled or connection error)
	assert.Error(t, err)

	// Should complete quickly due to context cancellation (not 30 seconds)
	assert.Less(t, elapsed, 200*time.Millisecond,
		"Should detect context cancellation quickly, took %v", elapsed)
}

// TestEnsureChannelWithContext_ChecksContextBeforeChannelCreation verifies that
// context is checked before calling Channel() when connection already exists.
// This test requires a real RabbitMQ connection to fully exercise the code path
// where connection exists but channel needs to be created.
func TestEnsureChannelWithContext_ChecksContextBeforeChannelCreation(t *testing.T) {
	t.Run("context_canceled_before_channel_with_nil_connection", func(t *testing.T) {
		// This test verifies that a pre-canceled context returns immediately
		// even when the connection would need to be established first.
		// The context check before Channel() provides defense-in-depth for cases
		// where an existing connection is reused but context was canceled.
		logger := &log.GoLogger{Level: log.InfoLevel}

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5672",
			Logger:                 logger,
		}

		// Pre-cancel context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := conn.EnsureChannelWithContext(ctx)

		// Should return context.Canceled from the first check (before lock)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("integration_test_with_real_connection", func(t *testing.T) {
		// Skip in unit tests - this would require a real RabbitMQ instance
		// to establish a connection, then cancel context before Channel() call.
		//
		// To fully test the context check before Channel():
		// 1. Establish a real connection to RabbitMQ
		// 2. Set rc.Connection to the valid connection
		// 3. Ensure rc.Channel is nil (needs channel creation)
		// 4. Cancel context
		// 5. Call EnsureChannelWithContext
		// 6. Verify it returns context.Canceled without calling Channel()
		t.Skip("Requires integration testing with a real RabbitMQ instance")
	})
}
