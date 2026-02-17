package tenantmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			json.NewEncoder(w).Encode(config)
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
}
