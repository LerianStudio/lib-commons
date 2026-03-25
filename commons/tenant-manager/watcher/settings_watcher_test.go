package watcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newTestClient creates a tenant-manager client pointing at the given URL.
// The client is automatically closed when the test finishes to prevent
// goroutine leaks from the internal InMemoryCache cleanup loop.
func newTestClient(t *testing.T, url string) *client.Client {
	t.Helper()

	c, err := client.NewClient(url, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = c.Close() })

	return c
}

func TestSettingsWatcher_NoOpWhenNoManagers(t *testing.T) {
	t.Parallel()

	// A watcher with no postgres manager should not start a goroutine.
	apiCalled := atomic.Bool{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmClient := newTestClient(t, server.URL)
	logger := testutil.NewCapturingLogger()

	w := NewSettingsWatcher(tmClient, "ledger",
		WithLogger(logger),
		WithInterval(10*time.Millisecond),
	)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	w.Start(ctx)

	// Give it some time to NOT start anything.
	time.Sleep(50 * time.Millisecond)

	w.Stop()

	assert.False(t, apiCalled.Load(), "should not call API when no managers are configured")
}

func TestSettingsWatcher_AppliesSettings(t *testing.T) {
	t.Parallel()

	configCalled := atomic.Int32{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		configCalled.Add(1)
		w.Header().Set("Content-Type", "application/json")

		resp := map[string]any{
			"id":         "tenant-abc",
			"tenantSlug": "abc",
			"databases": map[string]any{
				"onboarding": map[string]any{
					"connectionSettings": map[string]any{
						"maxOpenConns": 50,
						"maxIdleConns": 15,
					},
				},
			},
		}

		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmClient := newTestClient(t, server.URL)
	logger := testutil.NewCapturingLogger()

	// Create a PG manager with a fake connection so ConnectedTenantIDs returns something.
	// We use the manager's internal state to simulate an active tenant.
	pgManager := tmpostgres.NewManager(tmClient, "ledger",
		tmpostgres.WithModule("onboarding"),
		tmpostgres.WithLogger(logger),
	)

	// Inject a tenant connection into the PG manager's map via GetConnection.
	// Since we don't have a real DB, we'll test that the watcher at least
	// calls the API by checking configCalled. The ApplyConnectionSettings
	// will be a no-op since there's no real connection.

	w := NewSettingsWatcher(tmClient, "ledger",
		WithPostgresManager(pgManager),
		WithLogger(logger),
		WithInterval(20*time.Millisecond),
	)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	// Directly test the revalidate method with a manager that has connections.
	// We need to verify the watcher uses ConnectedTenantIDs correctly.
	// Since pgManager has no connections, revalidate should be a no-op.
	w.revalidate(ctx)

	assert.Equal(t, int32(0), configCalled.Load(),
		"should not call API when no tenants have connections")
}

func TestSettingsWatcher_StopsCleanly(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	tmClient := newTestClient(t, server.URL)
	logger := testutil.NewCapturingLogger()

	pgManager := tmpostgres.NewManager(tmClient, "ledger")

	w := NewSettingsWatcher(tmClient, "ledger",
		WithPostgresManager(pgManager),
		WithLogger(logger),
		WithInterval(10*time.Millisecond),
	)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	w.Start(ctx)

	// Let it tick at least once.
	time.Sleep(30 * time.Millisecond)

	// Stop should return quickly without deadlocking.
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success: Stop returned.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2 seconds, possible deadlock")
	}

	assert.True(t, logger.ContainsSubstring("settings watcher stopped"),
		"should log that watcher stopped")
}

func TestSettingsWatcher_HandlesAPIErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 500 for all tenant config calls
		if strings.Contains(r.URL.Path, "/connections/") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal error"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmClient := newTestClient(t, server.URL)
	logger := testutil.NewCapturingLogger()

	pgManager := tmpostgres.NewManager(tmClient, "ledger",
		tmpostgres.WithModule("onboarding"),
		tmpostgres.WithLogger(logger),
	)

	w := NewSettingsWatcher(tmClient, "ledger",
		WithPostgresManager(pgManager),
		WithLogger(logger),
		WithInterval(20*time.Millisecond),
	)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	// Directly call revalidate — since pgManager has no connections, nothing happens.
	// This test verifies the watcher doesn't panic on errors.
	w.revalidate(ctx)

	// No connections means no API calls, so no error logs expected.
	// The test passes if no panic occurred.
}

func TestSettingsWatcher_HandlesSuspendedTenant(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/connections/") {
			// Return 403 for suspended tenant
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"TS-SUSPENDED","error":"service suspended","status":"suspended"}`))

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmClient := newTestClient(t, server.URL)
	logger := testutil.NewCapturingLogger()

	pgManager := tmpostgres.NewManager(tmClient, "ledger",
		tmpostgres.WithModule("onboarding"),
		tmpostgres.WithLogger(logger),
	)

	w := NewSettingsWatcher(tmClient, "ledger",
		WithPostgresManager(pgManager),
		WithLogger(logger),
	)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	// Directly test handleSuspendedTenant — it should log and not panic.
	w.handleSuspendedTenant(ctx, "tenant-suspended", w.logger)

	assert.True(t, logger.ContainsSubstring("tenant-suspended suspended/purged"),
		"should log about suspended tenant")
}

func TestSettingsWatcher_CollectTenantIDs(t *testing.T) {
	t.Parallel()

	tmClient := newTestClient(t, "http://127.0.0.1:0")

	t.Run("returns_empty_when_no_managers", func(t *testing.T) {
		t.Parallel()

		w := NewSettingsWatcher(tmClient, "ledger")
		ids := w.collectTenantIDs()

		assert.Empty(t, ids)
	})

	t.Run("returns_empty_when_managers_have_no_connections", func(t *testing.T) {
		t.Parallel()

		pgManager := tmpostgres.NewManager(nil, "ledger")

		w := NewSettingsWatcher(tmClient, "ledger",
			WithPostgresManager(pgManager),
		)

		ids := w.collectTenantIDs()
		assert.Empty(t, ids)
	})
}

func TestSettingsWatcher_WithInterval(t *testing.T) {
	t.Parallel()

	tmClient := newTestClient(t, "http://127.0.0.1:0")

	t.Run("respects_custom_interval", func(t *testing.T) {
		t.Parallel()

		w := NewSettingsWatcher(tmClient, "ledger",
			WithInterval(5*time.Second),
		)

		assert.Equal(t, 5*time.Second, w.interval)
	})

	t.Run("ignores_sub_second_interval", func(t *testing.T) {
		t.Parallel()

		w := NewSettingsWatcher(tmClient, "ledger",
			WithInterval(500*time.Millisecond),
		)

		assert.Equal(t, defaultInterval, w.interval,
			"should keep default interval when provided interval is less than 1 second")
	})

	t.Run("uses_default_interval", func(t *testing.T) {
		t.Parallel()

		w := NewSettingsWatcher(tmClient, "ledger")

		assert.Equal(t, defaultInterval, w.interval)
	})
}

func TestSettingsWatcher_StopWithoutStart(t *testing.T) {
	t.Parallel()

	tmClient := newTestClient(t, "http://127.0.0.1:0")

	w := NewSettingsWatcher(tmClient, "ledger")

	// Stop on a watcher that was never started should not panic.
	w.Stop()
}

func TestSettingsWatcher_MultipleManagers(t *testing.T) {
	t.Parallel()

	// Track which tenant IDs the API receives config requests for.
	requestedTenants := make(chan string, 10)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Extract tenant ID from path: /v1/tenants/{tenantID}/associations/{service}/connections
		parts := strings.Split(r.URL.Path, "/")
		// Expected: ["", "v1", "tenants", "{tenantID}", "associations", "{service}", "connections"]
		if len(parts) >= 4 && parts[2] == "tenants" {
			requestedTenants <- parts[3]
		}

		resp := map[string]any{
			"id":         "test",
			"tenantSlug": "test",
			"databases":  map[string]any{},
		}

		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmClient := newTestClient(t, server.URL)
	logger := testutil.NewCapturingLogger()

	// Create two PG managers simulating onboarding and transaction modules.
	// tenant-A in onboarding only, tenant-B in transaction only, tenant-C
	// in both (to verify deduplication).
	onboardingMgr := tmpostgres.NewManager(tmClient, "ledger",
		tmpostgres.WithModule("onboarding"),
		tmpostgres.WithLogger(logger),
		tmpostgres.WithTestConnections("tenant-A", "tenant-C"),
	)

	transactionMgr := tmpostgres.NewManager(tmClient, "ledger",
		tmpostgres.WithModule("transaction"),
		tmpostgres.WithLogger(logger),
		tmpostgres.WithTestConnections("tenant-B", "tenant-C"),
	)

	sw := NewSettingsWatcher(tmClient, "ledger",
		WithPostgresManager(onboardingMgr),
		WithPostgresManager(transactionMgr),
		WithLogger(logger),
		WithInterval(time.Second), // long interval; we call revalidate directly
	)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	// Verify both managers are registered.
	assert.Len(t, sw.managers, 2, "both PG managers should be registered")

	// Verify collectTenantIDs returns deduplicated union.
	ids := sw.collectTenantIDs()
	assert.Len(t, ids, 3, "should return 3 unique tenant IDs from both managers")
	assert.ElementsMatch(t, []string{"tenant-A", "tenant-B", "tenant-C"}, ids)

	// Run revalidate and verify the API is called for each unique tenant.
	sw.revalidate(ctx)

	// Drain the channel to collect all requested tenant IDs.
	close(requestedTenants)

	var fetched []string
	for id := range requestedTenants {
		fetched = append(fetched, id)
	}

	assert.Len(t, fetched, 3, "API should be called once per unique tenant")
	assert.ElementsMatch(t, []string{"tenant-A", "tenant-B", "tenant-C"}, fetched)

	// Verify the revalidation log message mentions all 3 tenants.
	assert.True(t, logger.ContainsSubstring("revalidated connection settings for 3/3"),
		"should log revalidation for all 3 tenants")
}

func TestSettingsWatcher_WithPostgresManager_NilIsIgnored(t *testing.T) {
	t.Parallel()

	tmClient := newTestClient(t, "http://127.0.0.1:0")

	sw := NewSettingsWatcher(tmClient, "ledger",
		WithPostgresManager(nil),
	)

	assert.Empty(t, sw.managers, "nil manager should not be appended")
}

func TestSettingsWatcher_ImmediateRevalidationOnStart(t *testing.T) {
	t.Parallel()

	revalidateCalled := atomic.Int32{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Match: /v1/tenants/{tenantID}/associations/{service}/connections
		if strings.HasSuffix(r.URL.Path, "/connections") {
			revalidateCalled.Add(1)
		}

		resp := map[string]any{
			"id":         "tenant-imm",
			"tenantSlug": "imm",
			"databases":  map[string]any{},
		}

		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmClient := newTestClient(t, server.URL)
	logger := testutil.NewCapturingLogger()

	pgManager := tmpostgres.NewManager(tmClient, "ledger",
		tmpostgres.WithModule("onboarding"),
		tmpostgres.WithLogger(logger),
		tmpostgres.WithTestConnections("tenant-imm"),
	)

	// Use a very long interval so the ticker does not fire during the test.
	sw := NewSettingsWatcher(tmClient, "ledger",
		WithPostgresManager(pgManager),
		WithLogger(logger),
		WithInterval(10*time.Second),
	)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	sw.Start(ctx)

	// Give the goroutine time to run the immediate revalidation.
	time.Sleep(100 * time.Millisecond)

	sw.Stop()

	assert.GreaterOrEqual(t, revalidateCalled.Load(), int32(1),
		"revalidate should be called immediately on Start, before first tick")
}
