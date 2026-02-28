package consumer

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/internal/testutil"
	"go.uber.org/goleak"
)

// TestMultiTenantConsumer_Run_CloseStopsSyncLoop proves that Close() alone
// (without cancelling the original context) stops the sync loop goroutine.
// This prevents goroutine leaks when callers pass context.Background().
func TestMultiTenantConsumer_Run_CloseStopsSyncLoop(t *testing.T) {
	mr, redisClient := setupMiniredis(t)

	// Populate Redis so fetchTenantIDs succeeds during discovery
	mr.SAdd(testActiveTenantsKey, "tenant-001")

	consumer := NewMultiTenantConsumer(
		dummyRabbitMQManager(),
		redisClient,
		MultiTenantConfig{
			SyncInterval: 100 * time.Millisecond,
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

	// Let the sync loop goroutine start and run at least one tick.
	time.Sleep(250 * time.Millisecond)

	// Close without cancelling ctx — this must stop the sync loop.
	if closeErr := consumer.Close(); closeErr != nil {
		t.Fatalf("Close() returned unexpected error: %v", closeErr)
	}

	// Give goroutines time to wind down.
	time.Sleep(200 * time.Millisecond)

	goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("github.com/alicebob/miniredis/v2/server.(*Server).servePeer"),
		goleak.IgnoreTopFunction("github.com/alicebob/miniredis/v2.(*Miniredis).handleClient"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}

// TestMultiTenantConsumer_Run_CancelAndCloseNoLeak proves that the normal
// cleanup path (cancel context + Close) also leaves no leaked goroutines.
func TestMultiTenantConsumer_Run_CancelAndCloseNoLeak(t *testing.T) {
	mr, redisClient := setupMiniredis(t)

	// Populate Redis so fetchTenantIDs succeeds during discovery
	mr.SAdd(testActiveTenantsKey, "tenant-001")

	consumer := NewMultiTenantConsumer(
		dummyRabbitMQManager(),
		redisClient,
		MultiTenantConfig{
			SyncInterval: 100 * time.Millisecond,
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

	// Let the sync loop goroutine start.
	time.Sleep(250 * time.Millisecond)

	// Normal cleanup: cancel context first, then Close.
	cancel()

	if closeErr := consumer.Close(); closeErr != nil {
		t.Fatalf("Close() returned unexpected error: %v", closeErr)
	}

	// Give goroutines time to wind down.
	time.Sleep(200 * time.Millisecond)

	goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("github.com/alicebob/miniredis/v2/server.(*Server).servePeer"),
		goleak.IgnoreTopFunction("github.com/alicebob/miniredis/v2.(*Miniredis).handleClient"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}
