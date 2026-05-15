//go:build unit

package ratelimit

import (
	"errors"
	"sync"
	"testing"
	"time"

	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Increment tests: atomic INCR + PEXPIRE storage primitive ---

func TestRedisStorage_Increment_FirstHit(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	window := 60 * time.Second

	count, ttl, err := storage.Increment(t.Context(), "first-hit", window)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	// On the first hit the script sets PEXPIRE to window, so the TTL returned
	// equals window verbatim (no time has elapsed yet).
	assert.Equal(t, window, ttl)

	// Verify the on-the-wire key carries the storage namespace prefix.
	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "ratelimit:first-hit", keys[0])
}

func TestRedisStorage_Increment_SubsequentHit(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	window := 60 * time.Second

	// Prime the key with a first hit.
	_, ttl1, err := storage.Increment(t.Context(), "subseq", window)
	require.NoError(t, err)
	assert.Equal(t, window, ttl1)

	// Advance miniredis time so the remaining TTL has visibly shrunk.
	mr.FastForward(10 * time.Second)

	count, ttl2, err := storage.Increment(t.Context(), "subseq", window)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
	// PTTL must reflect time already elapsed: less than the full window.
	assert.Less(t, ttl2, window)
	assert.Greater(t, ttl2, time.Duration(0))
}

func TestRedisStorage_Increment_MonotonicCount(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	const hits = 10

	window := time.Minute

	for i := 1; i <= hits; i++ {
		count, _, err := storage.Increment(t.Context(), "mono", window)
		require.NoError(t, err)
		assert.Equal(t, int64(i), count)
	}
}

func TestRedisStorage_Increment_NilStorage(t *testing.T) {
	t.Parallel()

	var storage *RedisStorage

	count, ttl, err := storage.Increment(t.Context(), "key", time.Minute)
	require.ErrorIs(t, err, ErrStorageUnavailable)
	assert.Zero(t, count)
	assert.Zero(t, ttl)
}

func TestRedisStorage_Increment_NilConn(t *testing.T) {
	t.Parallel()

	storage := &RedisStorage{conn: nil}

	count, ttl, err := storage.Increment(t.Context(), "key", time.Minute)
	require.ErrorIs(t, err, ErrStorageUnavailable)
	assert.Zero(t, count)
	assert.Zero(t, ttl)
}

func TestRedisStorage_Increment_DisconnectedRedis(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Sanity-check that Increment works before closure.
	_, _, err := storage.Increment(t.Context(), "pre-close", time.Minute)
	require.NoError(t, err)

	// Kill the Redis server. The next Eval round-trip must fail and the error
	// must be wrapped with the storage layer's "redis eval:" envelope so callers
	// can distinguish transport errors from invariant-violation errors.
	mr.Close()

	count, ttl, err := storage.Increment(t.Context(), "post-close", time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis eval:")
	assert.NotErrorIs(t, err, ErrStorageUnavailable, "transport error must not masquerade as storage-unavailable")
	assert.Zero(t, count)
	assert.Zero(t, ttl)
}

func TestRedisStorage_Increment_InvalidWindow(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	cases := []struct {
		name   string
		window time.Duration
	}{
		{name: "zero window", window: 0},
		{name: "negative window", window: -1 * time.Second},
		{name: "positive sub-millisecond window", window: 999 * time.Microsecond},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			count, ttl, err := storage.Increment(t.Context(), "bad-window", tc.window)
			require.ErrorIs(t, err, ErrInvalidWindow)
			assert.Zero(t, count)
			assert.Zero(t, ttl)
		})
	}
}

func TestRedisStorage_Increment_TenantContextScopesRedisKey(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	ctx := tmcore.ContextWithTenantID(t.Context(), "tenant-A")
	count, ttl, err := storage.Increment(ctx, "tenant-key", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	assert.Equal(t, time.Minute, ttl)

	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "tenant:tenant-A:ratelimit:tenant-key", keys[0])
}

func TestRedisStorage_Increment_ServicePrefixStaysInStorageNamespace(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	_, _, err := storage.Increment(t.Context(), "my-svc:ratelimit:default:user-1", time.Minute)
	require.NoError(t, err)

	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "ratelimit:my-svc:ratelimit:default:user-1", keys[0])

	require.NoError(t, storage.Reset())
	assert.Empty(t, mr.Keys(), "Reset must delete storage-owned keys even when the identity contains :ratelimit:")
}

func TestRedisStorage_Increment_IdentityContainingRateLimitDoesNotEscapeNamespace(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	_, _, err := storage.Increment(t.Context(), "user:ratelimit:evil:tail", time.Minute)
	require.NoError(t, err)

	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "ratelimit:user:ratelimit:evil:tail", keys[0])

	_, _, err = storage.Increment(t.Context(), "user:foo:ratelimit:evil", time.Minute)
	require.NoError(t, err)

	keys = mr.Keys()
	assert.Contains(t, keys, "ratelimit:user:foo:ratelimit:evil")
}

func TestRedisStorage_Increment_InvalidTenantContextSanitized(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	const rawTenantID = "tenant:raw-secret"
	ctx := tmcore.ContextWithTenantID(t.Context(), rawTenantID)
	_, _, err := storage.Increment(ctx, "key", time.Minute)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidTenantContext))
	assert.NotContains(t, err.Error(), rawTenantID)
}

func TestRedisStorage_Increment_PTTLClearedFallback(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	window := 60 * time.Second
	key := "cleared-ttl"
	wireKey := keyPrefix + key

	// Prime the counter and verify the key now has a TTL.
	_, _, err := storage.Increment(t.Context(), key, window)
	require.NoError(t, err)

	// Use the underlying go-redis client to PERSIST the key — this strips the
	// TTL and simulates the "external SET cleared TTL" path so PTTL returns -1.
	client, err := conn.GetClient(t.Context())
	require.NoError(t, err)
	require.NoError(t, client.Persist(t.Context(), wireKey).Err())

	pttl, err := client.PTTL(t.Context(), wireKey).Result()
	require.NoError(t, err)
	assert.Equal(t, time.Duration(-1), pttl, "PTTL must report -1 after PERSIST")

	// Increment must fall back to window when the script observes the -1 sentinel.
	count, ttl, err := storage.Increment(t.Context(), key, window)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
	assert.Equal(t, window, ttl, "Increment must fall back to window when PTTL == -1")
}

func TestRedisStorage_Increment_ConcurrentMonotonic(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	const workers = 50

	window := time.Minute

	// Channel collects every observed post-increment count. The atomic INCR
	// inside the Lua script guarantees: for N concurrent callers, the multiset
	// of observed counts is exactly {1, 2, ..., N} with no duplicates and no
	// gaps. We do NOT assert call-order — only the set property.
	results := make(chan int64, workers)

	var wg sync.WaitGroup
	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()

			count, _, err := storage.Increment(t.Context(), "race", window)
			if err != nil {
				results <- -1
				return
			}

			results <- count
		}()
	}

	wg.Wait()
	close(results)

	seen := make(map[int64]int, workers)

	for c := range results {
		require.GreaterOrEqual(t, c, int64(1), "no error sentinel expected, no zero/negative counts")
		seen[c]++
	}

	require.Len(t, seen, workers, "every observed count must be unique")

	// Verify the multiset is exactly {1..workers} — no duplicates means the
	// atomic primitive held under contention.
	for i := int64(1); i <= int64(workers); i++ {
		assert.Equal(t, 1, seen[i], "count %d should be observed exactly once", i)
	}

	// Verify final wire-state matches expectation: counter == workers.
	count, _, err := storage.Increment(t.Context(), "race", window)
	require.NoError(t, err)
	assert.Equal(t, int64(workers+1), count)
}

func TestRedisStorage_Increment_KeyPrefixingMatchesGetSetDelete(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newTestRedisConnection(t, mr)

	storage := NewRedisStorage(conn)
	require.NotNil(t, storage)

	// Increment a key, then verify the Get/Delete shape sees the same on-the-wire
	// key by virtue of the shared keyPrefix. This is the prefix-parity contract
	// the doc comment makes explicit: Increment, Get, Set, Delete all carry the
	// "ratelimit:" prefix automatically.
	_, _, err := storage.Increment(t.Context(), "parity", time.Minute)
	require.NoError(t, err)

	// The raw value stored is the ASCII counter "1".
	val, err := storage.Get("parity")
	require.NoError(t, err)
	assert.Equal(t, []byte("1"), val)

	require.NoError(t, storage.Delete("parity"))

	val, err = storage.Get("parity")
	require.NoError(t, err)
	assert.Nil(t, val)
}
