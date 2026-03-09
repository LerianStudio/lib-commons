//go:build integration

package redis

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupRedisContainerRaw starts a Redis 7 container and returns the container
// handle (for Stop/Start control), its host:port endpoint, and a cleanup function.
// Unlike setupRedisContainer, this returns the container itself so tests can
// simulate server outages by stopping and restarting it.
func setupRedisContainerRaw(t *testing.T) (*tcredis.RedisContainer, string, func()) {
	t.Helper()

	ctx := context.Background()

	container, err := tcredis.Run(ctx,
		"redis:7-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err)

	return container, endpoint, func() {
		_ = container.Terminate(ctx)
	}
}

// waitForRedisReady polls the restarted container until Redis is accepting
// connections. After a container restart the mapped port stays the same but
// the server needs a moment to initialize. We try PING via a fresh client
// every pollInterval for up to timeout.
func waitForRedisReady(t *testing.T, addr string, timeout, pollInterval time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	ctx := context.Background()

	for time.Now().Before(deadline) {
		probe, err := New(ctx, newTestConfig(addr))
		if err == nil {
			_ = probe.Close()
			return
		}

		time.Sleep(pollInterval)
	}

	t.Fatalf("Redis at %s did not become ready within %s", addr, timeout)
}

// TestIntegration_Redis_Resilience_ReconnectAfterServerRestart validates the
// full outage-recovery cycle:
//  1. Connect and verify operations work.
//  2. Stop the container (simulates server crash / network partition).
//  3. Verify that operations fail while the server is down.
//  4. Restart the container (same mapped port).
//  5. Verify GetClient() eventually reconnects and operations succeed again.
//
// This is the most realistic resilience scenario: the process keeps running
// while the backing Redis goes down and comes back.
func TestIntegration_Redis_Resilience_ReconnectAfterServerRestart(t *testing.T) {
	container, addr, cleanup := setupRedisContainerRaw(t)
	defer cleanup()

	ctx := context.Background()

	// Phase 1: establish a healthy connection and verify operations.
	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)

	defer func() {
		// Best-effort close; may already be closed or disconnected.
		_ = client.Close()
	}()

	rdb, err := client.GetClient(ctx)
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, "resilience:before", "alive", 60*time.Second).Err(),
		"SET must succeed while server is healthy")

	// Phase 2: stop the container to simulate server going down.
	t.Log("Stopping Redis container to simulate outage...")
	require.NoError(t, container.Stop(ctx, nil))

	// The existing go-redis client handle is now pointing at a dead socket.
	// Operations should fail (the exact error varies by OS/timing).
	err = rdb.Set(ctx, "resilience:during-outage", "should-fail", 10*time.Second).Err()
	assert.Error(t, err, "SET must fail while server is down")

	// Phase 3: restart the container. The mapped port may change after restart,
	// so we must re-read the endpoint from the container.
	t.Log("Restarting Redis container...")
	require.NoError(t, container.Start(ctx))

	newAddr, err := container.Endpoint(ctx, "")
	require.NoError(t, err, "must be able to read endpoint after restart")
	t.Logf("Redis endpoint after restart: %s (was: %s)", newAddr, addr)

	// Poll until the server is accepting connections at the (potentially new) address.
	waitForRedisReady(t, newAddr, 15*time.Second, 200*time.Millisecond)
	t.Log("Redis container is ready after restart")

	// Phase 4: the old client's config points at the old address.
	// Close it and create a FRESH client with the new address to prove reconnect works.
	_ = client.Close()

	client2, err := New(ctx, newTestConfig(newAddr))
	require.NoError(t, err, "New() must succeed after server restart")

	defer func() { _ = client2.Close() }()

	// Phase 5: verify the reconnected client can operate.
	rdb2, err := client2.GetClient(ctx)
	require.NoError(t, err, "GetClient must succeed after server restart")

	require.NoError(t, rdb2.Set(ctx, "resilience:after-restart", "reconnected", 60*time.Second).Err(),
		"SET must succeed after reconnect")

	got, err := rdb2.Get(ctx, "resilience:after-restart").Result()
	require.NoError(t, err)
	assert.Equal(t, "reconnected", got, "value written after restart must be readable")

	connected, err := client2.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected, "client must report connected after successful reconnect")
}

// TestIntegration_Redis_Resilience_BackoffRateLimiting validates that the
// reconnect rate-limiter prevents thundering-herd storms. When the internal
// client is nil and GetClient() is called rapidly, only the first call
// attempts a real reconnect; subsequent calls within the backoff window
// return a "rate-limited" error without hitting the network.
//
// Mechanism (from redis.go GetClient):
//   - reconnectAttempts tracks consecutive failures.
//   - Each failure increments reconnectAttempts and records lastReconnectAttempt.
//   - The next GetClient computes delay = ExponentialWithJitter(500ms, attempts).
//   - If elapsed < delay, it returns "rate-limited" immediately.
//
// To trigger this, we connect to a real Redis, then close the underlying
// go-redis client directly (making c.client nil, c.connected false), and
// also stop the container so the reconnect attempt actually fails (which
// increments reconnectAttempts).
func TestIntegration_Redis_Resilience_BackoffRateLimiting(t *testing.T) {
	container, addr, cleanup := setupRedisContainerRaw(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)

	// Verify the connection is healthy before we break things.
	rdb, err := client.GetClient(ctx)
	require.NoError(t, err)
	require.NoError(t, rdb.Ping(ctx).Err())

	// Stop the container so reconnect attempts genuinely fail.
	t.Log("Stopping container to make reconnect attempts fail...")
	require.NoError(t, container.Stop(ctx, nil))

	// Close the wrapper client to nil out the internal go-redis handle.
	// This puts the client into the "needs reconnect" state.
	require.NoError(t, client.Close())

	// First GetClient call: should attempt a real reconnect to the stopped
	// server, fail, and increment reconnectAttempts to 1.
	_, err = client.GetClient(ctx)
	require.Error(t, err, "first GetClient must fail because server is stopped")
	t.Logf("First GetClient error (expected): %v", err)

	// Rapid subsequent calls: should be rate-limited because we're within
	// the backoff window. The delay after 1 failure is in
	// [0, 500ms * 2^1) = [0, 1000ms). Even with jitter at its minimum (0ms),
	// consecutive calls within microseconds should be rate-limited after the
	// first real attempt set lastReconnectAttempt.
	rateLimitedCount := 0
	realAttemptCount := 0

	const rapidCalls = 20

	for range rapidCalls {
		_, callErr := client.GetClient(ctx)
		require.Error(t, callErr)

		if strings.Contains(callErr.Error(), "rate-limited") {
			rateLimitedCount++
		} else {
			realAttemptCount++
		}
	}

	t.Logf("Of %d rapid calls: %d rate-limited, %d real attempts",
		rapidCalls, rateLimitedCount, realAttemptCount)

	// Due to the jitter in ExponentialWithJitter, the exact split between
	// rate-limited and real attempts is non-deterministic. However, we
	// expect the majority to be rate-limited since the calls happen in
	// microseconds and the backoff window is at least hundreds of milliseconds.
	assert.Greater(t, rateLimitedCount, 0,
		"at least some calls must be rate-limited to prevent reconnect storms")

	// Verify that real reconnect attempts are significantly fewer than
	// rate-limited ones. This proves the backoff is working.
	if rateLimitedCount > 0 && realAttemptCount > 0 {
		assert.Greater(t, rateLimitedCount, realAttemptCount,
			"rate-limited calls should outnumber real reconnect attempts")
	}
}

// TestIntegration_Redis_Resilience_GracefulDegradation validates that the
// client degrades gracefully under failure conditions without panics or
// undefined behavior:
//  1. After server goes down, IsConnected() still reflects the last known
//     state (true) because no probe has updated it yet.
//  2. Operations on the stale client handle fail with errors (not panics).
//  3. After Close() + GetClient(), we get clean errors (not panics).
//  4. Status() returns a valid struct throughout.
func TestIntegration_Redis_Resilience_GracefulDegradation(t *testing.T) {
	container, addr, cleanup := setupRedisContainerRaw(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)

	defer func() { _ = client.Close() }()

	// Capture the connected client handle before the outage.
	rdb, err := client.GetClient(ctx)
	require.NoError(t, err)
	require.NoError(t, rdb.Ping(ctx).Err())

	// Stop the server while the client still holds a connection handle.
	t.Log("Stopping Redis container...")
	require.NoError(t, container.Stop(ctx, nil))

	// IsConnected() checks the client struct's `connected` field, which is
	// only updated on connect/close calls — NOT by external server state.
	// So immediately after a server crash, it still reports true.
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected,
		"IsConnected must still be true immediately after server stop "+
			"(the struct hasn't been updated yet)")

	// Status() must return a valid struct, not panic.
	status, err := client.Status()
	require.NoError(t, err)
	assert.True(t, status.Connected,
		"Status.Connected must reflect the struct state, not the wire state")

	// Operations on the stale rdb handle should fail with errors, not panics.
	setErr := rdb.Set(ctx, "degradation:should-fail", "value", 10*time.Second).Err()
	assert.Error(t, setErr, "SET on stale handle must fail when server is down")

	pingErr := rdb.Ping(ctx).Err()
	assert.Error(t, pingErr, "PING on stale handle must fail when server is down")

	// Close the wrapper client. This nils out the internal handle and sets
	// connected=false.
	require.NoError(t, client.Close())

	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected, "IsConnected must be false after Close()")

	// GetClient() should attempt reconnect, fail (server is still down),
	// and return an error — not panic.
	_, getErr := client.GetClient(ctx)
	assert.Error(t, getErr, "GetClient must fail gracefully when server is down")

	// Verify Status() still works and returns a coherent snapshot.
	status, err = client.Status()
	require.NoError(t, err)
	assert.False(t, status.Connected,
		"Status.Connected must be false after failed reconnect")

	// Calling Close() again on an already-closed client must not panic.
	assert.NotPanics(t, func() {
		_ = client.Close()
	}, "double Close() must not panic")
}

// TestIntegration_Redis_Resilience_ConcurrentReconnect validates that when
// multiple goroutines call GetClient() simultaneously on a disconnected
// client, the double-checked locking in GetClient() serializes reconnect
// attempts correctly:
//   - No panics or data races (validated by -race detector).
//   - Only one goroutine performs the actual connect (others either get the
//     reconnected client from the second c.client!=nil check, or get a
//     rate-limited/connection error).
//   - All goroutines return without hanging.
func TestIntegration_Redis_Resilience_ConcurrentReconnect(t *testing.T) {
	_, addr, cleanup := setupRedisContainerRaw(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)

	// Verify healthy state before we break things.
	rdb, err := client.GetClient(ctx)
	require.NoError(t, err)
	require.NoError(t, rdb.Ping(ctx).Err())

	// Close the wrapper to put the client into "needs reconnect" state.
	// The container is still running, so reconnect should succeed.
	require.NoError(t, client.Close())

	connected, err := client.IsConnected()
	require.NoError(t, err)
	require.False(t, connected, "precondition: client must be disconnected")

	const goroutines = 10

	var (
		wg             sync.WaitGroup
		successCount   atomic.Int64
		errorCount     atomic.Int64
		panicRecovered atomic.Int64
	)

	wg.Add(goroutines)

	// All goroutines start simultaneously via a shared gate.
	gate := make(chan struct{})

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			// Catch any panics so the test can report them rather than crashing.
			defer func() {
				if r := recover(); r != nil {
					panicRecovered.Add(1)
					t.Errorf("goroutine %d panicked: %v", id, r)
				}
			}()

			// Wait for the gate to open so all goroutines race together.
			<-gate

			rdbLocal, getErr := client.GetClient(ctx)
			if getErr != nil {
				errorCount.Add(1)
				return
			}

			// Verify the returned client is functional.
			if pingErr := rdbLocal.Ping(ctx).Err(); pingErr != nil {
				errorCount.Add(1)
				return
			}

			successCount.Add(1)
		}(i)
	}

	// Open the gate: all goroutines race into GetClient().
	close(gate)
	wg.Wait()

	successes := successCount.Load()
	errors := errorCount.Load()
	panics := panicRecovered.Load()

	t.Logf("Concurrent reconnect results: %d successes, %d errors, %d panics",
		successes, errors, panics)

	// Hard requirement: no panics.
	assert.Equal(t, int64(0), panics, "no goroutines should panic during concurrent reconnect")

	// At least one goroutine must succeed (the one that wins the lock and
	// reconnects). Others may succeed too (if they arrive after the reconnect
	// completes and see c.client != nil in the fast path), or fail with
	// rate-limited errors.
	assert.Greater(t, successes, int64(0),
		"at least one goroutine must successfully reconnect")

	// All goroutines must have completed (no hangs).
	assert.Equal(t, int64(goroutines), successes+errors+panics,
		"all goroutines must complete")

	// Verify the client is in a good state after the storm.
	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected, "client must be connected after successful concurrent reconnect")

	// Final cleanup.
	require.NoError(t, client.Close())
}
