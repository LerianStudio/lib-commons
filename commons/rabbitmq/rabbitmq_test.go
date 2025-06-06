package rabbitmq

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/stretchr/testify/assert"
)

// Mock for amqp.Channel
type mockAMQPChannel struct{}

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
				Host:   "localhost",
				User:   "user",
				Pass:   "pass",
				Logger: logger,
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
				serverURL := server.URL[7:] // Remove http://
				hostParts := strings.SplitN(serverURL, ":", 2)
				conn.Host = hostParts[0]
				if len(hostParts) > 1 {
					conn.Port = hostParts[1]
				}

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
	serverURL := server.URL[7:] // Remove http://
	hostParts := strings.SplitN(serverURL, ":", 2)
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
		Host:   host,
		Port:   port,
		User:   "correct",
		Pass:   "correct",
		Logger: logger,
	}

	isHealthy = goodAuthConn.HealthCheck()
	assert.True(t, isHealthy, "HealthCheck should return true with valid credentials")
}
