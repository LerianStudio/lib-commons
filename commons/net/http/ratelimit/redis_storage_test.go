//go:build unit

package ratelimit

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libRedis "github.com/LerianStudio/lib-commons/v5/commons/redis"
	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRedisConnection(t *testing.T, mr *miniredis.Miniredis) *libRedis.Client {
	t.Helper()

	conn, err := libRedis.New(context.Background(), libRedis.Config{
		Topology: libRedis.Topology{
			Standalone: &libRedis.StandaloneTopology{Address: mr.Addr()},
		},
		Logger: &libLog.NopLogger{},
	})
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

func TestNewRedisStorage_NilConnection(t *testing.T) {
	t.Parallel()

	storage := NewRedisStorage(nil)
	assert.Nil(t, storage)
}

func TestNewRedisStorage_ValidConnection(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)
	require.NotNil(t, storage.conn)
}

func TestNewRedisStorage_NilOptionIgnored(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	assert.NotPanics(t, func() {
		storage := NewRedisStorage(conn, nil)
		require.NotNil(t, storage)
	})
}

func TestRedisStorage_GetSetDelete(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	key := "test-key"
	value := []byte("test-value")

	val, err := storage.Get(key)
	require.NoError(t, err)
	assert.Nil(t, val)

	err = storage.Set(key, value, time.Minute)
	require.NoError(t, err)

	val, err = storage.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, val)

	err = storage.Delete(key)
	require.NoError(t, err)

	val, err = storage.Get(key)
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestRedisStorage_SetEmptyKeyIgnored(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	err := storage.Set("", []byte("value"), time.Minute)
	require.NoError(t, err)

	err = storage.Set("key", nil, time.Minute)
	require.NoError(t, err)
}

func TestRedisStorage_Reset(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	require.NoError(t, storage.Set("key1", []byte("val1"), time.Minute))
	require.NoError(t, storage.Set("key2", []byte("val2"), time.Minute))

	err := storage.Reset()
	require.NoError(t, err)

	val, err := storage.Get("key1")
	require.NoError(t, err)
	assert.Nil(t, val)

	val, err = storage.Get("key2")
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestRedisStorage_Close(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	err := storage.Close()
	require.NoError(t, err)
}

func TestRedisStorage_NilStorageOperations(t *testing.T) {
	t.Parallel()

	var storage *RedisStorage

	val, err := storage.Get("key")
	require.ErrorIs(t, err, ErrStorageUnavailable)
	assert.Nil(t, val)

	err = storage.Set("key", []byte("value"), time.Minute)
	require.ErrorIs(t, err, ErrStorageUnavailable)

	err = storage.Delete("key")
	require.ErrorIs(t, err, ErrStorageUnavailable)

	err = storage.Reset()
	require.ErrorIs(t, err, ErrStorageUnavailable)

	err = storage.Close()
	require.NoError(t, err)
}

func TestRedisStorage_KeyPrefix(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	require.NoError(t, storage.Set("test", []byte("value"), time.Minute))

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	t.Cleanup(func() { _ = client.Close() })

	val, err := client.Get(t.Context(), "ratelimit:test").Bytes()
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)
}

// --- New comprehensive test coverage below ---

func TestRedisStorage_ConcurrentIncrementOperations(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	const workers = 50
	const key = "concurrent-counter"

	var wg sync.WaitGroup

	wg.Add(workers)

	var errCount int32

	for range workers {
		go func() {
			defer wg.Done()

			// Simulate incrementing a counter: read, parse, increment, write
			val, err := storage.Get(key)
			if err != nil {
				atomic.AddInt32(&errCount, 1)
				return
			}

			counter := 0
			if val != nil {
				counter, _ = strconv.Atoi(string(val))
			}

			counter++

			if err := storage.Set(key, []byte(strconv.Itoa(counter)), time.Minute); err != nil {
				atomic.AddInt32(&errCount, 1)
			}
		}()
	}

	wg.Wait()

	// Verify no errors occurred (no panics, no crashes)
	assert.Equal(t, int32(0), atomic.LoadInt32(&errCount))

	// Verify the key exists and has a value (exact value depends on race ordering,
	// which is expected - this test validates no crashes under contention)
	val, err := storage.Get(key)
	require.NoError(t, err)
	assert.NotNil(t, val)

	counter, err := strconv.Atoi(string(val))
	require.NoError(t, err)
	assert.Greater(t, counter, 0)
}

func TestRedisStorage_ConcurrentSetGet(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	const workers = 20
	var wg sync.WaitGroup
	errCh := make(chan error, workers*2)

	wg.Add(workers * 2) // writers + readers

	// Writers
	for i := range workers {
		go func(idx int) {
			defer wg.Done()

			key := "concurrent-key-" + strconv.Itoa(idx)
			val := []byte("value-" + strconv.Itoa(idx))

			if err := storage.Set(key, val, time.Minute); err != nil {
				errCh <- err
			}
		}(i)
	}

	// Readers (concurrent with writers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()

			key := "concurrent-key-" + strconv.Itoa(idx)

			if _, err := storage.Get(key); err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	// Verify all keys were written
	for i := range workers {
		key := "concurrent-key-" + strconv.Itoa(i)
		val, err := storage.Get(key)
		require.NoError(t, err)
		assert.Equal(t, []byte("value-"+strconv.Itoa(i)), val)
	}
}

func TestRedisStorage_TTLExpiration(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	key := "expiring-key"
	value := []byte("temporary")

	// Set with short TTL
	err := storage.Set(key, value, time.Second)
	require.NoError(t, err)

	// Verify it exists
	val, err := storage.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, val)

	// Fast-forward miniredis time past the TTL
	mr.FastForward(2 * time.Second)

	// Now the key should be expired
	val, err = storage.Get(key)
	require.NoError(t, err)
	assert.Nil(t, val, "key should have expired after TTL")
}

func TestRedisStorage_ZeroTTL(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	key := "no-expiry-key"
	value := []byte("persistent")

	// Set with 0 TTL (no expiration)
	err := storage.Set(key, value, 0)
	require.NoError(t, err)

	// Fast-forward time significantly
	mr.FastForward(24 * time.Hour)

	// Key should still exist
	val, err := storage.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, val)
}

func TestRedisStorage_MultipleKeySimultaneous(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	keys := map[string][]byte{
		"key-alpha":   []byte("value-alpha"),
		"key-beta":    []byte("value-beta"),
		"key-gamma":   []byte("value-gamma"),
		"key-delta":   []byte("value-delta"),
		"key-epsilon": []byte("value-epsilon"),
	}

	// Set all keys
	for k, v := range keys {
		require.NoError(t, storage.Set(k, v, time.Minute))
	}

	// Verify all keys exist with correct values
	for k, expected := range keys {
		val, err := storage.Get(k)
		require.NoError(t, err)
		assert.Equal(t, expected, val, "key %s should have correct value", k)
	}

	// Delete one key
	require.NoError(t, storage.Delete("key-gamma"))

	// Verify deleted key returns nil
	val, err := storage.Get("key-gamma")
	require.NoError(t, err)
	assert.Nil(t, val)

	// Verify other keys still exist
	val, err = storage.Get("key-alpha")
	require.NoError(t, err)
	assert.Equal(t, []byte("value-alpha"), val)
}

func TestRedisStorage_LargeCounterValues(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Store a very large counter value
	largeValue := strconv.Itoa(999999999)
	err := storage.Set("large-counter", []byte(largeValue), time.Minute)
	require.NoError(t, err)

	val, err := storage.Get("large-counter")
	require.NoError(t, err)
	assert.Equal(t, []byte(largeValue), val)
}

func TestRedisStorage_LargeByteValue(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Store a large byte slice
	largeVal := make([]byte, 1024*10) // 10KB
	for i := range largeVal {
		largeVal[i] = byte(i % 256)
	}

	err := storage.Set("large-value", largeVal, time.Minute)
	require.NoError(t, err)

	val, err := storage.Get("large-value")
	require.NoError(t, err)
	assert.Equal(t, largeVal, val)
}

func TestRedisStorage_SetOverwrite(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	key := "overwrite-key"

	// Set initial value
	require.NoError(t, storage.Set(key, []byte("original"), time.Minute))

	val, err := storage.Get(key)
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), val)

	// Overwrite with new value
	require.NoError(t, storage.Set(key, []byte("updated"), time.Minute))

	val, err = storage.Get(key)
	require.NoError(t, err)
	assert.Equal(t, []byte("updated"), val)
}

func TestRedisStorage_DeleteNonExistentKey(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Delete a key that doesn't exist should not error
	err := storage.Delete("non-existent-key")
	require.NoError(t, err)
}

func TestRedisStorage_GetNonExistentKey(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	val, err := storage.Get("non-existent-key")
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestRedisStorage_ResetOnlyRateLimitKeys(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Set rate limit keys through storage (these get the ratelimit: prefix)
	require.NoError(t, storage.Set("limit-key-1", []byte("1"), time.Minute))
	require.NoError(t, storage.Set("limit-key-2", []byte("2"), time.Minute))

	// Set a non-rate-limit key directly via Redis (no ratelimit: prefix)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Set(t.Context(), "other:key", "non-ratelimit", time.Minute).Err())

	// Reset should only clear ratelimit: prefixed keys
	err := storage.Reset()
	require.NoError(t, err)

	// Rate limit keys should be gone
	val, err := storage.Get("limit-key-1")
	require.NoError(t, err)
	assert.Nil(t, val)

	val, err = storage.Get("limit-key-2")
	require.NoError(t, err)
	assert.Nil(t, val)

	// Non-rate-limit key should still exist
	otherVal, err := client.Get(t.Context(), "other:key").Result()
	require.NoError(t, err)
	assert.Equal(t, "non-ratelimit", otherVal)
}

func TestRedisStorage_ResetEmptyStorage(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Reset on empty storage should not error
	err := storage.Reset()
	require.NoError(t, err)
}

func TestRedisStorage_NilConnOperations(t *testing.T) {
	t.Parallel()

	// Storage with nil conn field (manually constructed)
	storage := &RedisStorage{conn: nil}

	val, err := storage.Get("key")
	require.ErrorIs(t, err, ErrStorageUnavailable)
	assert.Nil(t, val)

	err = storage.Set("key", []byte("value"), time.Minute)
	require.ErrorIs(t, err, ErrStorageUnavailable)

	err = storage.Delete("key")
	require.ErrorIs(t, err, ErrStorageUnavailable)

	err = storage.Reset()
	require.ErrorIs(t, err, ErrStorageUnavailable)
}

func TestRedisStorage_SetEmptyValue(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Empty byte slice should be ignored (same as nil)
	err := storage.Set("key", []byte{}, time.Minute)
	require.NoError(t, err)

	// Key should not exist since empty value is ignored
	val, err := storage.Get("key")
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestRedisStorage_CloseIsNoop(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Close should be a no-op and return nil
	err := storage.Close()
	require.NoError(t, err)

	// Storage should still work after Close (since Close is a no-op)
	err = storage.Set("after-close", []byte("value"), time.Minute)
	require.NoError(t, err)

	val, err := storage.Get("after-close")
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)
}

func TestRedisStorage_ResetManyKeys(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Set more keys than scanBatchSize (100) to test pagination in SCAN
	const numKeys = 150
	for i := range numKeys {
		key := "batch-key-" + strconv.Itoa(i)
		require.NoError(t, storage.Set(key, []byte(strconv.Itoa(i)), time.Minute))
	}

	// Reset should clear all of them
	err := storage.Reset()
	require.NoError(t, err)

	// Verify they're all gone
	for i := range numKeys {
		key := "batch-key-" + strconv.Itoa(i)
		val, err := storage.Get(key)
		require.NoError(t, err)
		assert.Nil(t, val, "key %s should be deleted after reset", key)
	}
}

func TestRedisStorage_SetWithDifferentTTLs(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Set keys with different TTLs
	require.NoError(t, storage.Set("short-ttl", []byte("short"), 1*time.Second))
	require.NoError(t, storage.Set("long-ttl", []byte("long"), 1*time.Hour))

	// Both should exist initially
	val, err := storage.Get("short-ttl")
	require.NoError(t, err)
	assert.Equal(t, []byte("short"), val)

	val, err = storage.Get("long-ttl")
	require.NoError(t, err)
	assert.Equal(t, []byte("long"), val)

	// Fast-forward past short TTL but before long TTL
	mr.FastForward(5 * time.Second)

	// Short TTL should be gone
	val, err = storage.Get("short-ttl")
	require.NoError(t, err)
	assert.Nil(t, val, "short-ttl should have expired")

	// Long TTL should still exist
	val, err = storage.Get("long-ttl")
	require.NoError(t, err)
	assert.Equal(t, []byte("long"), val, "long-ttl should still exist")
}
