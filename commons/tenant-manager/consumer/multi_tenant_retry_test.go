package consumer

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

func applyRetryFailures(state *retryStateEntry, count int) {
	for range count {
		_, _, _ = state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
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
		{name: "initial_retry_state_is_zero", tenantID: "tenant-fresh", incrementRetries: 0, expectedDegraded: false},
		{name: "2_retries_not_degraded", tenantID: "tenant-2-retries", incrementRetries: 2, expectedDegraded: false},
		{name: "3_retries_marks_degraded", tenantID: "tenant-3-retries", incrementRetries: 3, expectedDegraded: true},
		{name: "5_retries_stays_degraded", tenantID: "tenant-5-retries", incrementRetries: 5, expectedDegraded: true},
		{name: "reset_clears_retry_state", tenantID: "tenant-reset", incrementRetries: 5, resetBeforeAssert: true, expectedDegraded: false},
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
			applyRetryFailures(state, tt.incrementRetries)

			if tt.resetBeforeAssert {
				consumer.resetRetryState(tt.tenantID)
			}

			assert.Equal(t, tt.expectedDegraded, consumer.IsDegraded(tt.tenantID))
		})
	}
}

// TestMultiTenantConsumer_RetryStateIsolation verifies that retry state is
// isolated between tenants (one tenant's failures don't affect another).
func TestMultiTenantConsumer_RetryStateIsolation(t *testing.T) {
	t.Parallel()

	_, redisClient := setupMiniredis(t)

	consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
		SyncInterval:    30 * time.Second,
		WorkersPerQueue: 1,
		PrefetchCount:   10,
	}, testutil.NewMockLogger())

	applyRetryFailures(consumer.getRetryState("tenant-a"), 5)
	_ = consumer.getRetryState("tenant-b")

	assert.True(t, consumer.IsDegraded("tenant-a"))
	assert.False(t, consumer.IsDegraded("tenant-b"))
}

// TestMultiTenantConsumer_Stats_Enhanced verifies the enhanced Stats() API
// returns ConnectionMode, KnownTenants, PendingTenants, and DegradedTenants.
func TestMultiTenantConsumer_Stats_Enhanced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		redisTenantIDs        []string
		startConsumerForIDs   []string
		degradeTenantIDs      []string
		eagerStart            bool
		expectedKnown         int
		expectedActive        int
		expectedPending       int
		expectedDegradedCount int
		expectedConnMode      string
	}{
		{name: "all_tenants_pending_in_lazy_mode", redisTenantIDs: []string{"tenant-a", "tenant-b", "tenant-c"}, expectedKnown: 3, expectedActive: 0, expectedPending: 3, expectedDegradedCount: 0, expectedConnMode: "lazy"},
		{name: "mix_of_active_and_pending", redisTenantIDs: []string{"tenant-a", "tenant-b", "tenant-c"}, startConsumerForIDs: []string{"tenant-a"}, expectedKnown: 3, expectedActive: 1, expectedPending: 2, expectedDegradedCount: 0, expectedConnMode: "lazy"},
		{name: "degraded_tenant_appears_in_stats", redisTenantIDs: []string{"tenant-a", "tenant-b"}, degradeTenantIDs: []string{"tenant-b"}, expectedKnown: 2, expectedActive: 0, expectedPending: 2, expectedDegradedCount: 1, expectedConnMode: "lazy"},
		{name: "empty_consumer_returns_zero_stats", expectedKnown: 0, expectedActive: 0, expectedPending: 0, expectedDegradedCount: 0, expectedConnMode: "lazy"},
		{name: "eager_mode_reports_connection_mode", redisTenantIDs: []string{"tenant-a"}, eagerStart: true, expectedKnown: 1, expectedActive: 0, expectedPending: 1, expectedDegradedCount: 0, expectedConnMode: "eager"},
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
				Service:         testServiceName,
				EagerStart:      tt.eagerStart,
			}, testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			consumer.parentCtx = ctx
			consumer.discoverTenants(ctx)

			for _, id := range tt.startConsumerForIDs {
				consumer.mu.Lock()
				consumer.startTenantConsumer(ctx, id)
				consumer.mu.Unlock()
			}

			for _, id := range tt.degradeTenantIDs {
				applyRetryFailures(consumer.getRetryState(id), maxRetryBeforeDegraded)
			}

			stats := consumer.Stats()

			assert.Equal(t, tt.expectedConnMode, stats.ConnectionMode)
			assert.Equal(t, tt.expectedKnown, stats.KnownTenants)
			assert.Equal(t, tt.expectedActive, stats.ActiveTenants)
			assert.Equal(t, tt.expectedPending, stats.PendingTenants)
			assert.Equal(t, tt.expectedDegradedCount, len(stats.DegradedTenants))

			consumer.Close()
		})
	}
}
