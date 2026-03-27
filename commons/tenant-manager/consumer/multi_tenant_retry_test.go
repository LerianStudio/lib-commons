// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
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

			server := setupTenantManagerAPIServer(t, nil)
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

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

	server := setupTenantManagerAPIServer(t, nil)
	consumer := mustNewConsumer(t, dummyRabbitMQManager(), newTestConfig(server.URL), testutil.NewMockLogger())

	applyRetryFailures(consumer.getRetryState("tenant-a"), 5)
	_ = consumer.getRetryState("tenant-b")

	assert.True(t, consumer.IsDegraded("tenant-a"))
	assert.False(t, consumer.IsDegraded("tenant-b"))
}

// TestMultiTenantConsumer_Stats_Enhanced verifies the enhanced Stats() API
// returns KnownTenants, PendingTenants, and DegradedTenants.
func TestMultiTenantConsumer_Stats_Enhanced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		knownTenantIDs        []string
		startConsumerForIDs   []string
		degradeTenantIDs      []string
		expectedKnown         int
		expectedActive        int
		expectedPending       int
		expectedDegradedCount int
	}{
		{name: "all_tenants_pending", knownTenantIDs: []string{"t-0", "t-1", "t-2"}, expectedKnown: 3, expectedActive: 0, expectedPending: 3, expectedDegradedCount: 0},
		{name: "mix_of_active_and_pending", knownTenantIDs: []string{"t-0", "t-1", "t-2"}, startConsumerForIDs: []string{"t-0"}, expectedKnown: 3, expectedActive: 1, expectedPending: 2, expectedDegradedCount: 0},
		{name: "degraded_tenant_appears_in_stats", knownTenantIDs: []string{"t-0", "t-1"}, degradeTenantIDs: []string{"t-1"}, expectedKnown: 2, expectedActive: 0, expectedPending: 2, expectedDegradedCount: 1},
		{name: "empty_consumer_returns_zero_stats", knownTenantIDs: []string{}, expectedKnown: 0, expectedActive: 0, expectedPending: 0, expectedDegradedCount: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			config := newTestConfig(server.URL)
			consumer := mustNewConsumer(t, dummyRabbitMQManager(), config, testutil.NewMockLogger())

			consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
				return nil
			})

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			consumer.parentCtx = ctx

			// Pre-populate knownTenants manually
			consumer.mu.Lock()
			for _, id := range tt.knownTenantIDs {
				consumer.knownTenants[id] = true
			}
			consumer.mu.Unlock()

			for _, id := range tt.startConsumerForIDs {
				consumer.mu.Lock()
				consumer.startTenantConsumer(ctx, id)
				consumer.mu.Unlock()
			}

			for _, id := range tt.degradeTenantIDs {
				applyRetryFailures(consumer.getRetryState(id), maxRetryBeforeDegraded)
			}

			stats := consumer.Stats()

			assert.Equal(t, tt.expectedKnown, stats.KnownTenants)
			assert.Equal(t, tt.expectedActive, stats.ActiveTenants)
			assert.Equal(t, tt.expectedPending, stats.PendingTenants)
			assert.Equal(t, tt.expectedDegradedCount, len(stats.DegradedTenants))

			consumer.Close()
		})
	}
}

// TestMultiTenantConsumer_Stats_Enhanced_WithAPI verifies Stats using tenant summaries from API.
func TestMultiTenantConsumer_Stats_Enhanced_WithAPI(t *testing.T) {
	t.Parallel()

	apiTenants := []*client.TenantSummary{
		{ID: "t-0", Name: "T0", Status: "active"},
		{ID: "t-1", Name: "T1", Status: "active"},
		{ID: "t-2", Name: "T2", Status: "active"},
	}

	server := setupTenantManagerAPIServer(t, apiTenants)
	config := newTestConfig(server.URL)
	consumer := mustNewConsumer(t, dummyRabbitMQManager(), config, testutil.NewMockLogger())

	consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer.parentCtx = ctx

	// Pre-populate known tenants (simulating event-based discovery)
	consumer.mu.Lock()
	for _, ts := range apiTenants {
		consumer.knownTenants[ts.ID] = true
	}
	consumer.mu.Unlock()

	stats := consumer.Stats()
	assert.Equal(t, 3, stats.KnownTenants)
	assert.Equal(t, 0, stats.ActiveTenants)
	assert.Equal(t, 3, stats.PendingTenants)

	consumer.Close()
}
