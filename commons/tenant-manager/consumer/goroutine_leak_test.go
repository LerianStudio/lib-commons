package consumer

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"go.uber.org/goleak"
)

// TestMultiTenantConsumer_Run_CloseStopsListener proves that Close() alone
// (without cancelling the original context) stops the event listener goroutine.
// This prevents goroutine leaks when callers pass context.Background().
func TestMultiTenantConsumer_Run_CloseStopsListener(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rc.Close() })

	server := setupTenantManagerAPIServer(t, makeTenantSummaries(1))
	config := newTestConfig(server.URL)

	consumer, err := NewMultiTenantConsumerWithError(
		dummyRabbitMQManager(),
		rc,
		config,
		testutil.NewMockLogger(),
	)
	require.NoError(t, err)

	// Use context.Background() -- never cancelled, like Midaz does in production.
	ctx := context.Background()

	err = consumer.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	// Close without cancelling ctx -- this must stop the event listener.
	if closeErr := consumer.Close(); closeErr != nil {
		t.Fatalf("Close() returned unexpected error: %v", closeErr)
	}

	assert.Eventually(t, func() bool {
		return consumer.Stats().Closed && consumer.Stats().ActiveTenants == 0
	}, time.Second, 20*time.Millisecond)

	goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/cache.(*InMemoryCache).cleanupLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
	)
}

// TestMultiTenantConsumer_Run_CancelAndCloseNoLeak proves that the normal
// cleanup path (cancel context + Close) also leaves no leaked goroutines.
func TestMultiTenantConsumer_Run_CancelAndCloseNoLeak(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rc.Close() })

	server := setupTenantManagerAPIServer(t, makeTenantSummaries(1))
	config := newTestConfig(server.URL)

	consumer, err := NewMultiTenantConsumerWithError(
		dummyRabbitMQManager(),
		rc,
		config,
		testutil.NewMockLogger(),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	err = consumer.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	// Normal cleanup: cancel context first, then Close.
	cancel()

	if closeErr := consumer.Close(); closeErr != nil {
		t.Fatalf("Close() returned unexpected error: %v", closeErr)
	}

	assert.Eventually(t, func() bool {
		return consumer.Stats().Closed && consumer.Stats().ActiveTenants == 0
	}, time.Second, 20*time.Millisecond)

	goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/cache.(*InMemoryCache).cleanupLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
	)
}
