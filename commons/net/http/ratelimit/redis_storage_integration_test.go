//go:build integration

package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libRedis "github.com/LerianStudio/lib-commons/v5/commons/redis"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// setupRedisContainer starts a disposable Redis container via testcontainers
// and returns a connected libRedis.Client plus a teardown function.
// The container is terminated when the returned cleanup is invoked (typically
// via t.Cleanup).
func setupRedisContainer(t *testing.T) (*libRedis.Client, func()) {
	t.Helper()

	ctx := context.Background()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "failed to start Redis container")

	// Endpoint returns "host:port" which is exactly what StandaloneTopology expects.
	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err, "failed to get Redis container endpoint")

	client, err := libRedis.New(ctx, libRedis.Config{
		Topology: libRedis.Topology{
			Standalone: &libRedis.StandaloneTopology{Address: endpoint},
		},
		Logger: log.NewNop(),
	})
	require.NoError(t, err, "failed to create libRedis.Client")

	cleanup := func() {
		_ = client.Close()

		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("warning: failed to terminate Redis container: %v", err)
		}
	}

	return client, cleanup
}

// ---------------------------------------------------------------------------
// TestIntegration_RateLimitStorage_SetAndGet
// ---------------------------------------------------------------------------

func TestIntegration_RateLimitStorage_SetAndGet(t *testing.T) {
	client, cleanup := setupRedisContainer(t)
	t.Cleanup(cleanup)

	storage := NewRedisStorage(client, WithRedisStorageLogger(log.NewNop()))
	require.NotNil(t, storage, "storage must not be nil with a valid connection")

	key := "integration-test-key"
	value := []byte("integration-test-value")

	// Verify key does not exist before Set.
	got, err := storage.Get(key)
	require.NoError(t, err, "Get on non-existent key should not error")
	assert.Nil(t, got, "Get on non-existent key should return nil")

	// Set the key with a reasonable TTL.
	err = storage.Set(key, value, 30*time.Second)
	require.NoError(t, err, "Set should succeed")

	// Get it back and verify the round-trip.
	got, err = storage.Get(key)
	require.NoError(t, err, "Get after Set should not error")
	assert.Equal(t, value, got, "Get should return the exact value that was Set")
}

// ---------------------------------------------------------------------------
// TestIntegration_RateLimitStorage_Expiration
// ---------------------------------------------------------------------------

func TestIntegration_RateLimitStorage_Expiration(t *testing.T) {
	client, cleanup := setupRedisContainer(t)
	t.Cleanup(cleanup)

	storage := NewRedisStorage(client, WithRedisStorageLogger(log.NewNop()))
	require.NotNil(t, storage)

	key := "expiring-key"
	value := []byte("temporary-value")

	// Set with a 1-second TTL.
	err := storage.Set(key, value, 1*time.Second)
	require.NoError(t, err, "Set with short TTL should succeed")

	// Verify it exists immediately.
	got, err := storage.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, got, "key should exist immediately after Set")

	// Wait for the key to expire. We use 1.5s to give real Redis enough
	// headroom for its lazy/active expiry cycle.
	time.Sleep(1500 * time.Millisecond)

	// Key should now be gone.
	got, err = storage.Get(key)
	require.NoError(t, err, "Get on expired key should not error")
	assert.Nil(t, got, "key should have expired and return nil")
}

// ---------------------------------------------------------------------------
// TestIntegration_RateLimitStorage_Delete
// ---------------------------------------------------------------------------

func TestIntegration_RateLimitStorage_Delete(t *testing.T) {
	client, cleanup := setupRedisContainer(t)
	t.Cleanup(cleanup)

	storage := NewRedisStorage(client, WithRedisStorageLogger(log.NewNop()))
	require.NotNil(t, storage)

	key := "delete-me"
	value := []byte("soon-to-be-gone")

	// Set the key.
	err := storage.Set(key, value, 30*time.Second)
	require.NoError(t, err, "Set should succeed")

	// Confirm it exists.
	got, err := storage.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, got, "key should exist after Set")

	// Delete it.
	err = storage.Delete(key)
	require.NoError(t, err, "Delete should succeed")

	// Confirm it is gone.
	got, err = storage.Get(key)
	require.NoError(t, err, "Get after Delete should not error")
	assert.Nil(t, got, "key should be nil after Delete")
}

// ---------------------------------------------------------------------------
// TestIntegration_RateLimitStorage_Reset
// ---------------------------------------------------------------------------

func TestIntegration_RateLimitStorage_Reset(t *testing.T) {
	client, cleanup := setupRedisContainer(t)
	t.Cleanup(cleanup)

	storage := NewRedisStorage(client, WithRedisStorageLogger(log.NewNop()))
	require.NotNil(t, storage)

	// Populate multiple keys.
	keys := []string{"reset-a", "reset-b", "reset-c", "reset-d", "reset-e"}
	for i, k := range keys {
		err := storage.Set(k, []byte(fmt.Sprintf("value-%d", i)), 30*time.Second)
		require.NoError(t, err, "Set(%s) should succeed", k)
	}

	// Verify all keys exist before Reset.
	for _, k := range keys {
		got, err := storage.Get(k)
		require.NoError(t, err)
		assert.NotNil(t, got, "key %s should exist before Reset", k)
	}

	// Reset all ratelimit keys.
	err := storage.Reset()
	require.NoError(t, err, "Reset should succeed")

	// Verify all keys are gone.
	for _, k := range keys {
		got, err := storage.Get(k)
		require.NoError(t, err, "Get(%s) after Reset should not error", k)
		assert.Nil(t, got, "key %s should be nil after Reset", k)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_RateLimitStorage_ConcurrentAccess
// ---------------------------------------------------------------------------

func TestIntegration_RateLimitStorage_ConcurrentAccess(t *testing.T) {
	client, cleanup := setupRedisContainer(t)
	t.Cleanup(cleanup)

	storage := NewRedisStorage(client, WithRedisStorageLogger(log.NewNop()))
	require.NotNil(t, storage)

	const goroutines = 20

	var (
		wg       sync.WaitGroup
		errCount atomic.Int32
	)

	wg.Add(goroutines)

	// Each goroutine writes its own key and reads it back, exercising
	// concurrent Set/Get against a real Redis server.
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()

			key := "concurrent-" + strconv.Itoa(idx)
			value := []byte("value-" + strconv.Itoa(idx))

			if err := storage.Set(key, value, 30*time.Second); err != nil {
				errCount.Add(1)
				return
			}

			got, err := storage.Get(key)
			if err != nil {
				errCount.Add(1)
				return
			}

			if string(got) != string(value) {
				errCount.Add(1)
			}
		}(i)
	}

	wg.Wait()

	assert.Equal(t, int32(0), errCount.Load(),
		"no errors should occur during concurrent Set/Get operations")

	// Verify all keys are readable after the concurrent burst.
	for i := range goroutines {
		key := "concurrent-" + strconv.Itoa(i)
		expected := []byte("value-" + strconv.Itoa(i))

		got, err := storage.Get(key)
		require.NoError(t, err, "Get(%s) should succeed after concurrent writes", key)
		assert.Equal(t, expected, got, "key %s should hold the correct value", key)
	}
}
