package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/rabbitmq"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NewMultiTenantConsumer is a test convenience wrapper around NewMultiTenantConsumerWithError
// that panics on error. This keeps test code concise while preserving the v4 constructor signature.
func NewMultiTenantConsumer(
	rabbitmq *tmrabbitmq.Manager,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) *MultiTenantConsumer {
	c, err := NewMultiTenantConsumerWithError(rabbitmq, config, logger, opts...)
	if err != nil {
		panic(fmt.Sprintf("NewMultiTenantConsumer (test helper): %v", err))
	}

	return c
}

// mustNewConsumer is an alternative test helper that takes *testing.T and calls t.Fatal on error.
func mustNewConsumer(
	t *testing.T,
	rabbitmq *tmrabbitmq.Manager,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) *MultiTenantConsumer {
	t.Helper()

	c, err := NewMultiTenantConsumerWithError(rabbitmq, config, logger, opts...)
	if err != nil {
		t.Fatalf("mustNewConsumer: %v", err)
	}

	return c
}

// generateTenantIDs creates a slice of N tenant IDs for testing.
func generateTenantIDs(n int) []string {
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("tenant-%04d", i)
	}

	return ids
}

// dummyRabbitMQManager returns a minimal non-nil *tmrabbitmq.Manager for tests that
// do not exercise RabbitMQ connections. Required because NewMultiTenantConsumer
// validates that rabbitmq is non-nil. A dummy Client is attached so that
// consumer goroutines spawned by ensureConsumerStarted do not panic on nil
// dereference; they will receive connection errors instead.
func dummyRabbitMQManager() *tmrabbitmq.Manager {
	dummyClient, err := client.NewClient("http://127.0.0.1:0", testutil.NewMockLogger(), client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	if err != nil {
		panic(fmt.Sprintf("dummyRabbitMQManager: failed to create client: %v", err))
	}

	return tmrabbitmq.NewManager(dummyClient, "test-service")
}

// setupTenantManagerAPIServer creates an httptest server that returns active tenants.
func setupTenantManagerAPIServer(t *testing.T, tenants []*client.TenantSummary) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(tenants); err != nil {
			t.Errorf("failed to encode tenant response: %v", err)
		}
	}))

	t.Cleanup(func() {
		server.Close()
	})

	return server
}

// makeTenantSummaries generates N TenantSummary entries for testing.
func makeTenantSummaries(n int) []*client.TenantSummary {
	tenants := make([]*client.TenantSummary, n)
	for i := range n {
		tenants[i] = &client.TenantSummary{
			ID:     fmt.Sprintf("tenant-%04d", i),
			Name:   fmt.Sprintf("Tenant %d", i),
			Status: "active",
		}
	}
	return tenants
}

// testServiceName is the service name used by most tests.
const testServiceName = "test-service"

// maxRunDuration is the maximum time Run() is allowed to take.
// The requirement specifies <1 second. We use 1 second as the hard deadline.
const maxRunDuration = 1 * time.Second

// newTestConfig creates a MultiTenantConfig pointing at the given API server URL.
func newTestConfig(apiURL string) MultiTenantConfig {
	return MultiTenantConfig{
		SyncInterval:      30 * time.Second,
		PrefetchCount:     10,
		Service:           testServiceName,
		MultiTenantURL:    apiURL,
		ServiceAPIKey:     "test-key",
		AllowInsecureHTTP: true,
	}
}

// TestMultiTenantConsumer_Run_EagerMode validates that Run() completes within 1 second,
// returns nil error (soft failure), and populates knownTenants.
func TestMultiTenantConsumer_Run_EagerMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		apiTenants               []*client.TenantSummary
		apiServerDown            bool
		expectedKnownTenantCount int
		expectError              bool
		expectConsumersStarted   bool
	}{
		{
			name:                     "returns_within_1s_with_0_tenants_configured",
			apiTenants:               []*client.TenantSummary{},
			expectedKnownTenantCount: 0,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		{
			name:                     "returns_within_1s_with_100_tenants_from_API",
			apiTenants:               makeTenantSummaries(100),
			expectedKnownTenantCount: 100,
			expectError:              false,
			expectConsumersStarted:   true,
		},
		{
			name:                     "returns_within_1s_with_500_tenants_from_API",
			apiTenants:               makeTenantSummaries(500),
			expectedKnownTenantCount: 500,
			expectError:              false,
			expectConsumersStarted:   true,
		},
		{
			name:                     "returns_nil_error_when_API_is_down",
			apiServerDown:            true,
			expectedKnownTenantCount: 0,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		// Edge case: single tenant
		{
			name:                     "returns_within_1s_with_1_tenant_from_API",
			apiTenants:               makeTenantSummaries(1),
			expectedKnownTenantCount: 1,
			expectError:              false,
			expectConsumersStarted:   true,
		},
		// Edge case: 3 tenants
		{
			name:                     "returns_within_1s_with_3_tenants_from_API",
			apiTenants:               makeTenantSummaries(3),
			expectedKnownTenantCount: 3,
			expectError:              false,
			expectConsumersStarted:   true,
		},
	}

	for _, tt := range tests {
		tt := tt // capture loop variable for parallel subtests
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var apiURL string
			if !tt.apiServerDown {
				server := setupTenantManagerAPIServer(t, tt.apiTenants)
				apiURL = server.URL
			} else {
				apiURL = "http://127.0.0.1:0" // unreachable port
			}

			config := newTestConfig(apiURL)
			mockLogger := testutil.NewMockLogger()
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, mockLogger)

			// Register a handler
			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Measure execution time of Run()
			start := time.Now()
			err := consumer.Run(ctx)
			elapsed := time.Since(start)

			// ASSERTION 1: Run() completes within maxRunDuration
			assert.Less(t, elapsed, maxRunDuration,
				"Run() must complete within %s, took %s", maxRunDuration, elapsed)

			// ASSERTION 2: Run() returns nil error (even on discovery failure)
			if !tt.expectError {
				assert.NoError(t, err,
					"Run() must return nil error (soft failure on discovery)")
			}

			// ASSERTION 3: knownTenants is populated
			consumer.mu.RLock()
			knownCount := len(consumer.knownTenants)
			consumersStarted := len(consumer.tenants)
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedKnownTenantCount, knownCount,
				"knownTenants should have %d entries after Run(), got %d",
				tt.expectedKnownTenantCount, knownCount)

			// ASSERTION 4: Consumers started for discovered tenants (eager mode)
			if tt.expectConsumersStarted {
				assert.Greater(t, consumersStarted, 0,
					"consumers should be started eagerly for discovered tenants")
			} else {
				assert.Equal(t, 0, consumersStarted,
					"no consumers should be started when no tenants discovered")
			}

			// Cleanup
			cancel()
			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_Run_SignatureUnchanged verifies the Run() method signature
// matches the expected interface: func (c *MultiTenantConsumer) Run(ctx context.Context) error
// This is a compile-time assertion. If the signature changes, this test will not compile.
// Covers: AC-T1
func TestMultiTenantConsumer_Run_SignatureUnchanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "Run_accepts_context_and_returns_error"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Compile-time signature assertion: Run must accept context.Context and return error.
			// If the signature changes, this assignment will fail to compile.
			var fn func(ctx context.Context) error

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			fn = consumer.Run
			assert.NotNil(t, fn, "Run method must exist and match expected signature")
		})
	}
}

// TestMultiTenantConsumer_DiscoverTenants_ReuseFetchTenantIDs verifies that
// discoverTenants() delegates to fetchTenantIDs() internally by confirming that
// tenant IDs sourced from API (via fetchTenantIDs) end up in knownTenants.
// Covers: AC-T2
func TestMultiTenantConsumer_DiscoverTenants_ReuseFetchTenantIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		apiTenants    []*client.TenantSummary
		expectedCount int
	}{
		{
			name: "discovers_tenants_from_API_via_fetchTenantIDs",
			apiTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
				{ID: "tenant-b", Name: "B", Status: "active"},
				{ID: "tenant-c", Name: "C", Status: "active"},
			},
			expectedCount: 3,
		},
		{
			name:          "discovers_zero_tenants_when_API_returns_empty",
			apiTenants:    []*client.TenantSummary{},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, tt.apiTenants)
			config := newTestConfig(server.URL)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

			ctx := context.Background()

			// Call discoverTenants which internally uses fetchTenantIDs
			consumer.discoverTenants(ctx)

			consumer.mu.RLock()
			knownCount := len(consumer.knownTenants)
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedCount, knownCount,
				"discoverTenants should populate knownTenants via fetchTenantIDs")

			// Verify each tenant ID is present in knownTenants
			consumer.mu.RLock()
			for _, ts := range tt.apiTenants {
				assert.True(t, consumer.knownTenants[ts.ID],
					"tenant %q should be in knownTenants after discovery", ts.ID)
			}
			consumer.mu.RUnlock()
		})
	}
}

// TestMultiTenantConsumer_Run_StartupLog verifies that Run() produces a log message
// containing "connection_mode=eager" during startup.
func TestMultiTenantConsumer_Run_StartupLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		expectedLogPart string
	}{
		{
			name:            "startup_log_contains_connection_mode_eager",
			expectedLogPart: "connection_mode=eager",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			config := newTestConfig(server.URL)
			logger := testutil.NewCapturingLogger()
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger)

			// Set the capturing logger in context so NewTrackingFromContext returns it
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = libCommons.ContextWithLogger(ctx, logger)

			err := consumer.Run(ctx)
			assert.NoError(t, err, "Run() should return nil")

			// Verify the startup log contains connection_mode=eager
			assert.True(t, logger.ContainsSubstring(tt.expectedLogPart),
				"startup log must contain %q, got messages: %v",
				tt.expectedLogPart, logger.GetMessages())

			cancel()
			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_Run_BackgroundSyncStarts verifies that syncActiveTenants
// is started in the background after Run() returns.
// Covers: AC-T4
func TestMultiTenantConsumer_Run_BackgroundSyncStarts(t *testing.T) {
	// Not parallel: relies on timing (time.Sleep) for sync loop detection
	tests := []struct {
		name          string
		syncInterval  time.Duration
		tenantToAdd   string
		expectedCount int
	}{
		{
			name:          "sync_loop_discovers_tenants_added_after_Run",
			syncInterval:  100 * time.Millisecond,
			tenantToAdd:   "new-tenant-001",
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// Start with empty API response, then switch to include the new tenant
			var mu sync.Mutex
			currentTenants := []*client.TenantSummary{}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				tenants := currentTenants
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tenants)
			}))
			t.Cleanup(server.Close)

			config := newTestConfig(server.URL)
			config.SyncInterval = tt.syncInterval

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Run() should return quickly
			err := consumer.Run(ctx)
			require.NoError(t, err, "Run() should succeed")

			// After Run, update API to return the new tenant - the sync loop should pick it up
			mu.Lock()
			currentTenants = []*client.TenantSummary{
				{ID: tt.tenantToAdd, Name: "New Tenant", Status: "active"},
			}
			mu.Unlock()

			// Wait for at least one sync cycle to complete
			time.Sleep(3 * tt.syncInterval)

			// The background sync loop should have discovered the new tenant
			consumer.mu.RLock()
			knownCount := len(consumer.knownTenants)
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedCount, knownCount,
				"background syncActiveTenants should discover tenants added after Run(), found %d", knownCount)

			cancel()
			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_Run_ReadinessWithinDeadline verifies that the service
// becomes ready (Run() returns) within 5 seconds across all tenant configurations.
// Covers: AC-O1
func TestMultiTenantConsumer_Run_ReadinessWithinDeadline(t *testing.T) {
	t.Parallel()

	const readinessDeadline = 5 * time.Second

	tests := []struct {
		name       string
		apiTenants []*client.TenantSummary
	}{
		{
			name:       "ready_within_5s_with_0_tenants",
			apiTenants: []*client.TenantSummary{},
		},
		{
			name:       "ready_within_5s_with_100_tenants",
			apiTenants: makeTenantSummaries(100),
		},
		{
			name:       "ready_within_5s_with_500_tenants_via_API",
			apiTenants: makeTenantSummaries(500),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, tt.apiTenants)
			config := newTestConfig(server.URL)
			mockLogger := testutil.NewMockLogger()
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, mockLogger)

			ctx, cancel := context.WithTimeout(context.Background(), readinessDeadline)
			defer cancel()

			start := time.Now()
			err := consumer.Run(ctx)
			elapsed := time.Since(start)

			assert.NoError(t, err, "Run() must not return error")
			assert.Less(t, elapsed, readinessDeadline,
				"Run() must complete within readiness deadline (%s), took %s", readinessDeadline, elapsed)

			cancel()
			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_Run_StartupTimeVariance verifies that startup time variance
// is <= 1 second across 0/100/500 tenant configurations.
// Covers: AC-O2
func TestMultiTenantConsumer_Run_StartupTimeVariance(t *testing.T) {
	// Not parallel: measures timing across sequential runs

	tests := []struct {
		name       string
		apiTenants []*client.TenantSummary
	}{
		{name: "0_tenants", apiTenants: []*client.TenantSummary{}},
		{name: "100_tenants", apiTenants: makeTenantSummaries(100)},
		{name: "500_tenants_via_API", apiTenants: makeTenantSummaries(500)},
	}

	var durations []time.Duration

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			server := setupTenantManagerAPIServer(t, tt.apiTenants)
			config := newTestConfig(server.URL)
			mockLogger := testutil.NewMockLogger()
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, mockLogger)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			start := time.Now()
			err := consumer.Run(ctx)
			elapsed := time.Since(start)

			assert.NoError(t, err, "Run() must not return error")
			durations = append(durations, elapsed)

			cancel()
			consumer.Close()
		})
	}

	// After all subtests run, verify variance
	if len(durations) >= 2 {
		var minDuration, maxDuration time.Duration
		minDuration = durations[0]
		maxDuration = durations[0]

		for _, d := range durations[1:] {
			if d < minDuration {
				minDuration = d
			}
			if d > maxDuration {
				maxDuration = d
			}
		}

		variance := maxDuration - minDuration
		assert.LessOrEqual(t, variance, 1*time.Second,
			"startup time variance must be <= 1s, got %s (min=%s, max=%s)",
			variance, minDuration, maxDuration)
	}
}

// TestMultiTenantConsumer_DiscoveryFailure_LogsWarning verifies that when tenant
// discovery fails, a warning is logged but Run() does not return an error.
// Covers: AC-O3 (explicit warning log verification)
func TestMultiTenantConsumer_DiscoveryFailure_LogsWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		apiDown         bool
		expectedLogPart string
	}{
		{
			name:            "logs_warning_when_API_fails",
			apiDown:         true,
			expectedLogPart: "tenant discovery failed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			apiURL := "http://127.0.0.1:0" // unreachable port
			config := newTestConfig(apiURL)
			logger := testutil.NewCapturingLogger()
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger)

			// Set the capturing logger in context so NewTrackingFromContext returns it
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = libCommons.ContextWithLogger(ctx, logger)

			err := consumer.Run(ctx)

			// Run() must return nil even when discovery fails
			assert.NoError(t, err, "Run() must return nil on discovery failure (soft failure)")

			// Warning log must contain discovery failure message
			assert.True(t, logger.ContainsSubstring(tt.expectedLogPart),
				"discovery failure must log warning containing %q, got: %v",
				tt.expectedLogPart, logger.GetMessages())

			cancel()
			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_DefaultMultiTenantConfig verifies DefaultMultiTenantConfig
// returns sensible defaults.
func TestMultiTenantConsumer_DefaultMultiTenantConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		expectedSync        time.Duration
		expectedWorkers     int
		expectedPrefetch    int
		expectedDiscoveryTO time.Duration
	}{
		{
			name:                "returns_default_values",
			expectedSync:        30 * time.Second,
			expectedWorkers:     0, // WorkersPerQueue is deprecated, default is 0
			expectedPrefetch:    10,
			expectedDiscoveryTO: 500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := DefaultMultiTenantConfig()

			assert.Equal(t, tt.expectedSync, config.SyncInterval,
				"default SyncInterval should be %s", tt.expectedSync)
			assert.Equal(t, tt.expectedWorkers, config.WorkersPerQueue,
				"default WorkersPerQueue should be %d", tt.expectedWorkers)
			assert.Equal(t, tt.expectedPrefetch, config.PrefetchCount,
				"default PrefetchCount should be %d", tt.expectedPrefetch)
			assert.Equal(t, tt.expectedDiscoveryTO, config.DiscoveryTimeout,
				"default DiscoveryTimeout should be %s", tt.expectedDiscoveryTO)
			assert.Empty(t, config.MultiTenantURL, "default MultiTenantURL should be empty")
			assert.Empty(t, config.Service, "default Service should be empty")
			assert.False(t, config.AllowInsecureHTTP, "default AllowInsecureHTTP should be false")
		})
	}
}

// TestMultiTenantConsumer_NewWithZeroConfig verifies that NewMultiTenantConsumerWithError
// validates required fields.
func TestMultiTenantConsumer_NewWithZeroConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		config           MultiTenantConfig
		expectedSync     time.Duration
		expectedPrefetch int
		expectPMClient   bool
		expectError      bool
		errContains      string
	}{
		{
			name:        "rejects_empty_MultiTenantURL",
			config:      MultiTenantConfig{},
			expectError: true,
			errContains: "MultiTenantURL must not be empty",
		},
		{
			name: "rejects_empty_Service",
			config: MultiTenantConfig{
				MultiTenantURL:    "http://tenant-manager:4003",
				AllowInsecureHTTP: true,
			},
			expectError: true,
			errContains: "Service must not be empty",
		},
		{
			name: "preserves_explicit_values",
			config: MultiTenantConfig{
				SyncInterval:      60 * time.Second,
				WorkersPerQueue:   5,
				PrefetchCount:     20,
				MultiTenantURL:    "http://tenant-manager:4003",
				ServiceAPIKey:     "test-key",
				Service:           "ledger",
				AllowInsecureHTTP: true,
			},
			expectedSync:     60 * time.Second,
			expectedPrefetch: 20,
			expectPMClient:   true,
		},
		{
			name: "creates_pmClient_when_URL_configured",
			config: MultiTenantConfig{
				MultiTenantURL:    "https://tenant-manager:4003",
				ServiceAPIKey:     "test-key",
				Service:           "ledger",
				AllowInsecureHTTP: false,
			},
			expectedSync:     30 * time.Second,
			expectedPrefetch: 10,
			expectPMClient:   true,
		},
		{
			name: "creates_pmClient_with_http_URL_when_AllowInsecureHTTP_set",
			config: MultiTenantConfig{
				MultiTenantURL:    "http://tenant-manager.namespace.svc.cluster.local:4003",
				ServiceAPIKey:     "test-key",
				Service:           "ledger",
				AllowInsecureHTTP: true,
			},
			expectedSync:     30 * time.Second,
			expectedPrefetch: 10,
			expectPMClient:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			consumer, err := NewMultiTenantConsumerWithError(dummyRabbitMQManager(), tt.config, testutil.NewMockLogger())

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, consumer)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, consumer, "consumer must not be nil")
			assert.Equal(t, tt.expectedSync, consumer.config.SyncInterval)
			assert.Equal(t, tt.expectedPrefetch, consumer.config.PrefetchCount)
			assert.NotNil(t, consumer.handlers, "handlers map must be initialized")
			assert.NotNil(t, consumer.tenants, "tenants map must be initialized")
			assert.NotNil(t, consumer.knownTenants, "knownTenants map must be initialized")

			if tt.expectPMClient {
				assert.NotNil(t, consumer.pmClient,
					"pmClient should be created when MultiTenantURL is configured")
			}

			_ = consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_Stats verifies the Stats() method returns correct statistics.
func TestMultiTenantConsumer_Stats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		registerQueues  []string
		expectClosed    bool
		closeBeforeStat bool
	}{
		{
			name:            "returns_stats_with_no_registered_queues",
			registerQueues:  nil,
			expectClosed:    false,
			closeBeforeStat: false,
		},
		{
			name:            "returns_stats_with_registered_queues",
			registerQueues:  []string{"queue-a", "queue-b"},
			expectClosed:    false,
			closeBeforeStat: false,
		},
		{
			name:            "returns_closed_true_after_Close",
			registerQueues:  []string{"queue-a"},
			expectClosed:    true,
			closeBeforeStat: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			for _, q := range tt.registerQueues {
				consumer.Register(q, func(ctx context.Context, delivery amqp.Delivery) error {
					return nil
				})
			}

			if tt.closeBeforeStat {
				consumer.Close()
			}

			stats := consumer.Stats()

			assert.Equal(t, 0, stats.ActiveTenants,
				"no tenants should be active (no Run() called)")
			assert.Equal(t, len(tt.registerQueues), len(stats.RegisteredQueues),
				"registered queues count should match")
			assert.Equal(t, tt.expectClosed, stats.Closed, "closed flag mismatch")
		})
	}
}

// TestMultiTenantConsumer_Close verifies the Close() method lifecycle behavior.
func TestMultiTenantConsumer_Close(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "close_marks_consumer_as_closed_and_clears_maps"},
		{name: "close_is_idempotent_on_double_call"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			// Pre-populate sync.Map entries to verify they are cleaned on Close
			consumer.consumerLocks.Store("tenant-x", &sync.Mutex{})
			consumer.consumerLocks.Store("tenant-y", &sync.Mutex{})
			consumer.retryState.Store("tenant-x", &retryStateEntry{})
			consumer.retryState.Store("tenant-y", &retryStateEntry{})

			// First close
			err := consumer.Close()
			assert.NoError(t, err, "Close() should not return error")

			consumer.mu.RLock()
			assert.True(t, consumer.closed, "consumer should be marked as closed")
			assert.Empty(t, consumer.tenants, "tenants map should be cleared after Close()")
			assert.Empty(t, consumer.knownTenants, "knownTenants map should be cleared after Close()")
			consumer.mu.RUnlock()

			// Note: sync.Map entries (consumerLocks, retryState) are NOT cleared by Close().
			// Close() clears regular maps (tenants, knownTenants) only.
			// sync.Map entries are cleaned lazily during syncTenants / eviction.

			if tt.name == "close_is_idempotent_on_double_call" {
				// Second close should not panic
				err2 := consumer.Close()
				assert.NoError(t, err2, "second Close() should not return error")
			}
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_RemovesTenants verifies that syncTenants()
// removes tenants that are no longer in the API response immediately (no grace period).
func TestMultiTenantConsumer_SyncTenants_RemovesTenants(t *testing.T) {
	// Not parallel: relies on internal state manipulation

	tests := []struct {
		name                   string
		initialTenants         []*client.TenantSummary
		postSyncTenants        []*client.TenantSummary
		expectedKnownAfterSync int
	}{
		{
			name:                   "removes_tenants_no_longer_in_API",
			initialTenants:         makeTenantSummaries(3),
			postSyncTenants:        makeTenantSummaries(1),
			expectedKnownAfterSync: 1,
		},
		{
			name:                   "handles_all_tenants_removed",
			initialTenants:         makeTenantSummaries(2),
			postSyncTenants:        []*client.TenantSummary{},
			expectedKnownAfterSync: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			currentTenants := tt.initialTenants

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				tenants := currentTenants
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tenants)
			}))
			t.Cleanup(server.Close)

			config := newTestConfig(server.URL)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())
			ctx := context.Background()

			// Initial discovery
			consumer.discoverTenants(ctx)

			consumer.mu.RLock()
			initialCount := len(consumer.knownTenants)
			consumer.mu.RUnlock()
			assert.Equal(t, len(tt.initialTenants), initialCount,
				"initial discovery should find all tenants")

			// Pre-populate active consumers for initial tenants
			consumer.mu.Lock()
			for _, ts := range tt.initialTenants {
				consumer.consumerLocks.Store(ts.ID, &sync.Mutex{})
				consumer.retryState.Store(ts.ID, &retryStateEntry{})
				_, cancel := context.WithCancel(ctx)
				consumer.tenants[ts.ID] = cancel
			}
			consumer.mu.Unlock()

			// Update API to reflect post-sync state
			mu.Lock()
			currentTenants = tt.postSyncTenants
			mu.Unlock()

			// Single sync removes immediately (no grace period)
			err := consumer.syncTenants(ctx)
			assert.NoError(t, err, "syncTenants should not return error")

			consumer.mu.RLock()
			afterSyncCount := len(consumer.knownTenants)
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedKnownAfterSync, afterSyncCount,
				"after single sync, knownTenants should reflect updated API response")
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_EagerMode verifies that syncTenants() populates
// knownTenants for new tenants AND starts consumer goroutines eagerly.
func TestMultiTenantConsumer_SyncTenants_EagerMode(t *testing.T) {
	tests := []struct {
		name               string
		initialAPITenants  []*client.TenantSummary
		newAPITenants      []*client.TenantSummary
		expectedKnownCount int
		expectConsumers    bool
	}{
		{
			name:               "new_tenants_added_and_consumers_started",
			initialAPITenants:  []*client.TenantSummary{},
			newAPITenants:      makeTenantSummaries(3),
			expectedKnownCount: 3,
			expectConsumers:    true,
		},
		{
			name:               "sync_discovers_tenants_and_starts_consumers",
			initialAPITenants:  []*client.TenantSummary{},
			newAPITenants:      makeTenantSummaries(10),
			expectedKnownCount: 10,
			expectConsumers:    true,
		},
		{
			name:               "sync_with_zero_tenants_starts_no_consumers",
			initialAPITenants:  []*client.TenantSummary{},
			newAPITenants:      []*client.TenantSummary{},
			expectedKnownCount: 0,
			expectConsumers:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			currentTenants := tt.initialAPITenants

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				tenants := currentTenants
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tenants)
			}))
			t.Cleanup(server.Close)

			config := newTestConfig(server.URL)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

			// Register a handler so startTenantConsumer has something to consume
			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			consumer.parentCtx = ctx

			// Initial discovery (populates knownTenants only)
			consumer.discoverTenants(ctx)

			// Update API with new tenants
			mu.Lock()
			currentTenants = tt.newAPITenants
			mu.Unlock()

			// Run syncTenants - should populate knownTenants and start consumers
			err := consumer.syncTenants(ctx)
			assert.NoError(t, err, "syncTenants should not return error")

			consumer.mu.RLock()
			knownCount := len(consumer.knownTenants)
			consumerCount := len(consumer.tenants)
			consumer.mu.RUnlock()

			// ASSERTION 1: knownTenants is populated with discovered tenants
			assert.Equal(t, tt.expectedKnownCount, knownCount,
				"syncTenants must populate knownTenants (expected %d, got %d)",
				tt.expectedKnownCount, knownCount)

			// ASSERTION 2: Consumers started for discovered tenants
			if tt.expectConsumers {
				assert.Greater(t, consumerCount, 0,
					"syncTenants should start consumers eagerly for discovered tenants")
			} else {
				assert.Equal(t, 0, consumerCount,
					"no consumers expected when no tenants discovered")
			}

			cancel()
			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_RemovalCleansKnownTenants verifies that when
// a tenant is removed from API, syncTenants() cleans it from knownTenants immediately.
// Covers: T-005 AC-F3, AC-F4
func TestMultiTenantConsumer_SyncTenants_RemovalCleansKnownTenants(t *testing.T) {
	tests := []struct {
		name                      string
		initialTenants            []*client.TenantSummary
		remainingTenants          []*client.TenantSummary
		expectedKnownAfterRemoval int
	}{
		{
			name: "removed_tenant_cleaned_from_knownTenants",
			initialTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
				{ID: "tenant-b", Name: "B", Status: "active"},
				{ID: "tenant-c", Name: "C", Status: "active"},
			},
			remainingTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
			},
			expectedKnownAfterRemoval: 1,
		},
		{
			name: "all_tenants_removed_cleans_knownTenants",
			initialTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
				{ID: "tenant-b", Name: "B", Status: "active"},
			},
			remainingTenants:          []*client.TenantSummary{},
			expectedKnownAfterRemoval: 0,
		},
		{
			name: "no_tenants_removed_keeps_all_in_knownTenants",
			initialTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
				{ID: "tenant-b", Name: "B", Status: "active"},
			},
			remainingTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
				{ID: "tenant-b", Name: "B", Status: "active"},
			},
			expectedKnownAfterRemoval: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			currentTenants := tt.initialTenants

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				tenants := currentTenants
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tenants)
			}))
			t.Cleanup(server.Close)

			config := newTestConfig(server.URL)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

			ctx := context.Background()

			// First sync to populate initial state
			err := consumer.syncTenants(ctx)
			require.NoError(t, err, "initial syncTenants should succeed")

			// Verify initial knownTenants count
			consumer.mu.RLock()
			initialKnown := len(consumer.knownTenants)
			consumer.mu.RUnlock()
			assert.Equal(t, len(tt.initialTenants), initialKnown,
				"initial sync should discover all tenants")

			// Update API to remaining tenants
			mu.Lock()
			currentTenants = tt.remainingTenants
			mu.Unlock()

			// Single sync removes immediately (no grace period, API is source of truth)
			err = consumer.syncTenants(ctx)
			require.NoError(t, err, "syncTenants should succeed")

			consumer.mu.RLock()
			afterRemovalKnown := len(consumer.knownTenants)
			// Verify removed tenants are NOT in knownTenants
			remainingSet := make(map[string]bool)
			for _, ts := range tt.remainingTenants {
				remainingSet[ts.ID] = true
			}
			for _, ts := range tt.initialTenants {
				if !remainingSet[ts.ID] {
					assert.False(t, consumer.knownTenants[ts.ID],
						"removed tenant %q must be cleaned from knownTenants immediately", ts.ID)
				}
			}
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedKnownAfterRemoval, afterRemovalKnown,
				"after sync, knownTenants should have %d entries, got %d",
				tt.expectedKnownAfterRemoval, afterRemovalKnown)
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_APIFailureKeepsCurrentState verifies that the
// sync keeps current state when the API fails (no removals on failure).
func TestMultiTenantConsumer_SyncTenants_APIFailureKeepsCurrentState(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "keeps_current_state_on_API_failure",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			callCount := 0
			var mu sync.Mutex

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				callCount++
				count := callCount
				mu.Unlock()

				if count == 1 {
					// First call: return tenants
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(makeTenantSummaries(3))
					return
				}
				// Subsequent calls: fail
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)

			config := newTestConfig(server.URL)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			// First sync succeeds
			err := consumer.syncTenants(ctx)
			assert.NoError(t, err, "first syncTenants should succeed")

			consumer.mu.RLock()
			knownAfterFirst := len(consumer.knownTenants)
			consumer.mu.RUnlock()
			assert.Equal(t, 3, knownAfterFirst, "first sync should populate 3 tenants")

			// Second sync fails but keeps current state
			err = consumer.syncTenants(ctx)
			assert.NoError(t, err, "syncTenants should return nil on API failure (keeps current state)")

			// Verify consumer still functional and tenants preserved
			consumer.mu.RLock()
			assert.False(t, consumer.closed, "consumer should not be closed after sync error")
			knownAfterError := len(consumer.knownTenants)
			consumer.mu.RUnlock()

			assert.Equal(t, 3, knownAfterError,
				"tenants should be preserved after API failure")

			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_ClosedConsumer verifies that syncTenants
// returns an error when the consumer is already closed.
func TestMultiTenantConsumer_SyncTenants_ClosedConsumer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		errContains string
	}{
		{
			name:        "returns_error_when_consumer_is_closed",
			errContains: "consumer is closed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, makeTenantSummaries(1))
			config := newTestConfig(server.URL)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

			// Close consumer first
			consumer.Close()

			// syncTenants should detect closed state
			err := consumer.syncTenants(context.Background())
			require.Error(t, err, "syncTenants must return error for closed consumer")
			assert.Contains(t, err.Error(), tt.errContains,
				"error message should indicate consumer is closed")
		})
	}
}

// TestMultiTenantConsumer_FetchTenantIDs verifies fetchTenantIDs behavior in isolation.
func TestMultiTenantConsumer_FetchTenantIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		apiTenants    []*client.TenantSummary
		apiDown       bool
		expectError   bool
		expectedCount int
	}{
		{
			name:          "returns_tenants_from_API",
			apiTenants:    makeTenantSummaries(3),
			expectedCount: 3,
		},
		{
			name:          "returns_empty_list_when_no_tenants",
			apiTenants:    []*client.TenantSummary{},
			expectedCount: 0,
		},
		{
			name:          "returns_tenants_from_API_2",
			apiTenants:    makeTenantSummaries(2),
			expectedCount: 2,
		},
		{
			name:        "returns_error_when_API_is_down",
			apiDown:     true,
			expectError: true,
		},
		{
			name:          "returns_4_tenants_from_API",
			apiTenants:    makeTenantSummaries(4),
			expectedCount: 4,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var apiURL string
			if !tt.apiDown {
				server := setupTenantManagerAPIServer(t, tt.apiTenants)
				apiURL = server.URL
			} else {
				apiURL = "http://127.0.0.1:0"
			}

			config := newTestConfig(apiURL)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

			ids, err := consumer.fetchTenantIDs(context.Background())

			if tt.expectError {
				assert.Error(t, err, "fetchTenantIDs should return error")
			} else {
				assert.NoError(t, err, "fetchTenantIDs should not return error")
				assert.Len(t, ids, tt.expectedCount,
					"expected %d tenant IDs, got %d", tt.expectedCount, len(ids))
			}
		})
	}
}

// TestMultiTenantConsumer_Register verifies handler registration.
func TestMultiTenantConsumer_Register(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		queueNames    []string
		expectedCount int
	}{
		{
			name:          "registers_single_queue_handler",
			queueNames:    []string{"queue-a"},
			expectedCount: 1,
		},
		{
			name:          "registers_multiple_queue_handlers",
			queueNames:    []string{"queue-a", "queue-b", "queue-c"},
			expectedCount: 3,
		},
		{
			name:          "overwrites_handler_for_same_queue",
			queueNames:    []string{"queue-a", "queue-a"},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			for _, q := range tt.queueNames {
				consumer.Register(q, func(ctx context.Context, delivery amqp.Delivery) error {
					return nil
				})
			}

			consumer.mu.RLock()
			handlerCount := len(consumer.handlers)
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedCount, handlerCount,
				"expected %d registered handlers, got %d", tt.expectedCount, handlerCount)
		})
	}
}

// TestMultiTenantConsumer_NilLogger verifies that NewMultiTenantConsumerWithError does not panic
// when a nil logger is provided and defaults to NoneLogger.
func TestMultiTenantConsumer_NilLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "nil_logger_does_not_panic_on_creation"},
		{name: "nil_logger_consumer_can_register_handler"},
		{name: "nil_logger_consumer_can_close"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)

			assert.NotPanics(t, func() {
				consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), nil) // nil logger

				assert.NotNil(t, consumer, "consumer must not be nil even with nil logger")

				if tt.name == "nil_logger_consumer_can_register_handler" {
					consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
						return nil
					})
				}

				if tt.name == "nil_logger_consumer_can_close" {
					err := consumer.Close()
					assert.NoError(t, err, "Close() should not panic with nil-guarded logger")
				}
			})
		})
	}
}

// TestIsValidTenantID verifies tenant ID validation logic.
func TestIsValidTenantID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
		expected bool
	}{
		{name: "valid_alphanumeric", tenantID: "tenant123", expected: true},
		{name: "valid_with_hyphens", tenantID: "tenant-123-abc", expected: true},
		{name: "valid_with_underscores", tenantID: "tenant_123_abc", expected: true},
		{name: "valid_uuid_format", tenantID: "550e8400-e29b-41d4-a716-446655440000", expected: true},
		{name: "valid_single_char", tenantID: "t", expected: true},
		{name: "invalid_empty", tenantID: "", expected: false},
		{name: "invalid_starts_with_hyphen", tenantID: "-tenant", expected: false},
		{name: "invalid_starts_with_underscore", tenantID: "_tenant", expected: false},
		{name: "invalid_contains_slash", tenantID: "tenant/../../etc", expected: false},
		{name: "invalid_contains_space", tenantID: "tenant 123", expected: false},
		{name: "invalid_contains_dots", tenantID: "tenant.123", expected: false},
		{name: "invalid_contains_special_chars", tenantID: "tenant@123!", expected: false},
		{name: "invalid_exceeds_max_length", tenantID: string(make([]byte, 257)), expected: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := core.IsValidTenantID(tt.tenantID)
			assert.Equal(t, tt.expected, result,
				"IsValidTenantID(%q) = %v, want %v", tt.tenantID, result, tt.expected)
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_FiltersInvalidIDs verifies that syncTenants
// skips tenant IDs that fail validation.
func TestMultiTenantConsumer_SyncTenants_FiltersInvalidIDs(t *testing.T) {
	tests := []struct {
		name             string
		apiTenants       []*client.TenantSummary
		expectedKnownIDs int
	}{
		{
			name: "filters_out_path_traversal_attempts",
			apiTenants: []*client.TenantSummary{
				{ID: "valid-tenant", Name: "Valid", Status: "active"},
				{ID: "../../etc/passwd", Name: "Invalid", Status: "active"},
				{ID: "also-valid", Name: "Also Valid", Status: "active"},
			},
			expectedKnownIDs: 2,
		},
		{
			name: "filters_out_empty_strings",
			apiTenants: []*client.TenantSummary{
				{ID: "valid-tenant", Name: "Valid", Status: "active"},
				{ID: "", Name: "Empty", Status: "active"},
				{ID: "another-valid", Name: "Another", Status: "active"},
			},
			expectedKnownIDs: 2,
		},
		{
			name: "all_valid_tenants_pass",
			apiTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
				{ID: "tenant-b", Name: "B", Status: "active"},
				{ID: "tenant-c", Name: "C", Status: "active"},
			},
			expectedKnownIDs: 3,
		},
		{
			name: "all_invalid_tenants_filtered",
			apiTenants: []*client.TenantSummary{
				{ID: "../etc", Name: "Bad1", Status: "active"},
				{ID: "tenant with spaces", Name: "Bad2", Status: "active"},
				{ID: "", Name: "Bad3", Status: "active"},
			},
			expectedKnownIDs: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			server := setupTenantManagerAPIServer(t, tt.apiTenants)
			config := newTestConfig(server.URL)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

			ctx := context.Background()
			err := consumer.syncTenants(ctx)
			assert.NoError(t, err, "syncTenants should not return error")

			consumer.mu.RLock()
			knownCount := len(consumer.knownTenants)
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedKnownIDs, knownCount,
				"expected %d known tenants after filtering, got %d", tt.expectedKnownIDs, knownCount)
		})
	}
}

// ---------------------
// T-002: On-Demand Consumer Spawning Tests
// ---------------------

// TestMultiTenantConsumer_EnsureConsumerStarted_SpawnsExactlyOnce verifies that
// concurrent calls to ensureConsumerStarted for the same tenant spawn exactly one consumer.
// Covers: T-002 exactly-once guarantee under concurrency
func TestMultiTenantConsumer_EnsureConsumerStarted_SpawnsExactlyOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		tenantID         string
		concurrentCalls  int
		expectedConsumer int
	}{
		{
			name:             "10_concurrent_calls_spawn_exactly_1_consumer",
			tenantID:         "tenant-001",
			concurrentCalls:  10,
			expectedConsumer: 1,
		},
		{
			name:             "50_concurrent_calls_spawn_exactly_1_consumer",
			tenantID:         "tenant-002",
			concurrentCalls:  50,
			expectedConsumer: 1,
		},
		{
			name:             "100_concurrent_calls_spawn_exactly_1_consumer",
			tenantID:         "tenant-003",
			concurrentCalls:  100,
			expectedConsumer: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			// Register a handler so startTenantConsumer has something to work with
			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Store parentCtx (normally done by Run())
			consumer.parentCtx = ctx

			// Add tenant to knownTenants (normally done by discoverTenants)
			consumer.mu.Lock()
			consumer.knownTenants[tt.tenantID] = true
			consumer.mu.Unlock()

			// Launch N concurrent calls to ensureConsumerStarted
			var wg sync.WaitGroup
			wg.Add(tt.concurrentCalls)

			for i := 0; i < tt.concurrentCalls; i++ {
				go func() {
					defer wg.Done()
					consumer.ensureConsumerStarted(ctx, tt.tenantID)
				}()
			}

			wg.Wait()

			// Verify exactly one consumer was spawned
			consumer.mu.RLock()
			consumerCount := len(consumer.tenants)
			_, hasCancel := consumer.tenants[tt.tenantID]
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedConsumer, consumerCount,
				"expected exactly %d consumer, got %d", tt.expectedConsumer, consumerCount)
			assert.True(t, hasCancel,
				"tenant %q should have an active cancel func in tenants map", tt.tenantID)

			cancel()
			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_EnsureConsumerStarted_NoopWhenActive verifies that
// ensureConsumerStarted is a no-op when the consumer is already running.
func TestMultiTenantConsumer_EnsureConsumerStarted_NoopWhenActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
	}{
		{
			name:     "noop_when_consumer_already_active",
			tenantID: "tenant-active",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			consumer.parentCtx = ctx

			// Add tenant to knownTenants (normally done by discoverTenants)
			consumer.mu.Lock()
			consumer.knownTenants[tt.tenantID] = true
			consumer.mu.Unlock()

			// First call spawns the consumer
			consumer.ensureConsumerStarted(ctx, tt.tenantID)

			consumer.mu.RLock()
			countAfterFirst := len(consumer.tenants)
			consumer.mu.RUnlock()

			assert.Equal(t, 1, countAfterFirst, "first call should spawn 1 consumer")

			// Second call should be a no-op
			consumer.ensureConsumerStarted(ctx, tt.tenantID)

			consumer.mu.RLock()
			countAfterSecond := len(consumer.tenants)
			consumer.mu.RUnlock()

			assert.Equal(t, 1, countAfterSecond,
				"second call should NOT spawn another consumer, count should remain 1")

			cancel()
			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_EnsureConsumerStarted_SkipsWhenClosed verifies that
// ensureConsumerStarted is a no-op when the consumer has been closed.
func TestMultiTenantConsumer_EnsureConsumerStarted_SkipsWhenClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
	}{
		{
			name:     "noop_when_consumer_is_closed",
			tenantID: "tenant-closed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx := context.Background()
			consumer.parentCtx = ctx

			// Close before calling ensureConsumerStarted
			consumer.Close()

			// Should be a no-op
			consumer.ensureConsumerStarted(ctx, tt.tenantID)

			consumer.mu.RLock()
			consumerCount := len(consumer.tenants)
			consumer.mu.RUnlock()

			assert.Equal(t, 0, consumerCount,
				"no consumer should be spawned after Close()")
		})
	}
}

// TestMultiTenantConsumer_EnsureConsumerStarted_MultipleTenants verifies that
// ensureConsumerStarted can spawn consumers for different tenants concurrently.
func TestMultiTenantConsumer_EnsureConsumerStarted_MultipleTenants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tenantIDs []string
	}{
		{
			name:      "spawns_independent_consumers_for_3_tenants",
			tenantIDs: []string{"tenant-a", "tenant-b", "tenant-c"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			consumer.parentCtx = ctx

			// Add tenants to knownTenants (normally done by discoverTenants)
			consumer.mu.Lock()
			for _, id := range tt.tenantIDs {
				consumer.knownTenants[id] = true
			}
			consumer.mu.Unlock()

			// Spawn consumers for all tenants concurrently
			var wg sync.WaitGroup
			wg.Add(len(tt.tenantIDs))

			for _, id := range tt.tenantIDs {
				go func(tenantID string) {
					defer wg.Done()
					consumer.ensureConsumerStarted(ctx, tenantID)
				}(id)
			}

			wg.Wait()

			consumer.mu.RLock()
			consumerCount := len(consumer.tenants)
			for _, id := range tt.tenantIDs {
				_, exists := consumer.tenants[id]
				assert.True(t, exists, "consumer for tenant %q should be active", id)
			}
			consumer.mu.RUnlock()

			assert.Equal(t, len(tt.tenantIDs), consumerCount,
				"expected %d consumers, got %d", len(tt.tenantIDs), consumerCount)

			cancel()
			consumer.Close()
		})
	}
}

// ---------------------
// T-004: Connection Failure Resilience Tests
// ---------------------

// TestBackoffDelay verifies the exponential backoff delay calculation.
// Expected base sequence: 5s, 10s, 20s, 40s, 40s (capped), with ±25% jitter applied.
func TestBackoffDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		retryCount int
		baseDelay  time.Duration
	}{
		{name: "retry_0_base_5s", retryCount: 0, baseDelay: 5 * time.Second},
		{name: "retry_1_base_10s", retryCount: 1, baseDelay: 10 * time.Second},
		{name: "retry_2_base_20s", retryCount: 2, baseDelay: 20 * time.Second},
		{name: "retry_3_base_40s", retryCount: 3, baseDelay: 40 * time.Second},
		{name: "retry_4_capped_at_40s", retryCount: 4, baseDelay: 40 * time.Second},
		{name: "retry_10_capped_at_40s", retryCount: 10, baseDelay: 40 * time.Second},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			delay := backoffDelay(tt.retryCount)
			// backoffDelay applies ±25% jitter: delay ∈ [0.75*base, 1.25*base)
			minDelay := time.Duration(float64(tt.baseDelay) * 0.75)
			maxDelay := time.Duration(float64(tt.baseDelay) * 1.25)
			assert.GreaterOrEqual(t, delay, minDelay,
				"backoffDelay(%d) = %s, want >= %s (0.75 * %s)", tt.retryCount, delay, minDelay, tt.baseDelay)
			assert.Less(t, delay, maxDelay,
				"backoffDelay(%d) = %s, want < %s (1.25 * %s)", tt.retryCount, delay, maxDelay, tt.baseDelay)
		})
	}
}

// TestMultiTenantConsumer_MetricConstants verifies that metric name constants are defined.
func TestMultiTenantConsumer_MetricConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{
			name:     "tenant_connections_total",
			constant: MetricTenantConnectionsTotal,
			expected: "tenant_connections_total",
		},
		{
			name:     "tenant_connection_errors_total",
			constant: MetricTenantConnectionErrors,
			expected: "tenant_connection_errors_total",
		},
		{
			name:     "tenant_consumers_active",
			constant: MetricTenantConsumersActive,
			expected: "tenant_consumers_active",
		},
		{
			name:     "tenant_messages_processed_total",
			constant: MetricTenantMessageProcessed,
			expected: "tenant_messages_processed_total",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tt.constant,
				"metric constant %q should equal %q", tt.constant, tt.expected)
		})
	}
}

// TestMultiTenantConsumer_StructuredLogEvents verifies that key operations
// produce structured log messages with tenant_id context.
func TestMultiTenantConsumer_StructuredLogEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		operation       string
		expectedLogPart string
	}{
		{
			name:            "run_logs_connection_mode",
			operation:       "run",
			expectedLogPart: "connection_mode=eager",
		},
		{
			name:            "discover_logs_tenant_count",
			operation:       "discover",
			expectedLogPart: "discovered",
		},
		{
			name:            "ensure_consumer_logs_on_demand",
			operation:       "ensure",
			expectedLogPart: "on-demand consumer start",
		},
		{
			name:            "sync_logs_tenant_added",
			operation:       "sync",
			expectedLogPart: "tenant added",
		},
		{
			name:            "register_logs_queue",
			operation:       "register",
			expectedLogPart: "registered handler for queue",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, []*client.TenantSummary{
				{ID: "tenant-log-test", Name: "Test", Status: "active"},
			})
			config := newTestConfig(server.URL)
			logger := testutil.NewCapturingLogger()
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = libCommons.ContextWithLogger(ctx, logger)

			consumer.parentCtx = ctx

			switch tt.operation {
			case "run":
				consumer.Run(ctx)
			case "discover":
				consumer.discoverTenants(ctx)
			case "ensure":
				consumer.Register("test-queue", func(ctx context.Context, d amqp.Delivery) error {
					return nil
				})
				// Add tenant to knownTenants so ensureConsumerStarted doesn't reject it
				consumer.mu.Lock()
				consumer.knownTenants["tenant-log-test"] = true
				consumer.mu.Unlock()
				consumer.ensureConsumerStarted(ctx, "tenant-log-test")
			case "sync":
				consumer.syncTenants(ctx)
			case "register":
				consumer.Register("test-queue", func(ctx context.Context, d amqp.Delivery) error {
					return nil
				})
			}

			assert.True(t, logger.ContainsSubstring(tt.expectedLogPart),
				"operation %q should produce log containing %q, got: %v",
				tt.operation, tt.expectedLogPart, logger.GetMessages())

			cancel()
			consumer.Close()
		})
	}
}

// BenchmarkMultiTenantConsumer_Run_Startup measures startup time of Run().
// Target: <1 second for all tenant configurations.
// Covers: AC-Q2
func BenchmarkMultiTenantConsumer_Run_Startup(b *testing.B) {
	benchmarks := []struct {
		name        string
		tenantCount int
	}{
		{name: "0_tenants", tenantCount: 0},
		{name: "100_tenants", tenantCount: 100},
		{name: "500_tenants", tenantCount: 500},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			tenants := makeTenantSummaries(bm.tenantCount)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tenants)
			}))
			defer server.Close()

			config := MultiTenantConfig{
				SyncInterval:      30 * time.Second,
				PrefetchCount:     10,
				Service:           "bench-service",
				MultiTenantURL:    server.URL,
				ServiceAPIKey:     "test-key",
				AllowInsecureHTTP: true,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, testutil.NewMockLogger())

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := consumer.Run(ctx)
				if err != nil {
					b.Fatalf("Run() returned error: %v", err)
				}
				cancel()
				consumer.Close()
			}
		})
	}
}

// ---------------------
// Consumer Option Tests
// ---------------------

// TestMultiTenantConsumer_WithOptions verifies that option functions configure the consumer correctly.
func TestMultiTenantConsumer_WithOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		withPostgres   bool
		withMongo      bool
		expectPostgres bool
		expectMongo    bool
	}{
		{
			name:           "no_options_leaves_managers_nil",
			withPostgres:   false,
			withMongo:      false,
			expectPostgres: false,
			expectMongo:    false,
		},
		{
			name:           "with_postgres_manager",
			withPostgres:   true,
			withMongo:      false,
			expectPostgres: true,
			expectMongo:    false,
		},
		{
			name:           "with_mongo_manager",
			withPostgres:   false,
			withMongo:      true,
			expectPostgres: false,
			expectMongo:    true,
		},
		{
			name:           "with_both_managers",
			withPostgres:   true,
			withMongo:      true,
			expectPostgres: true,
			expectMongo:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var opts []Option

			if tt.withPostgres {
				pgManager := tmpostgres.NewManager(nil, "test-service")
				opts = append(opts, WithPostgresManager(pgManager))
			}

			if tt.withMongo {
				mongoManager := tmmongo.NewManager(nil, "test-service")
				opts = append(opts, WithMongoManager(mongoManager))
			}

			server := setupTenantManagerAPIServer(t, nil)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger(), opts...)

			if tt.expectPostgres {
				assert.NotNil(t, consumer.postgres, "postgres manager should be set")
			} else {
				assert.Nil(t, consumer.postgres, "postgres manager should be nil")
			}

			if tt.expectMongo {
				assert.NotNil(t, consumer.mongo, "mongo manager should be set")
			} else {
				assert.Nil(t, consumer.mongo, "mongo manager should be nil")
			}
		})
	}
}

// TestMultiTenantConsumer_DefaultMultiTenantConfig_IncludesEnvironment verifies that
// DefaultMultiTenantConfig returns an empty Environment field.
func TestMultiTenantConsumer_DefaultMultiTenantConfig_IncludesEnvironment(t *testing.T) {
	t.Parallel()

	config := DefaultMultiTenantConfig()
	assert.Empty(t, config.Environment, "default Environment should be empty")
}

// ---------------------
// Connection Cleanup on Tenant Removal Tests
// ---------------------

// TestMultiTenantConsumer_SyncTenants_ClosesConnectionsOnRemoval verifies that
// when a tenant is removed during sync, its database connections are closed.
func TestMultiTenantConsumer_SyncTenants_ClosesConnectionsOnRemoval(t *testing.T) {
	tests := []struct {
		name             string
		initialTenants   []*client.TenantSummary
		remainingTenants []*client.TenantSummary
		removedTenantIDs []string
	}{
		{
			name: "closes_connections_for_single_removed_tenant",
			initialTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
				{ID: "tenant-b", Name: "B", Status: "active"},
			},
			remainingTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
			},
			removedTenantIDs: []string{"tenant-b"},
		},
		{
			name: "closes_connections_for_all_removed_tenants",
			initialTenants: []*client.TenantSummary{
				{ID: "tenant-a", Name: "A", Status: "active"},
				{ID: "tenant-b", Name: "B", Status: "active"},
				{ID: "tenant-c", Name: "C", Status: "active"},
			},
			remainingTenants: []*client.TenantSummary{},
			removedTenantIDs: []string{"tenant-a", "tenant-b", "tenant-c"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			currentTenants := tt.initialTenants

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				tenants := currentTenants
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tenants)
			}))
			t.Cleanup(server.Close)

			// Use a capturing logger to verify close log messages
			logger := testutil.NewCapturingLogger()
			config := newTestConfig(server.URL)

			// Create managers
			pgManager := tmpostgres.NewManager(nil, "test-service")
			mongoManager := tmmongo.NewManager(nil, "test-service")

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger,
				WithPostgresManager(pgManager),
				WithMongoManager(mongoManager),
			)

			ctx := context.Background()
			ctx = libCommons.ContextWithLogger(ctx, logger)

			// Initial sync to populate state
			err := consumer.syncTenants(ctx)
			require.NoError(t, err, "initial syncTenants should succeed")

			// Simulate active consumers for all tenants (so removal code path is triggered)
			consumer.mu.Lock()
			for _, ts := range tt.initialTenants {
				_, cancel := context.WithCancel(ctx)
				consumer.tenants[ts.ID] = cancel
			}
			consumer.mu.Unlock()

			// Update API to remaining tenants only
			mu.Lock()
			currentTenants = tt.remainingTenants
			mu.Unlock()

			// Single sync removes immediately
			err = consumer.syncTenants(ctx)
			require.NoError(t, err, "syncTenants should succeed")

			// Verify removed tenants are gone from tenants map
			consumer.mu.RLock()
			for _, id := range tt.removedTenantIDs {
				_, exists := consumer.tenants[id]
				assert.False(t, exists,
					"removed tenant %q should not be in tenants map", id)
			}
			consumer.mu.RUnlock()

			// Verify log messages contain removal information for each removed tenant
			for _, id := range tt.removedTenantIDs {
				assert.True(t, logger.ContainsSubstring("closing connections for removed tenant: "+id),
					"should log closing connections for removed tenant %q", id)
			}
		})
	}
}

// TestMultiTenantConsumer_RevalidateConnectionSettings tests revalidation behavior.
func TestMultiTenantConsumer_RevalidateConnectionSettings(t *testing.T) {
	t.Parallel()

	t.Run("skips_when_no_managers_configured", func(t *testing.T) {
		t.Parallel()

		logger := testutil.NewCapturingLogger()
		server := setupTenantManagerAPIServer(t, nil)
		config := newTestConfig(server.URL)
		config.Service = "ledger"

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger)

		ctx := context.Background()
		ctx = libCommons.ContextWithLogger(ctx, logger)

		// Should return immediately without logging
		consumer.revalidateConnectionSettings(ctx)

		assert.False(t, logger.ContainsSubstring("revalidated connection settings"),
			"should not log revalidation when no managers are configured")
	})

	t.Run("skips_when_no_active_tenants", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("should not call Tenant Manager when no active tenants")
			w.WriteHeader(http.StatusOK)
		}))
		defer apiServer.Close()

		logger := testutil.NewCapturingLogger()
		tmClient, tmErr := client.NewClient(apiServer.URL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
		require.NoError(t, tmErr)

		pgManager := tmpostgres.NewManager(tmClient, "ledger")

		server := setupTenantManagerAPIServer(t, nil)
		config := newTestConfig(server.URL)
		config.Service = "ledger"

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger,
			WithPostgresManager(pgManager),
		)
		consumer.pmClient = tmClient

		ctx := context.Background()
		ctx = libCommons.ContextWithLogger(ctx, logger)

		consumer.revalidateConnectionSettings(ctx)

		assert.False(t, logger.ContainsSubstring("revalidated connection settings"),
			"should not log revalidation when no active tenants")
	})

	t.Run("applies_settings_to_active_tenants", func(t *testing.T) {
		t.Parallel()

		// Set up a mock Tenant Manager that returns config with connection settings
		revalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := `{
				"id": "tenant-abc",
				"tenantSlug": "abc",
				"databases": {
					"onboarding": {
						"connectionSettings": {
							"maxOpenConns": 50,
							"maxIdleConns": 15
						}
					}
				}
			}`
			w.Write([]byte(resp))
		}))
		defer revalServer.Close()

		logger := testutil.NewCapturingLogger()
		tmClient, tmErr := client.NewClient(revalServer.URL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
		require.NoError(t, tmErr)

		pgManager := tmpostgres.NewManager(tmClient, "ledger",
			tmpostgres.WithModule("onboarding"),
			tmpostgres.WithLogger(logger),
		)

		apiServer := setupTenantManagerAPIServer(t, nil)
		config := newTestConfig(apiServer.URL)
		config.Service = "ledger"

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger,
			WithPostgresManager(pgManager),
		)
		consumer.pmClient = tmClient

		// Simulate active tenant
		consumer.mu.Lock()
		_, cancel := context.WithCancel(context.Background())
		consumer.tenants["tenant-abc"] = cancel
		consumer.mu.Unlock()

		ctx := context.Background()
		ctx = libCommons.ContextWithLogger(ctx, logger)

		consumer.revalidateConnectionSettings(ctx)

		assert.True(t, logger.ContainsSubstring("revalidated connection settings"),
			"should log revalidation summary")
	})

	t.Run("continues_on_individual_tenant_error", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		revalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if strings.Contains(r.URL.Path, "tenant-fail") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			resp := `{
				"id": "tenant-ok",
				"tenantSlug": "ok",
				"databases": {
					"onboarding": {
						"connectionSettings": {
							"maxOpenConns": 25,
							"maxIdleConns": 5
						}
					}
				}
			}`
			w.Write([]byte(resp))
		}))
		defer revalServer.Close()

		logger := testutil.NewCapturingLogger()
		tmClient, tmErr := client.NewClient(revalServer.URL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
		require.NoError(t, tmErr)

		pgManager := tmpostgres.NewManager(tmClient, "ledger",
			tmpostgres.WithModule("onboarding"),
			tmpostgres.WithLogger(logger),
		)

		apiServer := setupTenantManagerAPIServer(t, nil)
		config := newTestConfig(apiServer.URL)
		config.Service = "ledger"

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger,
			WithPostgresManager(pgManager),
		)
		consumer.pmClient = tmClient

		// Simulate active tenants
		consumer.mu.Lock()
		ctx := context.Background()
		_, cancelOK := context.WithCancel(ctx)
		_, cancelFail := context.WithCancel(ctx)
		consumer.tenants["tenant-ok"] = cancelOK
		consumer.tenants["tenant-fail"] = cancelFail
		consumer.mu.Unlock()

		ctx = libCommons.ContextWithLogger(ctx, logger)

		consumer.revalidateConnectionSettings(ctx)

		// Should log warning about failed tenant
		assert.True(t, logger.ContainsSubstring("failed to fetch config for tenant tenant-fail"),
			"should log warning about fetch failure")
	})
}

// TestMultiTenantConsumer_RevalidateSettings_StopsSuspendedTenant verifies that
// revalidateConnectionSettings stops the consumer and removes the tenant from
// knownTenants and tenants maps when the Tenant Manager returns 403 (suspended/purged).
func TestMultiTenantConsumer_RevalidateSettings_StopsSuspendedTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		responseBody       string
		suspendedTenantID  string
		healthyTenantID    string
		expectLogSubstring string
	}{
		{
			name:               "stops_suspended_tenant_and_keeps_healthy_tenant",
			responseBody:       `{"code":"TS-SUSPENDED","error":"service suspended","status":"suspended"}`,
			suspendedTenantID:  "tenant-suspended",
			healthyTenantID:    "tenant-healthy",
			expectLogSubstring: "tenant tenant-suspended service suspended, stopping consumer and closing connections",
		},
		{
			name:               "stops_purged_tenant_and_keeps_healthy_tenant",
			responseBody:       `{"code":"TS-SUSPENDED","error":"service purged","status":"purged"}`,
			suspendedTenantID:  "tenant-purged",
			healthyTenantID:    "tenant-healthy",
			expectLogSubstring: "service suspended, stopping consumer and closing connections",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Set up a mock Tenant Manager that returns 403 for the suspended tenant
			// and 200 with valid config for the healthy tenant
			revalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")

				if strings.Contains(r.URL.Path, tt.suspendedTenantID) {
					w.WriteHeader(http.StatusForbidden)
					w.Write([]byte(tt.responseBody))

					return
				}

				// Return valid config for healthy tenant
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
					"id": "` + tt.healthyTenantID + `",
					"tenantSlug": "healthy",
					"databases": {
						"onboarding": {
							"connectionSettings": {
								"maxOpenConns": 25,
								"maxIdleConns": 5
							}
						}
					}
				}`))
			}))
			defer revalServer.Close()

			logger := testutil.NewCapturingLogger()
			tmClient, tmErr := client.NewClient(revalServer.URL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
			require.NoError(t, tmErr)

			pgManager := tmpostgres.NewManager(tmClient, "ledger",
				tmpostgres.WithModule("onboarding"),
				tmpostgres.WithLogger(logger),
			)

			apiServer := setupTenantManagerAPIServer(t, nil)
			config := newTestConfig(apiServer.URL)
			config.Service = "ledger"

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger,
				WithPostgresManager(pgManager),
			)
			consumer.pmClient = tmClient

			// Pre-populate per-tenant sync.Map entries
			consumer.consumerLocks.Store(tt.suspendedTenantID, &sync.Mutex{})
			consumer.retryState.Store(tt.suspendedTenantID, &retryStateEntry{})
			consumer.consumerLocks.Store(tt.healthyTenantID, &sync.Mutex{})
			consumer.retryState.Store(tt.healthyTenantID, &retryStateEntry{})

			// Simulate active tenants with cancel functions
			consumer.mu.Lock()
			suspendedCanceled := false
			_, cancelSuspended := context.WithCancel(context.Background())
			wrappedCancel := func() {
				suspendedCanceled = true
				cancelSuspended()
			}
			_, cancelHealthy := context.WithCancel(context.Background())
			consumer.tenants[tt.suspendedTenantID] = wrappedCancel
			consumer.tenants[tt.healthyTenantID] = cancelHealthy
			consumer.knownTenants[tt.suspendedTenantID] = true
			consumer.knownTenants[tt.healthyTenantID] = true
			consumer.mu.Unlock()

			ctx := context.Background()
			ctx = libCommons.ContextWithLogger(ctx, logger)

			// Trigger revalidation
			consumer.revalidateConnectionSettings(ctx)

			// Verify the suspended tenant was removed from tenants map
			consumer.mu.RLock()
			_, suspendedInTenants := consumer.tenants[tt.suspendedTenantID]
			_, suspendedInKnown := consumer.knownTenants[tt.suspendedTenantID]
			_, healthyInTenants := consumer.tenants[tt.healthyTenantID]
			_, healthyInKnown := consumer.knownTenants[tt.healthyTenantID]
			consumer.mu.RUnlock()

			assert.False(t, suspendedInTenants,
				"suspended tenant should be removed from tenants map")
			assert.False(t, suspendedInKnown,
				"suspended tenant should be removed from knownTenants map")
			assert.True(t, suspendedCanceled,
				"suspended tenant's context cancel should have been called")

			// Verify the healthy tenant is still active
			assert.True(t, healthyInTenants,
				"healthy tenant should still be in tenants map")
			assert.True(t, healthyInKnown,
				"healthy tenant should still be in knownTenants map")

			// Verify the appropriate log message was produced
			assert.True(t, logger.ContainsSubstring(tt.expectLogSubstring),
				"expected log message containing %q, got: %v",
				tt.expectLogSubstring, logger.GetMessages())

			// Verify that the healthy tenant was still revalidated
			assert.True(t, logger.ContainsSubstring("revalidated connection settings for 1/"),
				"should log revalidation summary for the healthy tenant")
		})
	}
}

// TestMultiTenantConsumer_AllowInsecureHTTP verifies that the AllowInsecureHTTP
// config field controls whether http:// MultiTenantURLs are accepted by the constructor.
func TestMultiTenantConsumer_AllowInsecureHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      MultiTenantConfig
		expectError bool
		errContains string
	}{
		{
			name: "rejects_http_URL_when_AllowInsecureHTTP_is_false",
			config: MultiTenantConfig{
				MultiTenantURL: "http://tenant-manager.namespace.svc.cluster.local:4003",
				ServiceAPIKey:  "test-key",
				Service:        "ledger",
			},
			expectError: true,
			errContains: "insecure HTTP",
		},
		{
			name: "accepts_http_URL_when_AllowInsecureHTTP_is_true",
			config: MultiTenantConfig{
				MultiTenantURL:    "http://tenant-manager.namespace.svc.cluster.local:4003",
				ServiceAPIKey:     "test-key",
				Service:           "ledger",
				AllowInsecureHTTP: true,
			},
			expectError: false,
		},
		{
			name: "accepts_https_URL_regardless_of_AllowInsecureHTTP",
			config: MultiTenantConfig{
				MultiTenantURL: "https://tenant-manager.dev.example.com",
				ServiceAPIKey:  "test-key",
				Service:        "ledger",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			consumer, err := NewMultiTenantConsumerWithError(
				dummyRabbitMQManager(), tt.config, testutil.NewMockLogger(),
			)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, consumer)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, consumer)
				if consumer != nil {
					_ = consumer.Close()
				}
			}
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_EmptyAPIRemovesAllTenants verifies that
// when the API returns an empty list, all existing tenants are removed.
func TestMultiTenantConsumer_SyncTenants_EmptyAPIRemovesAllTenants(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	currentTenants := makeTenantSummaries(3)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tenants := currentTenants
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tenants)
	}))
	t.Cleanup(server.Close)

	config := newTestConfig(server.URL)
	logger := testutil.NewCapturingLogger()
	consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	// First sync populates
	err := consumer.syncTenants(ctx)
	require.NoError(t, err)

	consumer.mu.RLock()
	assert.Equal(t, 3, len(consumer.knownTenants), "should have 3 tenants after first sync")
	consumer.mu.RUnlock()

	// Update API to return empty
	mu.Lock()
	currentTenants = []*client.TenantSummary{}
	mu.Unlock()

	// Second sync removes all
	err = consumer.syncTenants(ctx)
	require.NoError(t, err)

	consumer.mu.RLock()
	assert.Equal(t, 0, len(consumer.knownTenants), "should have 0 tenants after empty API response")
	consumer.mu.RUnlock()
}

// TestMultiTenantConsumer_SyncTenants_LogOnlyOnChanges verifies that sync does NOT
// produce any summary log when nothing changed.
func TestMultiTenantConsumer_SyncTenants_LogOnlyOnChanges(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, makeTenantSummaries(2))
	config := newTestConfig(server.URL)
	logger := testutil.NewCapturingLogger()
	consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), config, logger)

	ctx := context.Background()
	ctx = libCommons.ContextWithLogger(ctx, logger)

	// First sync discovers tenants
	err := consumer.syncTenants(ctx)
	require.NoError(t, err)

	// Clear captured logs
	logger.Clear()

	// Second sync with same tenants should produce no "tenant added" or "tenant removed" logs
	err = consumer.syncTenants(ctx)
	require.NoError(t, err)

	assert.False(t, logger.ContainsSubstring("tenant added"),
		"should NOT log 'tenant added' when nothing changed")
	assert.False(t, logger.ContainsSubstring("tenant removed"),
		"should NOT log 'tenant removed' when nothing changed")
	assert.False(t, logger.ContainsSubstring("sync complete"),
		"should NOT log 'sync complete' summary")
}
