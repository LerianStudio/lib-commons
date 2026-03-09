//go:build integration

package postgres

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
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupPostgresContainerRaw starts a PostgreSQL 16 container and returns the
// container handle (for Stop/Start control), its connection string, and a
// cleanup function. Unlike setupPostgresContainer, this returns the raw
// container so tests can simulate server outages by stopping and restarting it.
func setupPostgresContainerRaw(t *testing.T) (*tcpostgres.PostgresContainer, string, func()) {
	t.Helper()

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	return container, connStr, func() {
		_ = container.Terminate(ctx)
	}
}

// waitForPostgresReady polls the restarted container until PostgreSQL is
// accepting connections. After a container restart the mapped port may change,
// so the caller must provide the current DSN. We try New()+Connect() every
// pollInterval for up to timeout.
func waitForPostgresReady(t *testing.T, dsn string, timeout, pollInterval time.Duration) {
	t.Helper()

	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		probe, err := New(newTestConfig(dsn))
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		if connectErr := probe.Connect(ctx); connectErr == nil {
			_ = probe.Close()
			return
		}

		_ = probe.Close()
		time.Sleep(pollInterval)
	}

	t.Fatalf("PostgreSQL at DSN did not become ready within %s", timeout)
}

// TestIntegration_Postgres_Resilience_ReconnectAfterRestart validates the full
// outage-recovery cycle:
//  1. Connect and verify operations work (SELECT 1).
//  2. Stop the container (simulates server crash / network partition).
//  3. Verify that operations fail while the server is down.
//  4. Restart the container and re-read the DSN (port may change).
//  5. Create a fresh client with the new DSN and verify operations succeed.
//
// This is the most realistic resilience scenario: the backing PostgreSQL goes
// down and comes back, possibly on a different port.
func TestIntegration_Postgres_Resilience_ReconnectAfterRestart(t *testing.T) {
	container, dsn, cleanup := setupPostgresContainerRaw(t)
	defer cleanup()

	ctx := context.Background()

	// Phase 1: establish a healthy connection and verify operations.
	client, err := New(newTestConfig(dsn))
	require.NoError(t, err, "New() should succeed with valid DSN")

	err = client.Connect(ctx)
	require.NoError(t, err, "Connect() should succeed against running container")

	db, err := client.Primary()
	require.NoError(t, err, "Primary() should return a live *sql.DB")

	var result int
	err = db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
	require.NoError(t, err, "SELECT 1 must succeed while server is healthy")
	assert.Equal(t, 1, result)

	// Phase 2: stop the container to simulate server going down.
	t.Log("Stopping PostgreSQL container to simulate outage...")
	require.NoError(t, container.Stop(ctx, nil))

	// The existing *sql.DB handle is now pointing at a dead socket.
	// Operations should fail (the exact error varies by OS/timing).
	err = db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
	assert.Error(t, err, "SELECT 1 must fail while server is down")

	// Phase 3: restart the container. The mapped port may change after
	// restart, so we must re-read the connection string from the container.
	t.Log("Restarting PostgreSQL container...")
	require.NoError(t, container.Start(ctx))

	newDSN, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "must be able to read connection string after restart")
	t.Logf("PostgreSQL DSN after restart: %s (was: %s)", newDSN, dsn)

	// Poll until the server is accepting connections at the (potentially new) DSN.
	waitForPostgresReady(t, newDSN, 15*time.Second, 200*time.Millisecond)
	t.Log("PostgreSQL container is ready after restart")

	// Phase 4: close the old client and create a fresh one with the new DSN.
	_ = client.Close()

	client2, err := New(newTestConfig(newDSN))
	require.NoError(t, err, "New() must succeed after server restart")

	defer func() { _ = client2.Close() }()

	err = client2.Connect(ctx)
	require.NoError(t, err, "Connect() must succeed against restarted container")

	// Phase 5: verify the reconnected client can operate.
	db2, err := client2.Primary()
	require.NoError(t, err, "Primary() must return a live *sql.DB after reconnect")

	var result2 int
	err = db2.QueryRowContext(ctx, "SELECT 1").Scan(&result2)
	require.NoError(t, err, "SELECT 1 must succeed after reconnect")
	assert.Equal(t, 1, result2, "query result must be correct after reconnect")

	connected, err := client2.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected, "client must report connected after successful reconnect")
}

// TestIntegration_Postgres_Resilience_BackoffRateLimiting validates that the
// reconnect rate-limiter prevents thundering-herd storms. When the resolver is
// nil and Resolver() is called rapidly, the first call attempts a real
// reconnect; subsequent calls within the backoff window return a "rate-limited"
// error without hitting the network.
//
// Mechanism (from postgres.go Resolver):
//   - connectAttempts tracks consecutive failures.
//   - Each failure increments connectAttempts and records lastConnectAttempt.
//   - The next Resolver() computes delay = ExponentialWithJitter(1s, attempts).
//   - If elapsed < delay, it returns "rate-limited" immediately.
//
// To trigger this path, we connect to a real PostgreSQL, stop the container so
// reconnect attempts genuinely fail, then close the client (resolver=nil) and
// fire rapid Resolver() calls.
func TestIntegration_Postgres_Resilience_BackoffRateLimiting(t *testing.T) {
	container, dsn, cleanup := setupPostgresContainerRaw(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	// Verify the connection is healthy before we break things.
	err = client.Connect(ctx)
	require.NoError(t, err)

	resolver, err := client.Resolver(ctx)
	require.NoError(t, err)
	require.NoError(t, resolver.PingContext(ctx))

	// Stop the container so reconnect attempts genuinely fail.
	t.Log("Stopping container to make reconnect attempts fail...")
	require.NoError(t, container.Stop(ctx, nil))

	// Close the wrapper client to nil out the resolver. This puts the client
	// into the "needs reconnect" state where Resolver() will attempt lazy-connect.
	require.NoError(t, client.Close())

	// First Resolver() call: should attempt a real reconnect to the stopped
	// server, fail, and increment connectAttempts to 1.
	_, err = client.Resolver(ctx)
	require.Error(t, err, "first Resolver() must fail because server is stopped")
	t.Logf("First Resolver() error (expected): %v", err)

	// Rapid subsequent calls: should be rate-limited because we're within
	// the backoff window. The delay after 1 failure is in
	// [0, 1s * 2^1) = [0, 2s). Even with jitter at its minimum (0ms),
	// consecutive calls within microseconds should be rate-limited after the
	// first real attempt set lastConnectAttempt.
	rateLimitedCount := 0
	realAttemptCount := 0

	const rapidCalls = 20

	for range rapidCalls {
		_, callErr := client.Resolver(ctx)
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

// TestIntegration_Postgres_Resilience_GracefulDegradation validates that the
// client degrades gracefully under failure conditions without panics or
// undefined behavior:
//  1. After server goes down, IsConnected() still returns true because the
//     resolver struct field was set during Connect() and has not been cleared.
//  2. Primary() returns the stale *sql.DB (struct access, not a wire check).
//  3. PingContext on the stale *sql.DB fails (server is down).
//  4. Close() succeeds cleanly.
//  5. Resolver() after Close() fails with an error (not a panic).
//  6. No panics throughout the entire degradation sequence.
func TestIntegration_Postgres_Resilience_GracefulDegradation(t *testing.T) {
	container, dsn, cleanup := setupPostgresContainerRaw(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	defer func() {
		// Best-effort close; may already be closed.
		_ = client.Close()
	}()

	// Establish a healthy connection.
	err = client.Connect(ctx)
	require.NoError(t, err)

	db, err := client.Primary()
	require.NoError(t, err)
	require.NoError(t, db.PingContext(ctx), "PingContext must succeed while server is healthy")

	// Stop the server while the client still holds connection handles.
	t.Log("Stopping PostgreSQL container...")
	require.NoError(t, container.Stop(ctx, nil))

	// IsConnected() checks c.resolver != nil. The struct field was set during
	// Connect() and hasn't been cleared, so it still returns true even though
	// the server is unreachable.
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected,
		"IsConnected must still be true immediately after server stop "+
			"(the struct field hasn't been cleared)")

	// Primary() returns the stale *sql.DB — this is a struct read, not a
	// wire check. The handle itself is still non-nil.
	staleDB, err := client.Primary()
	require.NoError(t, err, "Primary() must return the stale *sql.DB without error")
	require.NotNil(t, staleDB, "stale *sql.DB must be non-nil")

	// But PingContext on the stale handle must fail because the server is down.
	pingErr := staleDB.PingContext(ctx)
	assert.Error(t, pingErr, "PingContext on stale handle must fail when server is down")

	// Close() should succeed cleanly, releasing all handles.
	closeErr := client.Close()
	assert.NoError(t, closeErr, "Close() must succeed even when server is unreachable")

	// After Close(), IsConnected must be false (resolver was set to nil).
	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected, "IsConnected must be false after Close()")

	// Resolver() should attempt reconnect, fail (server is still down),
	// and return an error — not panic.
	_, resolverErr := client.Resolver(ctx)
	assert.Error(t, resolverErr, "Resolver() must fail gracefully when server is down")

	// Primary() after Close() should return ErrNotConnected — not panic.
	_, primaryErr := client.Primary()
	assert.Error(t, primaryErr, "Primary() must fail gracefully after Close()")

	// Calling Close() again on an already-closed client must not panic.
	assert.NotPanics(t, func() {
		_ = client.Close()
	}, "double Close() must not panic")
}

// TestIntegration_Postgres_Resilience_ConcurrentResolve validates that when
// multiple goroutines call Resolver() simultaneously on a disconnected client,
// the double-checked locking in Resolver() serializes reconnect attempts
// correctly:
//   - No panics or data races (validated by -race detector).
//   - Only one goroutine performs the actual connect; others either get the
//     reconnected resolver from the second c.resolver!=nil check, or get a
//     rate-limited / connection error.
//   - All goroutines return without hanging (deadlock-free).
func TestIntegration_Postgres_Resilience_ConcurrentResolve(t *testing.T) {
	_, dsn, cleanup := setupPostgresContainerRaw(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	// Verify healthy state before we break things.
	err = client.Connect(ctx)
	require.NoError(t, err)

	resolver, err := client.Resolver(ctx)
	require.NoError(t, err)
	require.NoError(t, resolver.PingContext(ctx))

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

			res, resolveErr := client.Resolver(ctx)
			if resolveErr != nil {
				errorCount.Add(1)
				return
			}

			// Verify the returned resolver is functional.
			if pingErr := res.PingContext(ctx); pingErr != nil {
				errorCount.Add(1)
				return
			}

			successCount.Add(1)
		}(i)
	}

	// Use a timeout to detect deadlocks: if goroutines don't finish within
	// a generous window, something is stuck.
	done := make(chan struct{})
	go func() {
		// Open the gate: all goroutines race into Resolver().
		close(gate)
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines completed.
	case <-time.After(30 * time.Second):
		t.Fatal("DEADLOCK: not all goroutines completed within 30 seconds")
	}

	successes := successCount.Load()
	errors := errorCount.Load()
	panics := panicRecovered.Load()

	t.Logf("Concurrent resolve results: %d successes, %d errors, %d panics",
		successes, errors, panics)

	// Hard requirement: no panics.
	assert.Equal(t, int64(0), panics,
		"no goroutines should panic during concurrent resolve")

	// At least one goroutine must succeed (the one that wins the write lock
	// and reconnects). Others may succeed too (if they see c.resolver != nil
	// in the fast path after the winner completes), or fail with rate-limited
	// errors.
	assert.Greater(t, successes, int64(0),
		"at least one goroutine must successfully reconnect")

	// All goroutines must have completed (no hangs).
	assert.Equal(t, int64(goroutines), successes+errors+panics,
		"all goroutines must complete")

	// Verify the client is in a good state after the storm.
	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected,
		"client must be connected after successful concurrent resolve")

	// Final cleanup.
	require.NoError(t, client.Close())
}
