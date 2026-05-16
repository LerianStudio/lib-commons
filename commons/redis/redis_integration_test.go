//go:build integration

package redis

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

const testRedisPort = "6379/tcp"

func waitForRedisEndpoint(t *testing.T, container *tcredis.RedisContainer) string {
	t.Helper()

	ctx := context.Background()
	var endpoint string

	require.Eventually(t, func() bool {
		value, err := container.PortEndpoint(ctx, testRedisPort, "")
		if err != nil || value == "" {
			return false
		}

		endpoint = value
		return true
	}, 10*time.Second, 100*time.Millisecond, "failed to resolve Redis container endpoint")

	return endpoint
}

// setupRedisContainer starts a real Redis 7 container and returns its address
// (host:port) plus a cleanup function. The container is waited on until Redis
// logs "Ready to accept connections", which guarantees the server is ready.
func setupRedisContainer(t *testing.T) (string, func()) {
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

	endpoint := waitForRedisEndpoint(t, container)

	return endpoint, func() {
		require.NoError(t, container.Terminate(ctx))
	}
}

// setupRedisContainerWithPassword starts a Redis 7 container with password
// authentication enabled via the --requirepass flag. Returns the address,
// the password, and a cleanup function.
func setupRedisContainerWithPassword(t *testing.T, password string) (string, func()) {
	t.Helper()

	ctx := context.Background()

	container, err := tcredis.Run(ctx,
		"redis:7-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(30*time.Second),
		),
		// Override the default CMD to pass --requirepass.
		testcontainers.WithCmd("redis-server", "--requirepass", password),
	)
	require.NoError(t, err)

	endpoint := waitForRedisEndpoint(t, container)

	return endpoint, func() {
		require.NoError(t, container.Terminate(ctx))
	}
}

// newTestConfig builds a minimal standalone Config pointing at the given address.
func newTestConfig(addr string) Config {
	return Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: addr},
		},
		Logger: &log.NopLogger{},
	}
}

// TestIntegration_Redis_ConnectAndOperate verifies the full lifecycle against a
// real Redis container: connect, SET, GET, and close.
func TestIntegration_Redis_ConnectAndOperate(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)

	defer func() { require.NoError(t, client.Close()) }()

	rdb, err := client.GetClient(ctx)
	require.NoError(t, err)
	require.NotNil(t, rdb)

	// SET a key with a TTL to avoid polluting the container beyond test scope.
	const testKey = "integration:connect:key"
	const testValue = "hello-from-integration-test"

	err = rdb.Set(ctx, testKey, testValue, 30*time.Second).Err()
	require.NoError(t, err, "SET must succeed")

	got, err := rdb.Get(ctx, testKey).Result()
	require.NoError(t, err, "GET must succeed")
	assert.Equal(t, testValue, got, "GET value must match SET value")
}

// TestIntegration_Redis_Status verifies that Status() and IsConnected() report
// the correct state throughout the client lifecycle.
func TestIntegration_Redis_Status(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)

	// After New(), the client must be connected.
	status, err := client.Status()
	require.NoError(t, err)
	assert.True(t, status.Connected, "status.Connected must be true after New()")

	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected, "IsConnected must be true after New()")

	// After Close(), the client must report disconnected.
	require.NoError(t, client.Close())

	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected, "IsConnected must be false after Close()")

	status, err = client.Status()
	require.NoError(t, err)
	assert.False(t, status.Connected, "status.Connected must be false after Close()")
}

// TestIntegration_Redis_ReconnectOnDemand verifies that GetClient() transparently
// reconnects when the internal client has been closed (simulating a disconnect).
func TestIntegration_Redis_ReconnectOnDemand(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)

	// Verify initial connectivity.
	rdb, err := client.GetClient(ctx)
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, "reconnect:before", "v1", 30*time.Second).Err())

	// Simulate a disconnect by calling Close(), which sets the internal client
	// to nil and connected to false.
	require.NoError(t, client.Close())

	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected, "must be disconnected after Close()")

	// GetClient() should trigger reconnect-on-demand because the internal
	// client is nil.
	rdb2, err := client.GetClient(ctx)
	require.NoError(t, err, "GetClient must reconnect on demand")
	require.NotNil(t, rdb2)

	// The reconnected client must be able to operate normally.
	require.NoError(t, rdb2.Set(ctx, "reconnect:after", "v2", 30*time.Second).Err())

	got, err := rdb2.Get(ctx, "reconnect:after").Result()
	require.NoError(t, err)
	assert.Equal(t, "v2", got)

	// Verify status is back to connected.
	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected, "must be reconnected after GetClient()")

	// Final cleanup.
	require.NoError(t, client.Close())
}

// TestIntegration_Redis_ConcurrentOperations spawns multiple goroutines each
// performing SET/GET operations concurrently. When run with -race, this
// validates there are no data races in the client implementation.
func TestIntegration_Redis_ConcurrentOperations(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)

	defer func() { require.NoError(t, client.Close()) }()

	const goroutines = 10
	const opsPerGoroutine = 50

	var wg sync.WaitGroup

	wg.Add(goroutines)

	// errors collects any non-nil errors from goroutines so the main
	// goroutine can fail the test with full context.
	errs := make(chan error, goroutines*opsPerGoroutine)

	for g := range goroutines {
		go func(id int) {
			defer wg.Done()

			rdb, getErr := client.GetClient(ctx)
			if getErr != nil {
				errs <- fmt.Errorf("goroutine %d: GetClient: %w", id, getErr)
				return
			}

			for i := range opsPerGoroutine {
				key := fmt.Sprintf("concurrent:%d:%d", id, i)
				value := fmt.Sprintf("val-%d-%d", id, i)

				if setErr := rdb.Set(ctx, key, value, 30*time.Second).Err(); setErr != nil {
					errs <- fmt.Errorf("goroutine %d op %d: SET: %w", id, i, setErr)
					return
				}

				got, getValErr := rdb.Get(ctx, key).Result()
				if getValErr != nil {
					errs <- fmt.Errorf("goroutine %d op %d: GET: %w", id, i, getValErr)
					return
				}

				if got != value {
					errs <- fmt.Errorf("goroutine %d op %d: value mismatch: got %q, want %q", id, i, got, value)
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)

	for e := range errs {
		t.Error(e)
	}
}

// TestIntegration_Redis_StaticPassword verifies that authentication with a
// static password works against a real Redis container configured with
// --requirepass.
func TestIntegration_Redis_StaticPassword(t *testing.T) {
	const password = "integration-test-secret-42"

	addr, cleanup := setupRedisContainerWithPassword(t, password)
	defer cleanup()

	ctx := context.Background()

	// Connect with the correct password.
	cfg := Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: addr},
		},
		Auth: Auth{
			StaticPassword: &StaticPasswordAuth{Password: password},
		},
		Logger: &log.NopLogger{},
	}

	client, err := New(ctx, cfg)
	require.NoError(t, err, "New() with correct password must succeed")

	defer func() { require.NoError(t, client.Close()) }()

	rdb, err := client.GetClient(ctx)
	require.NoError(t, err)

	// Verify authenticated operations work.
	const testKey = "auth:static:key"
	const testValue = "authenticated-value"

	require.NoError(t, rdb.Set(ctx, testKey, testValue, 30*time.Second).Err())

	got, err := rdb.Get(ctx, testKey).Result()
	require.NoError(t, err)
	assert.Equal(t, testValue, got)

	// Verify that connecting WITHOUT a password fails.
	badCfg := Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: addr},
		},
		Logger: &log.NopLogger{},
	}

	badClient, err := New(ctx, badCfg)
	assert.Error(t, err, "New() without password must fail against auth-protected Redis")
	assert.Nil(t, badClient)

	// Verify that connecting with the WRONG password also fails.
	wrongCfg := Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: addr},
		},
		Auth: Auth{
			StaticPassword: &StaticPasswordAuth{Password: "wrong-password"},
		},
		Logger: &log.NopLogger{},
	}

	wrongClient, err := New(ctx, wrongCfg)
	assert.Error(t, err, "New() with wrong password must fail")
	assert.Nil(t, wrongClient)
}
