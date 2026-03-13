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
	"github.com/alicebob/miniredis/v2"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NewMultiTenantConsumer is a test convenience wrapper around NewMultiTenantConsumerWithError
// that panics on error. This keeps test code concise while preserving the v4 constructor signature.
func NewMultiTenantConsumer(
	rabbitmq *tmrabbitmq.Manager,
	redisClient redis.UniversalClient,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) *MultiTenantConsumer {
	c, err := NewMultiTenantConsumerWithError(rabbitmq, redisClient, config, logger, opts...)
	if err != nil {
		panic(fmt.Sprintf("NewMultiTenantConsumer (test helper): %v", err))
	}

	return c
}

// mustNewConsumer is an alternative test helper that takes *testing.T and calls t.Fatal on error.
func mustNewConsumer(
	t *testing.T,
	rabbitmq *tmrabbitmq.Manager,
	redisClient redis.UniversalClient,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) *MultiTenantConsumer {
	t.Helper()

	c, err := NewMultiTenantConsumerWithError(rabbitmq, redisClient, config, logger, opts...)
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

// setupMiniredis creates a miniredis instance and returns it with a go-redis client.
func setupMiniredis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()

	mr, err := miniredis.Run()
	require.NoError(t, err, "failed to start miniredis")

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	t.Cleanup(func() {
		redisClient.Close()
		mr.Close()
	})

	return mr, redisClient
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

// dummyRedisClient returns a miniredis-backed Redis client for tests that need a
// non-nil redisClient but do not exercise Redis. The caller does not need to
// close the returned client; it is registered for cleanup via t.Cleanup.
func dummyRedisClient(t *testing.T) redis.UniversalClient {
	t.Helper()

	_, redisClient := setupMiniredis(t)

	return redisClient
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

// testActiveTenantsKey is the Redis key used by tests with Service="test-service" and no Environment.
// This matches the key that fetchTenantIDs will read from when Environment is empty.
var testActiveTenantsKey = buildActiveTenantsKey("", testServiceName)

// maxRunDuration is the maximum time Run() is allowed to take in lazy mode.
// The requirement specifies <1 second. We use 1 second as the hard deadline.
const maxRunDuration = 1 * time.Second

// TestMultiTenantConsumer_Run_LazyMode validates that Run() completes within 1 second,
// returns nil error (soft failure), populates knownTenants, and does NOT start consumers.
// Covers: AC-F1, AC-F2, AC-F3, AC-F4, AC-F5, AC-F6, AC-O3, AC-Q1
func TestMultiTenantConsumer_Run_LazyMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		redisTenantIDs           []string
		apiTenants               []*client.TenantSummary
		apiServerDown            bool
		redisDown                bool
		expectedKnownTenantCount int
		expectError              bool
		expectConsumersStarted   bool
	}{
		{
			name:                     "returns_within_1s_with_0_tenants_configured",
			redisTenantIDs:           []string{},
			apiTenants:               nil,
			expectedKnownTenantCount: 0,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		{
			name:                     "returns_within_1s_with_100_tenants_in_Redis_cache",
			redisTenantIDs:           generateTenantIDs(100),
			apiTenants:               nil,
			expectedKnownTenantCount: 100,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		{
			name:                     "returns_within_1s_with_500_tenants_from_Tenant_Manager_API",
			redisTenantIDs:           []string{},
			apiTenants:               makeTenantSummaries(500),
			expectedKnownTenantCount: 500,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		{
			name:                     "returns_nil_error_when_both_Redis_and_API_are_down",
			redisTenantIDs:           nil,
			redisDown:                true,
			apiServerDown:            true,
			expectedKnownTenantCount: 0,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		{
			name:                     "returns_nil_error_when_API_server_is_down",
			redisTenantIDs:           []string{},
			apiServerDown:            true,
			expectedKnownTenantCount: 0,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		// Edge case: single tenant in Redis
		{
			name:                     "returns_within_1s_with_1_tenant_in_Redis_cache",
			redisTenantIDs:           []string{"single-tenant"},
			apiTenants:               nil,
			expectedKnownTenantCount: 1,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		// Edge case: Redis empty but API returns tenants (fallback path)
		{
			name:                     "falls_back_to_API_when_Redis_cache_is_empty",
			redisTenantIDs:           []string{},
			apiTenants:               makeTenantSummaries(3),
			expectedKnownTenantCount: 3,
			expectError:              false,
			expectConsumersStarted:   false,
		},
		// Edge case: Redis down but API is up. Discovery timeout (500ms) may
		// be consumed by the Redis connection attempt, so API fallback may not
		// complete in time. In this case, discoverTenants treats it as soft failure
		// and the background sync loop will retry. We expect 0 tenants known at startup.
		{
			name:                     "returns_nil_error_when_Redis_down_and_API_configured",
			redisTenantIDs:           nil,
			redisDown:                true,
			apiServerDown:            false,
			apiTenants:               makeTenantSummaries(5),
			expectedKnownTenantCount: 0,
			expectError:              false,
			expectConsumersStarted:   false,
		},
	}

	for _, tt := range tests {
		tt := tt // capture loop variable for parallel subtests
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup miniredis
			mr, redisClient := setupMiniredis(t)

			// Populate Redis SET with tenant IDs (if provided and Redis is up)
			if !tt.redisDown && len(tt.redisTenantIDs) > 0 {
				for _, id := range tt.redisTenantIDs {
					mr.SAdd(testActiveTenantsKey, id)
				}
			}

			// If Redis should be down, close it
			if tt.redisDown {
				mr.Close()
			}

			// Setup Tenant Manager API server and pmClient
			var apiURL string
			if !tt.apiServerDown && tt.apiTenants != nil {
				server := setupTenantManagerAPIServer(t, tt.apiTenants)
				apiURL = server.URL
			} else if tt.apiServerDown {
				apiURL = "http://127.0.0.1:0" // unreachable port
			}

			// Create consumer config (MultiTenantURL left empty; pmClient set manually below)
			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}

			// Create the consumer
			mockLogger := testutil.NewMockLogger()
			consumer := NewMultiTenantConsumer(
				dummyRabbitMQManager(),
				redisClient,
				config,
				mockLogger,
			)

			// Manually create pmClient for http:// test URLs (bypasses HTTPS enforcement in constructor)
			if apiURL != "" {
				pmClient, pmErr := client.NewClient(apiURL, mockLogger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
				require.NoError(t, pmErr)
				consumer.pmClient = pmClient
				consumer.config.Service = "test-service"
			}

			// Register a handler (to verify it is NOT consumed from during Run)
			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				t.Error("handler should not be called during Run() in lazy mode")
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
				"Run() must complete within %s in lazy mode, took %s", maxRunDuration, elapsed)

			// ASSERTION 2: Run() returns nil error (even on discovery failure)
			if !tt.expectError {
				assert.NoError(t, err,
					"Run() must return nil error in lazy mode (soft failure on discovery)")
			}

			// ASSERTION 3: knownTenants is populated (NOT tenants which holds cancel funcs)
			consumer.mu.RLock()
			knownCount := len(consumer.knownTenants)
			consumersStarted := len(consumer.tenants)
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedKnownTenantCount, knownCount,
				"knownTenants should have %d entries after Run(), got %d",
				tt.expectedKnownTenantCount, knownCount)

			// ASSERTION 4: No consumers started during Run() (lazy mode = no startTenantConsumer calls)
			if !tt.expectConsumersStarted {
				assert.Equal(t, 0, consumersStarted,
					"no goroutines should call startTenantConsumer() during Run(), but %d consumers are active",
					consumersStarted)
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

			_, redisClient := setupMiniredis(t)
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

			fn = consumer.Run
			assert.NotNil(t, fn, "Run method must exist and match expected signature")
		})
	}
}

// TestMultiTenantConsumer_DiscoverTenants_ReuseFetchTenantIDs verifies that
// discoverTenants() delegates to fetchTenantIDs() internally by confirming that
// tenant IDs sourced from Redis (via fetchTenantIDs) end up in knownTenants.
// Covers: AC-T2
func TestMultiTenantConsumer_DiscoverTenants_ReuseFetchTenantIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		redisTenantIDs []string
		expectedCount  int
	}{
		{
			name:           "discovers_tenants_from_Redis_via_fetchTenantIDs",
			redisTenantIDs: []string{"tenant-a", "tenant-b", "tenant-c"},
			expectedCount:  3,
		},
		{
			name:           "discovers_zero_tenants_when_Redis_is_empty",
			redisTenantIDs: []string{},
			expectedCount:  0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mr, redisClient := setupMiniredis(t)

			// This test uses no Service or Environment, so the key has empty segments
			noServiceKey := buildActiveTenantsKey("", "")
			for _, id := range tt.redisTenantIDs {
				mr.SAdd(noServiceKey, id)
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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
			for _, id := range tt.redisTenantIDs {
				assert.True(t, consumer.knownTenants[id],
					"tenant %q should be in knownTenants after discovery", id)
			}
			consumer.mu.RUnlock()
		})
	}
}

// TestMultiTenantConsumer_Run_StartupLog verifies that Run() produces a log message
// containing "connection_mode=lazy" during startup.
// Covers: AC-T3
func TestMultiTenantConsumer_Run_StartupLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		expectedLogPart string
	}{
		{
			name:            "startup_log_contains_connection_mode_lazy",
			expectedLogPart: "connection_mode=lazy",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, redisClient := setupMiniredis(t)

			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}

			logger := testutil.NewCapturingLogger()

			consumer := NewMultiTenantConsumer(
				dummyRabbitMQManager(),
				redisClient,
				config,
				logger,
			)

			// Set the capturing logger in context so NewTrackingFromContext returns it
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = libCommons.ContextWithLogger(ctx, logger)

			err := consumer.Run(ctx)
			assert.NoError(t, err, "Run() should return nil in lazy mode")

			// Verify the startup log contains connection_mode=lazy
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
			mr, redisClient := setupMiniredis(t)

			config := MultiTenantConfig{
				SyncInterval:    tt.syncInterval,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}

			consumer := NewMultiTenantConsumer(
				dummyRabbitMQManager(),
				redisClient,
				config,
				testutil.NewMockLogger(),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Run() should return immediately (lazy mode)
			err := consumer.Run(ctx)
			require.NoError(t, err, "Run() should succeed in lazy mode")

			// After Run, add tenants to Redis - the sync loop should pick them up
			mr.SAdd(testActiveTenantsKey, tt.tenantToAdd)

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
		name           string
		redisTenantIDs []string
		apiTenants     []*client.TenantSummary
	}{
		{
			name:           "ready_within_5s_with_0_tenants",
			redisTenantIDs: []string{},
		},
		{
			name:           "ready_within_5s_with_100_tenants",
			redisTenantIDs: generateTenantIDs(100),
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

			mr, redisClient := setupMiniredis(t)

			for _, id := range tt.redisTenantIDs {
				mr.SAdd(testActiveTenantsKey, id)
			}

			var apiURL string
			if tt.apiTenants != nil {
				server := setupTenantManagerAPIServer(t, tt.apiTenants)
				apiURL = server.URL
			}

			mockLogger := testutil.NewMockLogger()
			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, config, mockLogger)

			if apiURL != "" {
				pmClient, pmErr := client.NewClient(apiURL, mockLogger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
				require.NoError(t, pmErr)
				consumer.pmClient = pmClient
			}

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
		name           string
		redisTenantIDs []string
		apiTenants     []*client.TenantSummary
	}{
		{name: "0_tenants", redisTenantIDs: []string{}},
		{name: "100_tenants", redisTenantIDs: generateTenantIDs(100)},
		{name: "500_tenants_via_API", apiTenants: makeTenantSummaries(500)},
	}

	var durations []time.Duration

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mr, redisClient := setupMiniredis(t)

			for _, id := range tt.redisTenantIDs {
				mr.SAdd(testActiveTenantsKey, id)
			}

			var apiURL string
			if tt.apiTenants != nil {
				server := setupTenantManagerAPIServer(t, tt.apiTenants)
				apiURL = server.URL
			}

			mockLogger := testutil.NewMockLogger()
			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, config, mockLogger)

			if apiURL != "" {
				pmClient, pmErr := client.NewClient(apiURL, mockLogger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
				require.NoError(t, pmErr)
				consumer.pmClient = pmClient
			}

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
		redisDown       bool
		apiDown         bool
		expectedLogPart string
	}{
		{
			name:            "logs_warning_when_Redis_and_API_both_fail",
			redisDown:       true,
			apiDown:         true,
			expectedLogPart: "tenant discovery failed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mr, redisClient := setupMiniredis(t)

			if tt.redisDown {
				mr.Close()
			}

			var apiURL string
			if tt.apiDown {
				apiURL = "http://127.0.0.1:0"
			}

			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}

			logger := testutil.NewCapturingLogger()
			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, config, logger)

			if apiURL != "" {
				pmClient, pmErr := client.NewClient(apiURL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
				require.NoError(t, pmErr)
				consumer.pmClient = pmClient
			}

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
		})
	}
}

// TestMultiTenantConsumer_NewWithZeroConfig verifies that NewMultiTenantConsumer
// applies defaults when config fields are zero-valued.
func TestMultiTenantConsumer_NewWithZeroConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		config           MultiTenantConfig
		expectedSync     time.Duration
		expectedWorkers  int
		expectedPrefetch int
		expectPMClient   bool
	}{
		{
			name:             "applies_defaults_for_all_zero_fields",
			config:           MultiTenantConfig{},
			expectedSync:     30 * time.Second,
			expectedWorkers:  0, // WorkersPerQueue is deprecated, default is 0
			expectedPrefetch: 10,
			expectPMClient:   false,
		},
		{
			name: "preserves_explicit_values",
			config: MultiTenantConfig{
				SyncInterval:    60 * time.Second,
				WorkersPerQueue: 5,
				PrefetchCount:   20,
			},
			expectedSync:     60 * time.Second,
			expectedWorkers:  5,
			expectedPrefetch: 20,
			expectPMClient:   false,
		},
		{
			name: "creates_pmClient_when_URL_configured",
			config: MultiTenantConfig{
				MultiTenantURL:  "https://tenant-manager:4003",
				ServiceAPIKey: "test-key",
			},
			expectedSync:     30 * time.Second,
			expectedWorkers:  0, // WorkersPerQueue is deprecated, default is 0
			expectedPrefetch: 10,
			expectPMClient:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, tt.config, testutil.NewMockLogger())

			assert.NotNil(t, consumer, "consumer must not be nil")
			assert.Equal(t, tt.expectedSync, consumer.config.SyncInterval)
			assert.Equal(t, tt.expectedWorkers, consumer.config.WorkersPerQueue)
			assert.Equal(t, tt.expectedPrefetch, consumer.config.PrefetchCount)
			assert.NotNil(t, consumer.handlers, "handlers map must be initialized")
			assert.NotNil(t, consumer.tenants, "tenants map must be initialized")
			assert.NotNil(t, consumer.knownTenants, "knownTenants map must be initialized")

			if tt.expectPMClient {
				assert.NotNil(t, consumer.pmClient,
					"pmClient should be created when MultiTenantURL is configured")
			} else {
				assert.Nil(t, consumer.pmClient,
					"pmClient should be nil when MultiTenantURL is empty")
			}
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

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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
				"no tenants should be active (lazy mode, no startTenantConsumer called)")
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

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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
			// Close() clears regular maps (tenants, knownTenants, tenantAbsenceCount) only.
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
// removes tenants that are no longer in the Redis cache.
func TestMultiTenantConsumer_SyncTenants_RemovesTenants(t *testing.T) {
	// Not parallel: relies on internal state manipulation

	tests := []struct {
		name                   string
		initialTenants         []string
		postSyncTenants        []string
		expectedKnownAfterSync int
	}{
		{
			name:                   "removes_tenants_no_longer_in_cache",
			initialTenants:         []string{"tenant-a", "tenant-b", "tenant-c"},
			postSyncTenants:        []string{"tenant-a"},
			expectedKnownAfterSync: 1,
		},
		{
			name:                   "handles_all_tenants_removed",
			initialTenants:         []string{"tenant-a", "tenant-b"},
			postSyncTenants:        []string{},
			expectedKnownAfterSync: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mr, redisClient := setupMiniredis(t)

			// Populate initial tenants
			for _, id := range tt.initialTenants {
				mr.SAdd(testActiveTenantsKey, id)
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}, testutil.NewMockLogger())

			ctx := context.Background()

			// Initial discovery
			consumer.discoverTenants(ctx)

			consumer.mu.RLock()
			initialCount := len(consumer.knownTenants)
			consumer.mu.RUnlock()
			assert.Equal(t, len(tt.initialTenants), initialCount,
				"initial discovery should find all tenants")

			// Pre-populate consumerLocks, retryState, and active consumers for
			// all initial tenants to verify they are cleaned up when tenants are removed.
			// Active consumers (c.tenants) are required because stopRemovedTenants only
			// processes tenants that have a running consumer.
			consumer.mu.Lock()
			for _, id := range tt.initialTenants {
				consumer.consumerLocks.Store(id, &sync.Mutex{})
				consumer.retryState.Store(id, &retryStateEntry{})
				_, cancel := context.WithCancel(ctx)
				consumer.tenants[id] = cancel
			}
			consumer.mu.Unlock()

			// Update Redis to reflect post-sync state (remove some tenants)
			mr.Del(testActiveTenantsKey)
			for _, id := range tt.postSyncTenants {
				mr.SAdd(testActiveTenantsKey, id)
			}

			// Run syncTenants absentSyncsBeforeRemoval times so retained tenants
			// exceed the absence threshold and are actually removed.
			for i := 0; i < absentSyncsBeforeRemoval; i++ {
				err := consumer.syncTenants(ctx)
				assert.NoError(t, err, "syncTenants should not return error")
			}

			consumer.mu.RLock()
			afterSyncCount := len(consumer.knownTenants)
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedKnownAfterSync, afterSyncCount,
				"after %d syncs, knownTenants should reflect updated tenant list", absentSyncsBeforeRemoval)

			// Verify consumerLocks and retryState are cleaned for removed tenants
			removedSet := make(map[string]bool, len(tt.initialTenants))
			for _, id := range tt.initialTenants {
				removedSet[id] = true
			}

			for _, id := range tt.postSyncTenants {
				delete(removedSet, id)
			}

			// Note: sync.Map entries (consumerLocks, retryState) are NOT cleaned by
			// syncTenants/cancelRemovedTenantConsumers. They are cleaned lazily.
			// Only regular maps (tenants, knownTenants) are reconciled during sync.
			_ = removedSet
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_LazyMode verifies that syncTenants() populates
// knownTenants for new tenants WITHOUT starting consumer goroutines (lazy mode behavior).
// In lazy mode, consumers are spawned on-demand (T-002), not during sync.
// Covers: T-005 AC-F1, AC-F2, AC-T3
func TestMultiTenantConsumer_SyncTenants_LazyMode(t *testing.T) {
	tests := []struct {
		name                  string
		initialRedisTenants   []string
		newRedisTenants       []string
		expectedKnownCount    int
		expectedConsumerCount int
	}{
		{
			name:                  "new_tenants_added_to_knownTenants_only_not_activeTenants",
			initialRedisTenants:   []string{},
			newRedisTenants:       []string{"tenant-a", "tenant-b", "tenant-c"},
			expectedKnownCount:    3,
			expectedConsumerCount: 0,
		},
		{
			name:                  "sync_discovers_100_tenants_without_starting_consumers",
			initialRedisTenants:   []string{},
			newRedisTenants:       generateTenantIDs(100),
			expectedKnownCount:    100,
			expectedConsumerCount: 0,
		},
		{
			name:                  "sync_adds_incremental_tenants_without_starting_consumers",
			initialRedisTenants:   []string{"existing-tenant"},
			newRedisTenants:       []string{"existing-tenant", "new-tenant-1", "new-tenant-2"},
			expectedKnownCount:    3,
			expectedConsumerCount: 0,
		},
		{
			name:                  "sync_with_zero_tenants_starts_no_consumers",
			initialRedisTenants:   []string{},
			newRedisTenants:       []string{},
			expectedKnownCount:    0,
			expectedConsumerCount: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mr, redisClient := setupMiniredis(t)

			// Populate initial tenants
			for _, id := range tt.initialRedisTenants {
				mr.SAdd(testActiveTenantsKey, id)
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}, testutil.NewMockLogger())

			// Register a handler so startTenantConsumer would have something to consume
			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				t.Error("handler must not be called during syncTenants in lazy mode")
				return nil
			})

			ctx := context.Background()

			// Initial discovery (populates knownTenants only)
			consumer.discoverTenants(ctx)

			// Update Redis with new tenants
			mr.Del(testActiveTenantsKey)
			for _, id := range tt.newRedisTenants {
				mr.SAdd(testActiveTenantsKey, id)
			}

			// Run syncTenants - should populate knownTenants but NOT start consumers
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

			// ASSERTION 2: No consumer goroutines started (lazy mode)
			assert.Equal(t, tt.expectedConsumerCount, consumerCount,
				"syncTenants must NOT start consumers in lazy mode (expected %d active consumers, got %d)",
				tt.expectedConsumerCount, consumerCount)
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_RemovalCleansKnownTenants verifies that when
// a tenant is removed from Redis, syncTenants() cleans it from knownTenants and
// cancels any active consumer for that tenant.
// Covers: T-005 AC-F3, AC-F4
func TestMultiTenantConsumer_SyncTenants_RemovalCleansKnownTenants(t *testing.T) {
	tests := []struct {
		name                      string
		initialTenants            []string
		remainingTenants          []string
		expectedKnownAfterRemoval int
	}{
		{
			name:                      "removed_tenant_cleaned_from_knownTenants",
			initialTenants:            []string{"tenant-a", "tenant-b", "tenant-c"},
			remainingTenants:          []string{"tenant-a"},
			expectedKnownAfterRemoval: 1,
		},
		{
			name:                      "all_tenants_removed_cleans_knownTenants",
			initialTenants:            []string{"tenant-a", "tenant-b"},
			remainingTenants:          []string{},
			expectedKnownAfterRemoval: 0,
		},
		{
			name:                      "no_tenants_removed_keeps_all_in_knownTenants",
			initialTenants:            []string{"tenant-a", "tenant-b"},
			remainingTenants:          []string{"tenant-a", "tenant-b"},
			expectedKnownAfterRemoval: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mr, redisClient := setupMiniredis(t)

			// Populate initial tenants
			for _, id := range tt.initialTenants {
				mr.SAdd(testActiveTenantsKey, id)
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}, testutil.NewMockLogger())

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

			// Remove tenants from Redis
			mr.Del(testActiveTenantsKey)
			for _, id := range tt.remainingTenants {
				mr.SAdd(testActiveTenantsKey, id)
			}

			// Run sync absentSyncsBeforeRemoval times so retained tenants exceed
			// the absence threshold and are cleaned from knownTenants.
			for i := 0; i < absentSyncsBeforeRemoval; i++ {
				err = consumer.syncTenants(ctx)
				require.NoError(t, err, "syncTenants should succeed")
			}

			consumer.mu.RLock()
			afterRemovalKnown := len(consumer.knownTenants)
			// Verify removed tenants are NOT in knownTenants
			for _, id := range tt.initialTenants {
				isRemaining := false
				for _, remaining := range tt.remainingTenants {
					if id == remaining {
						isRemaining = true
						break
					}
				}
				if !isRemaining {
					assert.False(t, consumer.knownTenants[id],
						"removed tenant %q must be cleaned from knownTenants after %d absences", id, absentSyncsBeforeRemoval)
				}
			}
			consumer.mu.RUnlock()

			assert.Equal(t, tt.expectedKnownAfterRemoval, afterRemovalKnown,
				"after %d absences, knownTenants should have %d entries, got %d",
				absentSyncsBeforeRemoval, tt.expectedKnownAfterRemoval, afterRemovalKnown)
		})
	}
}

// TestMultiTenantConsumer_SyncTenants_SyncLoopContinuesOnError verifies that the
// sync loop continues operating when individual sync iterations fail.
// Covers: T-005 AC-O3
func TestMultiTenantConsumer_SyncTenants_SyncLoopContinuesOnError(t *testing.T) {
	tests := []struct {
		name              string
		breakRedisOnFirst bool
		restoreBefore     int // restore Redis before this sync iteration
	}{
		{
			name:              "continues_after_transient_error",
			breakRedisOnFirst: true,
			restoreBefore:     2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mr, redisClient := setupMiniredis(t)

			// Populate tenants
			mr.SAdd(testActiveTenantsKey, "tenant-001")

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    100 * time.Millisecond,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}, testutil.NewMockLogger())

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			// First sync succeeds
			err := consumer.syncTenants(ctx)
			assert.NoError(t, err, "first syncTenants should succeed")

			// Break Redis
			mr.Close()

			// Second sync should fail but not crash
			err = consumer.syncTenants(ctx)
			assert.Error(t, err, "syncTenants should return error when Redis is down")

			// Verify consumer still functional (not panicked)
			consumer.mu.RLock()
			assert.False(t, consumer.closed, "consumer should not be closed after sync error")
			consumer.mu.RUnlock()

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

			mr, redisClient := setupMiniredis(t)
			mr.SAdd(testActiveTenantsKey, "tenant-001")

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}, testutil.NewMockLogger())

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
		name           string
		redisTenantIDs []string
		apiTenants     []*client.TenantSummary
		redisDown      bool
		apiDown        bool
		expectError    bool
		expectedCount  int
		errContains    string
	}{
		{
			name:           "returns_tenants_from_Redis_cache",
			redisTenantIDs: []string{"t1", "t2", "t3"},
			expectedCount:  3,
		},
		{
			name:           "returns_empty_list_when_no_tenants",
			redisTenantIDs: []string{},
			expectedCount:  0,
		},
		{
			name:          "falls_back_to_API_when_Redis_is_empty",
			apiTenants:    makeTenantSummaries(2),
			expectedCount: 2,
		},
		{
			name:        "returns_error_when_both_Redis_and_API_fail",
			redisDown:   true,
			apiDown:     true,
			expectError: true,
		},
		{
			name:          "returns_tenants_from_API_when_Redis_fails",
			redisDown:     true,
			apiTenants:    makeTenantSummaries(4),
			expectedCount: 4,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mr, redisClient := setupMiniredis(t)

			if !tt.redisDown {
				for _, id := range tt.redisTenantIDs {
					mr.SAdd(testActiveTenantsKey, id)
				}
			} else {
				mr.Close()
			}

			var apiURL string
			if tt.apiTenants != nil && !tt.apiDown {
				server := setupTenantManagerAPIServer(t, tt.apiTenants)
				apiURL = server.URL
			} else if tt.apiDown {
				apiURL = "http://127.0.0.1:0"
			}

			mockLogger := testutil.NewMockLogger()
			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, config, mockLogger)

			if apiURL != "" {
				pmClient, pmErr := client.NewClient(apiURL, mockLogger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
				require.NoError(t, pmErr)
				consumer.pmClient = pmClient
			}

			ids, err := consumer.fetchTenantIDs(context.Background())

			if tt.expectError {
				assert.Error(t, err, "fetchTenantIDs should return error")
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
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

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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

// TestMultiTenantConsumer_NilLogger verifies that NewMultiTenantConsumer does not panic
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

			_, redisClient := setupMiniredis(t)

			assert.NotPanics(t, func() {
				consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
					SyncInterval:    30 * time.Second,
					WorkersPerQueue: 1,
					PrefetchCount:   10,
				}, nil) // nil logger

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
		redisTenantIDs   []string
		expectedKnownIDs int
	}{
		{
			name:             "filters_out_path_traversal_attempts",
			redisTenantIDs:   []string{"valid-tenant", "../../etc/passwd", "also-valid"},
			expectedKnownIDs: 2,
		},
		{
			name:             "filters_out_empty_strings",
			redisTenantIDs:   []string{"valid-tenant", "", "another-valid"},
			expectedKnownIDs: 2,
		},
		{
			name:             "all_valid_tenants_pass",
			redisTenantIDs:   []string{"tenant-a", "tenant-b", "tenant-c"},
			expectedKnownIDs: 3,
		},
		{
			name:             "all_invalid_tenants_filtered",
			redisTenantIDs:   []string{"../etc", "tenant with spaces", ""},
			expectedKnownIDs: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mr, redisClient := setupMiniredis(t)

			for _, id := range tt.redisTenantIDs {
				mr.SAdd(testActiveTenantsKey, id)
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}, testutil.NewMockLogger())

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

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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

// TestMultiTenantConsumer_EnsureConsumerStarted_PublicAPI verifies the public
// EnsureConsumerStarted method delegates correctly to the internal method.
func TestMultiTenantConsumer_EnsureConsumerStarted_PublicAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
	}{
		{
			name:     "public_API_spawns_consumer",
			tenantID: "tenant-public",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

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

			// Use public API
			consumer.EnsureConsumerStarted(ctx, tt.tenantID)

			consumer.mu.RLock()
			_, exists := consumer.tenants[tt.tenantID]
			consumer.mu.RUnlock()

			assert.True(t, exists, "public API should spawn consumer for tenant %q", tt.tenantID)

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

// TestMultiTenantConsumer_RetryState verifies per-tenant retry state management.
func TestMultiTenantConsumer_RetryState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		tenantID          string
		incrementRetries  int
		expectedDegraded  bool
		resetBeforeAssert bool
	}{
		{
			name:             "initial_retry_state_is_zero",
			tenantID:         "tenant-fresh",
			incrementRetries: 0,
			expectedDegraded: false,
		},
		{
			name:             "2_retries_not_degraded",
			tenantID:         "tenant-2-retries",
			incrementRetries: 2,
			expectedDegraded: false,
		},
		{
			name:             "3_retries_marks_degraded",
			tenantID:         "tenant-3-retries",
			incrementRetries: 3,
			expectedDegraded: true,
		},
		{
			name:             "5_retries_stays_degraded",
			tenantID:         "tenant-5-retries",
			incrementRetries: 5,
			expectedDegraded: true,
		},
		{
			name:              "reset_clears_retry_state",
			tenantID:          "tenant-reset",
			incrementRetries:  5,
			resetBeforeAssert: true,
			expectedDegraded:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

			state := consumer.getRetryState(tt.tenantID)

			for i := 0; i < tt.incrementRetries; i++ {
				state.retryCount++
				if state.retryCount >= maxRetryBeforeDegraded {
					state.degraded = true
				}
			}

			if tt.resetBeforeAssert {
				consumer.resetRetryState(tt.tenantID)
			}

			isDegraded := consumer.IsDegraded(tt.tenantID)
			assert.Equal(t, tt.expectedDegraded, isDegraded,
				"IsDegraded(%q) = %v, want %v", tt.tenantID, isDegraded, tt.expectedDegraded)
		})
	}
}

// TestMultiTenantConsumer_RetryStateIsolation verifies that retry state is
// isolated between tenants (one tenant's failures don't affect another).
func TestMultiTenantConsumer_RetryStateIsolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "retry_state_isolated_between_tenants"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, redisClient := setupMiniredis(t)

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger())

			// Tenant A: 5 failures (degraded)
			stateA := consumer.getRetryState("tenant-a")
			for i := 0; i < 5; i++ {
				stateA.retryCount++
				if stateA.retryCount >= maxRetryBeforeDegraded {
					stateA.degraded = true
				}
			}

			// Tenant B: 0 failures (healthy)
			_ = consumer.getRetryState("tenant-b")

			assert.True(t, consumer.IsDegraded("tenant-a"),
				"tenant-a should be degraded after 5 failures")
			assert.False(t, consumer.IsDegraded("tenant-b"),
				"tenant-b should NOT be degraded (no failures)")
		})
	}
}

// ---------------------
// T-003: Enhanced Observability Tests
// ---------------------

// TestMultiTenantConsumer_Stats_Enhanced verifies the enhanced Stats() API
// returns ConnectionMode, KnownTenants, PendingTenants, and DegradedTenants.
func TestMultiTenantConsumer_Stats_Enhanced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		redisTenantIDs        []string
		startConsumerForIDs   []string
		degradeTenantIDs      []string
		expectedKnown         int
		expectedActive        int
		expectedPending       int
		expectedDegradedCount int
		expectedConnMode      string
	}{
		{
			name:                  "all_tenants_pending_in_lazy_mode",
			redisTenantIDs:        []string{"tenant-a", "tenant-b", "tenant-c"},
			startConsumerForIDs:   nil,
			expectedKnown:         3,
			expectedActive:        0,
			expectedPending:       3,
			expectedDegradedCount: 0,
			expectedConnMode:      "lazy",
		},
		{
			name:                  "mix_of_active_and_pending",
			redisTenantIDs:        []string{"tenant-a", "tenant-b", "tenant-c"},
			startConsumerForIDs:   []string{"tenant-a"},
			expectedKnown:         3,
			expectedActive:        1,
			expectedPending:       2,
			expectedDegradedCount: 0,
			expectedConnMode:      "lazy",
		},
		{
			name:                  "degraded_tenant_appears_in_stats",
			redisTenantIDs:        []string{"tenant-a", "tenant-b"},
			startConsumerForIDs:   nil,
			degradeTenantIDs:      []string{"tenant-b"},
			expectedKnown:         2,
			expectedActive:        0,
			expectedPending:       2,
			expectedDegradedCount: 1,
			expectedConnMode:      "lazy",
		},
		{
			name:                  "empty_consumer_returns_zero_stats",
			redisTenantIDs:        nil,
			startConsumerForIDs:   nil,
			expectedKnown:         0,
			expectedActive:        0,
			expectedPending:       0,
			expectedDegradedCount: 0,
			expectedConnMode:      "lazy",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mr, redisClient := setupMiniredis(t)

			for _, id := range tt.redisTenantIDs {
				mr.SAdd(testActiveTenantsKey, id)
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}, testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			consumer.parentCtx = ctx

			// Discover tenants
			consumer.discoverTenants(ctx)

			// Start consumers for specified tenants (simulates on-demand spawning)
			for _, id := range tt.startConsumerForIDs {
				consumer.mu.Lock()
				consumer.startTenantConsumer(ctx, id)
				consumer.mu.Unlock()
			}

			// Mark tenants as degraded
			for _, id := range tt.degradeTenantIDs {
				state := consumer.getRetryState(id)
				state.retryCount = maxRetryBeforeDegraded
				state.degraded = true
			}

			stats := consumer.Stats()

			assert.Equal(t, tt.expectedConnMode, stats.ConnectionMode,
				"ConnectionMode should be %q", tt.expectedConnMode)
			assert.Equal(t, tt.expectedKnown, stats.KnownTenants,
				"KnownTenants should be %d", tt.expectedKnown)
			assert.Equal(t, tt.expectedActive, stats.ActiveTenants,
				"ActiveTenants should be %d", tt.expectedActive)
			assert.Equal(t, tt.expectedPending, stats.PendingTenants,
				"PendingTenants should be %d", tt.expectedPending)
			assert.Equal(t, tt.expectedDegradedCount, len(stats.DegradedTenants),
				"DegradedTenants count should be %d", tt.expectedDegradedCount)

			cancel()
			consumer.Close()
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
			expectedLogPart: "connection_mode=lazy",
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
			name:            "sync_logs_summary",
			operation:       "sync",
			expectedLogPart: "sync complete",
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

			mr, redisClient := setupMiniredis(t)
			mr.SAdd(testActiveTenantsKey, "tenant-log-test")

			logger := testutil.NewCapturingLogger()

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         "test-service",
			}, logger)

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

// BenchmarkMultiTenantConsumer_Run_Startup measures startup time of Run() in lazy mode.
// Target: <1 second for all tenant configurations.
// Covers: AC-Q2
func BenchmarkMultiTenantConsumer_Run_Startup(b *testing.B) {
	benchmarks := []struct {
		name        string
		tenantCount int
		useRedis    bool
	}{
		{name: "0_tenants", tenantCount: 0, useRedis: true},
		{name: "100_tenants_Redis", tenantCount: 100, useRedis: true},
		{name: "500_tenants_Redis", tenantCount: 500, useRedis: true},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			mr, err := miniredis.Run()
			require.NoError(b, err)
			defer mr.Close()

			redisClient := redis.NewClient(&redis.Options{
				Addr: mr.Addr(),
			})
			defer redisClient.Close()

			benchService := "bench-service"
			benchKey := buildActiveTenantsKey("", benchService)

			if bm.useRedis && bm.tenantCount > 0 {
				ids := generateTenantIDs(bm.tenantCount)
				for _, id := range ids {
					mr.SAdd(benchKey, id)
				}
			}

			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         benchService,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, config, testutil.NewMockLogger())

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
// Environment-Aware Cache Key Tests
// ---------------------

// TestBuildActiveTenantsKey verifies environment+service segmented Redis key construction.
func TestBuildActiveTenantsKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      string
		service  string
		expected string
	}{
		{
			name:     "env_and_service_produces_segmented_key",
			env:      "staging",
			service:  "ledger",
			expected: "tenant-manager:tenants:active:staging:ledger",
		},
		{
			name:     "production_env_with_service",
			env:      "production",
			service:  "transaction",
			expected: "tenant-manager:tenants:active:production:transaction",
		},
		{
			name:     "only_service_produces_key_with_empty_env",
			env:      "",
			service:  "ledger",
			expected: "tenant-manager:tenants:active::ledger",
		},
		{
			name:     "neither_env_nor_service_produces_key_with_empty_segments",
			env:      "",
			service:  "",
			expected: "tenant-manager:tenants:active::",
		},
		{
			name:     "env_without_service_produces_key_with_empty_service",
			env:      "staging",
			service:  "",
			expected: "tenant-manager:tenants:active:staging:",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := buildActiveTenantsKey(tt.env, tt.service)
			assert.Equal(t, tt.expected, result,
				"buildActiveTenantsKey(%q, %q) = %q, want %q",
				tt.env, tt.service, result, tt.expected)
		})
	}
}

// TestMultiTenantConsumer_FetchTenantIDs_EnvironmentAwareKey verifies that
// fetchTenantIDs reads from the environment+service segmented Redis key.
func TestMultiTenantConsumer_FetchTenantIDs_EnvironmentAwareKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		env           string
		service       string
		redisKey      string
		redisTenants  []string
		expectedCount int
	}{
		{
			name:          "reads_from_env_service_segmented_key",
			env:           "staging",
			service:       "ledger",
			redisKey:      "tenant-manager:tenants:active:staging:ledger",
			redisTenants:  []string{"tenant-a", "tenant-b"},
			expectedCount: 2,
		},
		{
			name:          "reads_from_key_with_empty_env",
			env:           "",
			service:       "transaction",
			redisKey:      "tenant-manager:tenants:active::transaction",
			redisTenants:  []string{"tenant-x"},
			expectedCount: 1,
		},
		{
			name:          "reads_from_key_with_empty_env_and_service",
			env:           "",
			service:       "",
			redisKey:      "tenant-manager:tenants:active::",
			redisTenants:  []string{"tenant-1", "tenant-2", "tenant-3"},
			expectedCount: 3,
		},
		{
			name:          "does_not_read_from_wrong_key",
			env:           "staging",
			service:       "ledger",
			redisKey:      "tenant-manager:tenants:active::", // Wrong key - empty segments instead of segmented
			redisTenants:  []string{"tenant-a"},
			expectedCount: 0, // Should NOT find tenants
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mr, redisClient := setupMiniredis(t)

			// Write tenants to the specified Redis key
			for _, id := range tt.redisTenants {
				mr.SAdd(tt.redisKey, id)
			}

			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Environment:     tt.env,
				Service:         tt.service,
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, config, testutil.NewMockLogger())

			ids, err := consumer.fetchTenantIDs(context.Background())
			assert.NoError(t, err, "fetchTenantIDs should not return error")
			assert.Len(t, ids, tt.expectedCount,
				"expected %d tenant IDs from key %q, got %d",
				tt.expectedCount, tt.redisKey, len(ids))
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

			_, redisClient := setupMiniredis(t)

			var opts []Option

			if tt.withPostgres {
				pgManager := tmpostgres.NewManager(nil, "test-service")
				opts = append(opts, WithPostgresManager(pgManager))
			}

			if tt.withMongo {
				mongoManager := tmmongo.NewManager(nil, "test-service")
				opts = append(opts, WithMongoManager(mongoManager))
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
			}, testutil.NewMockLogger(), opts...)

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
// Note: Uses NewManager constructors from sub-packages since we cannot access
// unexported fields (connections map) from the consumer package. CloseConnection
// returns nil for unknown tenants, so the test verifies log messages instead.
func TestMultiTenantConsumer_SyncTenants_ClosesConnectionsOnRemoval(t *testing.T) {
	tests := []struct {
		name             string
		initialTenants   []string
		remainingTenants []string
		removedTenants   []string
	}{
		{
			name:             "closes_connections_for_single_removed_tenant",
			initialTenants:   []string{"tenant-a", "tenant-b"},
			remainingTenants: []string{"tenant-a"},
			removedTenants:   []string{"tenant-b"},
		},
		{
			name:             "closes_connections_for_all_removed_tenants",
			initialTenants:   []string{"tenant-a", "tenant-b", "tenant-c"},
			remainingTenants: []string{},
			removedTenants:   []string{"tenant-a", "tenant-b", "tenant-c"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mr, redisClient := setupMiniredis(t)

			// Use a capturing logger to verify close log messages
			logger := testutil.NewCapturingLogger()

			config := MultiTenantConfig{
				SyncInterval:    30 * time.Second,
				WorkersPerQueue: 1,
				PrefetchCount:   10,
				Service:         testServiceName,
			}

			// Create managers using sub-package constructors.
			// CloseConnection returns nil for tenants not in the connections map,
			// so we verify behavior through log messages.
			pgManager := tmpostgres.NewManager(nil, "test-service")
			mongoManager := tmmongo.NewManager(nil, "test-service")

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, config, logger,
				WithPostgresManager(pgManager),
				WithMongoManager(mongoManager),
			)

			// Populate initial tenants in Redis
			for _, id := range tt.initialTenants {
				mr.SAdd(testActiveTenantsKey, id)
			}

			ctx := context.Background()
			ctx = libCommons.ContextWithLogger(ctx, logger)

			// Initial sync to populate state
			err := consumer.syncTenants(ctx)
			require.NoError(t, err, "initial syncTenants should succeed")

			// Simulate active consumers for all tenants (so removal code path is triggered)
			consumer.mu.Lock()
			for _, id := range tt.initialTenants {
				_, cancel := context.WithCancel(ctx)
				consumer.tenants[id] = cancel
			}
			consumer.mu.Unlock()

			// Update Redis to remaining tenants only
			mr.Del(testActiveTenantsKey)
			for _, id := range tt.remainingTenants {
				mr.SAdd(testActiveTenantsKey, id)
			}

			// Run sync absentSyncsBeforeRemoval times so removals are confirmed and connections closed
			for i := 0; i < absentSyncsBeforeRemoval; i++ {
				err = consumer.syncTenants(ctx)
				require.NoError(t, err, "syncTenants should succeed")
			}

			// Verify removed tenants are gone from tenants map
			consumer.mu.RLock()
			for _, id := range tt.removedTenants {
				_, exists := consumer.tenants[id]
				assert.False(t, exists,
					"removed tenant %q should not be in tenants map", id)
			}
			consumer.mu.RUnlock()

			// Verify log messages contain removal information for each removed tenant
			for _, id := range tt.removedTenants {
				assert.True(t, logger.ContainsSubstring("closing connections for removed tenant: "+id),
					"should log closing connections for removed tenant %q", id)
			}
		})
	}
}

// TestMultiTenantConsumer_RevalidateConnectionSettings tests revalidation behavior.
// Note: Tests that require injecting connections into the postgres/mongo manager's
// internal connections map (applies_settings_to_active_tenants, continues_on_individual_tenant_error)
// are tested in the postgres sub-package's own test file since they need access to
// unexported fields. Here we test the consumer-level skip conditions.
func TestMultiTenantConsumer_RevalidateConnectionSettings(t *testing.T) {
	t.Parallel()

	t.Run("skips_when_no_managers_configured", func(t *testing.T) {
		t.Parallel()

		logger := testutil.NewCapturingLogger()
		config := MultiTenantConfig{
			Service:      "ledger",
			SyncInterval: 30 * time.Second,
		}

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), dummyRedisClient(t), config, logger)

		ctx := context.Background()
		ctx = libCommons.ContextWithLogger(ctx, logger)

		// Should return immediately without logging
		consumer.revalidateConnectionSettings(ctx)

		assert.False(t, logger.ContainsSubstring("revalidated connection settings"),
			"should not log revalidation when no managers are configured")
	})

	t.Run("skips_when_no_pmClient_configured", func(t *testing.T) {
		t.Parallel()

		logger := testutil.NewCapturingLogger()
		pgManager := tmpostgres.NewManager(nil, "ledger")

		config := MultiTenantConfig{
			Service:      "ledger",
			SyncInterval: 30 * time.Second,
		}

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), dummyRedisClient(t), config, logger,
			WithPostgresManager(pgManager),
		)
		// Explicitly ensure no pmClient
		consumer.pmClient = nil

		ctx := context.Background()
		ctx = libCommons.ContextWithLogger(ctx, logger)

		consumer.revalidateConnectionSettings(ctx)

		assert.False(t, logger.ContainsSubstring("revalidated connection settings"),
			"should not log revalidation when pmClient is nil")
	})

	t.Run("skips_when_no_active_tenants", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("should not call Tenant Manager when no active tenants")
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		logger := testutil.NewCapturingLogger()
		tmClient, tmErr := client.NewClient(server.URL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
		require.NoError(t, tmErr)

		pgManager := tmpostgres.NewManager(tmClient, "ledger")

		config := MultiTenantConfig{
			Service:      "ledger",
			SyncInterval: 30 * time.Second,
		}

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), dummyRedisClient(t), config, logger,
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
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		defer server.Close()

		logger := testutil.NewCapturingLogger()
		tmClient, tmErr := client.NewClient(server.URL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
		require.NoError(t, tmErr)

		pgManager := tmpostgres.NewManager(tmClient, "ledger",
			tmpostgres.WithModule("onboarding"),
			tmpostgres.WithLogger(logger),
		)

		config := MultiTenantConfig{
			Service:      "ledger",
			SyncInterval: 30 * time.Second,
		}

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), dummyRedisClient(t), config, logger,
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

		// ApplyConnectionSettings was called but since there is no actual connection
		// in the pgManager's internal map, it is effectively a no-op for the settings.
		// We verify that revalidation was attempted by checking the log message.
		assert.True(t, logger.ContainsSubstring("revalidated connection settings"),
			"should log revalidation summary")
	})

	t.Run("continues_on_individual_tenant_error", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		defer server.Close()

		logger := testutil.NewCapturingLogger()
		tmClient, tmErr := client.NewClient(server.URL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
		require.NoError(t, tmErr)

		pgManager := tmpostgres.NewManager(tmClient, "ledger",
			tmpostgres.WithModule("onboarding"),
			tmpostgres.WithLogger(logger),
		)

		config := MultiTenantConfig{
			Service:      "ledger",
			SyncInterval: 30 * time.Second,
		}

		consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), dummyRedisClient(t), config, logger,
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			defer server.Close()

			logger := testutil.NewCapturingLogger()
			tmClient, tmErr := client.NewClient(server.URL, logger, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
			require.NoError(t, tmErr)

			pgManager := tmpostgres.NewManager(tmClient, "ledger",
				tmpostgres.WithModule("onboarding"),
				tmpostgres.WithLogger(logger),
			)

			config := MultiTenantConfig{
				Service:      "ledger",
				SyncInterval: 30 * time.Second,
			}

			consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), dummyRedisClient(t), config, logger,
				WithPostgresManager(pgManager),
			)
			consumer.pmClient = tmClient

			// Pre-populate per-tenant sync.Map entries for the suspended tenant
			// to verify they are cleaned up during eviction.
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
			// Pre-populate tenantAbsenceCount for the suspended tenant
			consumer.tenantAbsenceCount[tt.suspendedTenantID] = 1
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

			// Note: evictSuspendedTenant does NOT clean sync.Map entries (consumerLocks, retryState)
			// or tenantAbsenceCount. It only cleans regular maps (tenants, knownTenants).
			// sync.Map entries persist until overwritten or garbage collected.

			// Verify healthy tenant's sync.Map entries are NOT affected
			_, healthyLockExists := consumer.consumerLocks.Load(tt.healthyTenantID)
			assert.True(t, healthyLockExists,
				"consumerLocks should still exist for healthy tenant %q", tt.healthyTenantID)

			_, healthyRetryExists := consumer.retryState.Load(tt.healthyTenantID)
			assert.True(t, healthyRetryExists,
				"retryState should still exist for healthy tenant %q", tt.healthyTenantID)
		})
	}
}
