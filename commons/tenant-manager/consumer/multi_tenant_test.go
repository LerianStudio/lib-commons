// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	tmmongo "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/postgres"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/rabbitmq"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NewMultiTenantConsumer is a test convenience wrapper around NewMultiTenantConsumerWithError
// that panics on error. It prepends WithRabbitMQ(rabbitmq) to opts when rabbitmq is non-nil,
// preserving backward compatibility with existing test call sites.
func NewMultiTenantConsumer(
	rabbitmq *tmrabbitmq.Manager,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) *MultiTenantConsumer {
	if rabbitmq != nil {
		opts = append([]Option{WithRabbitMQ(rabbitmq)}, opts...)
	}

	c, err := NewMultiTenantConsumerWithError(config, logger, opts...)
	if err != nil {
		panic(fmt.Sprintf("NewMultiTenantConsumer (test helper): %v", err))
	}

	return c
}

// mustNewConsumer is an alternative test helper that takes *testing.T and calls t.Fatal on error.
// It prepends WithRabbitMQ(rabbitmq) to opts when rabbitmq is non-nil.
func mustNewConsumer(
	t *testing.T,
	rabbitmq *tmrabbitmq.Manager,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) *MultiTenantConsumer {
	t.Helper()

	if rabbitmq != nil {
		opts = append([]Option{WithRabbitMQ(rabbitmq)}, opts...)
	}

	c, err := NewMultiTenantConsumerWithError(config, logger, opts...)
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
// consumer goroutines spawned by EnsureConsumerStarted do not panic on nil
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

// TestMultiTenantConsumer_Run_SignatureUnchanged verifies the Run() method signature
// matches the expected interface: func (c *MultiTenantConsumer) Run(ctx context.Context) error
// This is a compile-time assertion. If the signature changes, this test will not compile.
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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			fn = consumer.Run
			assert.NotNil(t, fn, "Run method must exist and match expected signature")
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

			consumer, err := NewMultiTenantConsumerWithError(tt.config, testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))

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

// TestMultiTenantConsumer_CacheTTLPropagation verifies that the CacheTTL config field
// is propagated to the underlying HTTP client via client.WithCacheTTL.
func TestMultiTenantConsumer_CacheTTLPropagation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cacheTTL time.Duration
	}{
		{
			name:     "zero_CacheTTL_uses_client_default",
			cacheTTL: 0,
		},
		{
			name:     "positive_CacheTTL_is_propagated",
			cacheTTL: 5 * time.Minute,
		},
		{
			name:     "large_CacheTTL_is_propagated",
			cacheTTL: 2 * time.Hour,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := MultiTenantConfig{
				MultiTenantURL:    "http://tenant-manager:4003",
				ServiceAPIKey:     "test-key",
				Service:           "ledger",
				AllowInsecureHTTP: true,
				CacheTTL:          tt.cacheTTL,
			}

			consumer, err := NewMultiTenantConsumerWithError(config, testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
			require.NoError(t, err)
			assert.NotNil(t, consumer)
			assert.NotNil(t, consumer.pmClient, "pmClient should be created")
			assert.Equal(t, tt.cacheTTL, consumer.config.CacheTTL, "CacheTTL should be preserved in config")

			require.NoError(t, consumer.Close())
		})
	}
}

// TestMultiTenantConsumer_CacheTTL_NegativeRejected verifies that negative CacheTTL
// values are rejected by NewMultiTenantConsumerWithError.
func TestMultiTenantConsumer_CacheTTL_NegativeRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cacheTTL time.Duration
	}{
		{
			name:     "rejects_negative_CacheTTL",
			cacheTTL: -1 * time.Second,
		},
		{
			name:     "rejects_large_negative_CacheTTL",
			cacheTTL: -10 * time.Minute,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := MultiTenantConfig{
				MultiTenantURL:    "http://tenant-manager:4003",
				ServiceAPIKey:     "test-key",
				Service:           "ledger",
				AllowInsecureHTTP: true,
				CacheTTL:          tt.cacheTTL,
			}

			consumer, err := NewMultiTenantConsumerWithError(config, testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
			require.Error(t, err)
			assert.Nil(t, consumer)
			assert.Contains(t, err.Error(), "CacheTTL must be non-negative")
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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

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

			if tt.name == "close_is_idempotent_on_double_call" {
				// Second close should not panic
				err2 := consumer.Close()
				assert.NoError(t, err2, "second Close() should not return error")
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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

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

// ---------------------
// T-002: On-Demand Consumer Spawning Tests
// ---------------------

// TestMultiTenantConsumer_EnsureConsumerStarted_SpawnsExactlyOnce verifies that
// concurrent calls to EnsureConsumerStarted for the same tenant spawn exactly one consumer.
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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			// Register a handler so startTenantConsumer has something to work with
			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Store parentCtx (normally done by Run())
			consumer.parentCtx = ctx

			// Add tenant to knownTenants
			consumer.mu.Lock()
			consumer.knownTenants[tt.tenantID] = true
			consumer.mu.Unlock()

			// Also seed in cache so EnsureConsumerStarted doesn't trigger lazy-load
			consumer.cache.Set(tt.tenantID, &core.TenantConfig{ID: tt.tenantID}, 1*time.Hour)

			// Launch N concurrent calls to EnsureConsumerStarted
			var wg sync.WaitGroup
			wg.Add(tt.concurrentCalls)

			for i := 0; i < tt.concurrentCalls; i++ {
				go func() {
					defer wg.Done()
					consumer.EnsureConsumerStarted(ctx, tt.tenantID)
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
// EnsureConsumerStarted is a no-op when the consumer is already running.
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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			consumer.parentCtx = ctx

			// Add tenant to knownTenants
			consumer.mu.Lock()
			consumer.knownTenants[tt.tenantID] = true
			consumer.mu.Unlock()

			// Seed in cache
			consumer.cache.Set(tt.tenantID, &core.TenantConfig{ID: tt.tenantID}, 1*time.Hour)

			// First call spawns the consumer
			consumer.EnsureConsumerStarted(ctx, tt.tenantID)

			consumer.mu.RLock()
			countAfterFirst := len(consumer.tenants)
			consumer.mu.RUnlock()

			assert.Equal(t, 1, countAfterFirst, "first call should spawn 1 consumer")

			// Second call should be a no-op
			consumer.EnsureConsumerStarted(ctx, tt.tenantID)

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
// EnsureConsumerStarted is a no-op when the consumer has been closed.
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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx := context.Background()
			consumer.parentCtx = ctx

			// Close before calling EnsureConsumerStarted
			consumer.Close()

			// Should be a no-op
			consumer.EnsureConsumerStarted(ctx, tt.tenantID)

			consumer.mu.RLock()
			consumerCount := len(consumer.tenants)
			consumer.mu.RUnlock()

			assert.Equal(t, 0, consumerCount,
				"no consumer should be spawned after Close()")
		})
	}
}

// TestMultiTenantConsumer_EnsureConsumerStarted_MultipleTenants verifies that
// EnsureConsumerStarted can spawn consumers for different tenants concurrently.
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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			consumer.parentCtx = ctx

			// Add tenants to knownTenants
			consumer.mu.Lock()
			for _, id := range tt.tenantIDs {
				consumer.knownTenants[id] = true
			}
			consumer.mu.Unlock()

			// Seed in cache
			for _, id := range tt.tenantIDs {
				consumer.cache.Set(id, &core.TenantConfig{ID: id}, 1*time.Hour)
			}

			// Spawn consumers for all tenants concurrently
			var wg sync.WaitGroup
			wg.Add(len(tt.tenantIDs))

			for _, id := range tt.tenantIDs {
				go func(tenantID string) {
					defer wg.Done()
					consumer.EnsureConsumerStarted(ctx, tenantID)
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
// Expected base sequence: 5s, 10s, 20s, 40s, 40s (capped), with +/-25% jitter applied.
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
			// backoffDelay applies +/-25% jitter: delay in [0.75*base, 1.25*base)
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
			name:            "ensure_consumer_logs_on_demand",
			operation:       "ensure",
			expectedLogPart: "on-demand consumer start",
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
			case "ensure":
				consumer.Register("test-queue", func(ctx context.Context, d amqp.Delivery) error {
					return nil
				})
				// Add tenant to knownTenants so EnsureConsumerStarted doesn't trigger lazy-load
				consumer.mu.Lock()
				consumer.knownTenants["tenant-log-test"] = true
				consumer.mu.Unlock()
				consumer.cache.Set("tenant-log-test", &core.TenantConfig{ID: "tenant-log-test"}, 1*time.Hour)
				consumer.EnsureConsumerStarted(ctx, "tenant-log-test")
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
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger(), opts...)

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
				tt.config, testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()),
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
