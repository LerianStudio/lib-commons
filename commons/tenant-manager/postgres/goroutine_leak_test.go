package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	libPostgres "github.com/LerianStudio/lib-commons/v3/commons/postgres"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/internal/testutil"
	"github.com/bxcodec/dbresolver/v2"
	"go.uber.org/goleak"
)

// TestManager_Close_WaitsForRevalidateSettings proves that Close() waits for
// active revalidatePoolSettings goroutines to finish before returning. Without the
// WaitGroup fix, Close() would return immediately while the goroutine is still
// running, causing a goroutine leak.
func TestManager_Close_WaitsForRevalidateSettings(t *testing.T) {
	logger := testutil.NewMockLogger()

	// Create a slow HTTP server that simulates a Tenant Manager responding
	// after a delay. The revalidatePoolSettings goroutine will block on this.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)

		config := core.TenantConfig{
			ID:            "tenant-slow",
			TenantSlug:    "slow-tenant",
			Service:       "test-service",
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
			ConnectionSettings: &core.ConnectionSettings{
				MaxOpenConns: 20,
				MaxIdleConns: 5,
			},
		}

		w.Header().Set("Content-Type", "application/json")

		if err := json.NewEncoder(w).Encode(config); err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	tmClient := client.NewClient(server.URL, logger)

	manager := NewManager(tmClient, "test-service",
		WithLogger(logger),
		WithSettingsCheckInterval(1*time.Millisecond), // Trigger revalidation immediately
	)

	// Pre-populate the connections map with a dummy connection so GetConnection
	// returns from cache and triggers the revalidation goroutine.
	dummyDB := &pingableDB{pingErr: nil}
	var db dbresolver.DB = dummyDB

	manager.connections["tenant-slow"] = &libPostgres.PostgresConnection{
		ConnectionDB: &db,
	}
	manager.lastAccessed["tenant-slow"] = time.Now()
	// Set lastSettingsCheck to zero time so revalidation is triggered immediately
	manager.lastSettingsCheck["tenant-slow"] = time.Time{}

	// GetConnection will hit cache, see that settingsCheckInterval has elapsed,
	// and spawn a revalidatePoolSettings goroutine that blocks for 500ms on the server.
	_, err := manager.GetConnection(context.Background(), "tenant-slow")
	if err != nil {
		t.Fatalf("GetConnection() returned unexpected error: %v", err)
	}

	// Close immediately — the revalidation goroutine is still blocked on the
	// slow HTTP server. With the fix, Close() waits for it to finish.
	if closeErr := manager.Close(context.Background()); closeErr != nil {
		t.Fatalf("Close() returned unexpected error: %v", closeErr)
	}

	// Close the Tenant Manager client to stop the InMemoryCache cleanup goroutine.
	if closeErr := tmClient.Close(); closeErr != nil {
		t.Fatalf("tmClient.Close() returned unexpected error: %v", closeErr)
	}

	// If Close() properly waited, no goroutines should be leaked.
	goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
	)
}
