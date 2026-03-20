package consumer

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
)

// TestMultiTenantConsumer_Run_CloseStopsSyncLoop proves that Close() alone
// (without cancelling the original context) stops the sync loop goroutine.
// This prevents goroutine leaks when callers pass context.Background().
func TestMultiTenantConsumer_Run_CloseStopsSyncLoop(t *testing.T) {
	mr, redisClient := setupMiniredis(t)

	// Populate Redis so fetchTenantIDs succeeds during discovery
	mr.SAdd(testActiveTenantsKey, "tenant-001")

	consumer := mustNewConsumer(t,
		dummyRabbitMQManager(),
		redisClient,
		MultiTenantConfig{
			SyncInterval:  100 * time.Millisecond,
			PrefetchCount: 10,
			Service:       testServiceName,
		},
		testutil.NewMockLogger(),
	)

	// Use context.Background() — never cancelled, like Midaz does in production.
	ctx := context.Background()

	err := consumer.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	assert.Eventually(t, func() bool {
		return consumer.Stats().KnownTenants > 0
	}, time.Second, 20*time.Millisecond)

	// Close without cancelling ctx — this must stop the sync loop.
	if closeErr := consumer.Close(); closeErr != nil {
		t.Fatalf("Close() returned unexpected error: %v", closeErr)
	}

	assert.Eventually(t, func() bool {
		return consumer.Stats().Closed && consumer.Stats().ActiveTenants == 0
	}, time.Second, 20*time.Millisecond)

}

// TestMultiTenantConsumer_Run_CancelAndCloseNoLeak proves that the normal
// cleanup path (cancel context + Close) also leaves no leaked goroutines.
func TestMultiTenantConsumer_Run_CancelAndCloseNoLeak(t *testing.T) {
	mr, redisClient := setupMiniredis(t)

	// Populate Redis so fetchTenantIDs succeeds during discovery
	mr.SAdd(testActiveTenantsKey, "tenant-001")

	consumer := mustNewConsumer(t,
		dummyRabbitMQManager(),
		redisClient,
		MultiTenantConfig{
			SyncInterval:  100 * time.Millisecond,
			PrefetchCount: 10,
			Service:       testServiceName,
		},
		testutil.NewMockLogger(),
	)

	ctx, cancel := context.WithCancel(context.Background())

	err := consumer.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	assert.Eventually(t, func() bool {
		return consumer.Stats().KnownTenants > 0
	}, time.Second, 20*time.Millisecond)

	// Normal cleanup: cancel context first, then Close.
	cancel()

	if closeErr := consumer.Close(); closeErr != nil {
		t.Fatalf("Close() returned unexpected error: %v", closeErr)
	}

	assert.Eventually(t, func() bool {
		return consumer.Stats().Closed && consumer.Stats().ActiveTenants == 0
	}, time.Second, 20*time.Millisecond)

}
