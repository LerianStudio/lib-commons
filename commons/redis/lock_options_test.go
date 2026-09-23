//go:build unit

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestLockWithServer is setupTestLock with the miniredis handle exposed,
// which is what lets a test read a key's TTL and move the clock forward.
func setupTestLockWithServer(t *testing.T) (*miniredis.Miniredis, *RedisLockManager) {
	t.Helper()

	mr := miniredis.RunT(t)

	client, err := New(context.Background(), Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: mr.Addr()},
		},
		Logger: obs.Nop(),
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, client.Close())
		mr.Close()
	})

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	return mr, lock
}

// TestTryLockWithOptions_HonoursExpiry is the reason this entry point exists:
// TryLock hard-codes a 10s expiry, so a caller needing a different TTL on a
// single attempt previously had to accept the wrong one.
func TestTryLockWithOptions_HonoursExpiry(t *testing.T) {
	mr, lock := setupTestLockWithServer(t)
	ctx := context.Background()

	const key = "test:try-opts:expiry"

	opts := DefaultLockOptions()
	opts.Expiry = 200 * time.Millisecond
	opts.Tries = 1

	handle, acquired, err := lock.TryLockWithOptions(ctx, key, opts)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle)

	assert.InDelta(t, opts.Expiry.Seconds(), mr.TTL(key).Seconds(), 0.05,
		"the key was written with an expiry other than the one supplied")

	// Past the expiry the key is gone, so a second attempt takes the lock even
	// though the first holder never unlocked.
	mr.FastForward(300 * time.Millisecond)

	second, acquired, err := lock.TryLockWithOptions(ctx, key, opts)
	require.NoError(t, err)
	assert.True(t, acquired, "the supplied expiry was ignored: the lock outlived it")

	if second != nil {
		_ = second.Unlock(ctx)
	}
}

// TestTryLockWithOptions_HonoursTries pins that Tries is passed through rather
// than forced to 1 as TryLock does. Retrying is observed two independent ways —
// the extra commands miniredis served and the retry delays that had to elapse —
// so neither pins redsync's internal command mix on its own.
func TestTryLockWithOptions_HonoursTries(t *testing.T) {
	mr, lock := setupTestLockWithServer(t)
	ctx := context.Background()

	const (
		key        = "test:try-opts:tries"
		retryDelay = 20 * time.Millisecond
	)

	held, acquired, err := lock.TryLock(ctx, key)
	require.NoError(t, err)
	require.True(t, acquired)

	defer func() { _ = held.Unlock(ctx) }()

	opts := LockOptions{Expiry: time.Second, Tries: 1, RetryDelay: retryDelay, DriftFactor: 0.01}

	before := mr.Server().TotalCommands()
	handle, acquired, err := lock.TryLockWithOptions(ctx, key, opts)
	singleAttempt := mr.Server().TotalCommands() - before

	require.NoError(t, err, "contention is not an error on this entry point")
	require.False(t, acquired)
	require.Nil(t, handle)

	opts.Tries = 3

	before = mr.Server().TotalCommands()
	start := time.Now()
	handle, acquired, err = lock.TryLockWithOptions(ctx, key, opts)
	elapsed := time.Since(start)
	threeAttempts := mr.Server().TotalCommands() - before

	require.NoError(t, err)
	assert.False(t, acquired, "a contended lock must report (nil, false, nil) however many tries were configured")
	assert.Nil(t, handle)

	assert.Greater(t, threeAttempts, singleAttempt,
		"Tries was not passed through: three tries served no more commands than one")
	assert.GreaterOrEqual(t, elapsed, 2*retryDelay,
		"Tries was not passed through: the call returned before two retry delays could elapse")
}

// TestTryLockWithOptions_RejectsInvalidOptions pins that this entry point
// validates like WithLockOptions instead of handing redsync nonsense.
func TestTryLockWithOptions_RejectsInvalidOptions(t *testing.T) {
	_, lock := setupTestLock(t)

	valid := DefaultLockOptions()

	tests := []struct {
		name string
		opts LockOptions
		want error
	}{
		{name: "zero expiry", opts: LockOptions{Expiry: 0, Tries: 1, DriftFactor: 0.01}, want: ErrLockExpiryInvalid},
		{name: "zero tries", opts: LockOptions{Expiry: time.Second, Tries: 0, DriftFactor: 0.01}, want: ErrLockTriesInvalid},
		{name: "tries above maximum", opts: LockOptions{Expiry: time.Second, Tries: maxLockTries + 1, DriftFactor: 0.01}, want: ErrLockTriesExceeded},
		{name: "negative retry delay", opts: LockOptions{Expiry: time.Second, Tries: 1, RetryDelay: -time.Second, DriftFactor: 0.01}, want: ErrLockRetryDelayNegative},
		{name: "drift factor of one", opts: LockOptions{Expiry: time.Second, Tries: 1, DriftFactor: 1}, want: ErrLockDriftFactorInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handle, acquired, err := lock.TryLockWithOptions(context.Background(), "test:try-opts:invalid", tt.opts)

			require.ErrorIs(t, err, tt.want)
			assert.False(t, acquired)
			assert.Nil(t, handle)
		})
	}

	// The guards that do not depend on the options still fire first.
	handle, acquired, err := lock.TryLockWithOptions(context.Background(), "   ", valid)
	require.ErrorIs(t, err, ErrEmptyLockKey)
	assert.False(t, acquired)
	assert.Nil(t, handle)

	var nilManager *RedisLockManager

	handle, acquired, err = nilManager.TryLockWithOptions(context.Background(), "test:try-opts:nil", valid)
	require.ErrorIs(t, err, ErrNilLockManager)
	assert.False(t, acquired)
	assert.Nil(t, handle)
}

// TestTryLock_MatchesTryLockWithOptionsDefaults pins the delegation: TryLock is
// TryLockWithOptions with DefaultLockOptions() and Tries forced to 1, so the two
// must write the same TTL. This is the guard against TryLock quietly changing
// TTL when it was rewritten to delegate.
func TestTryLock_MatchesTryLockWithOptionsDefaults(t *testing.T) {
	mr, lock := setupTestLockWithServer(t)
	ctx := context.Background()

	const (
		viaTryLock = "test:try-opts:parity:trylock"
		viaOptions = "test:try-opts:parity:options"
	)

	plain, acquired, err := lock.TryLock(ctx, viaTryLock)
	require.NoError(t, err)
	require.True(t, acquired)

	defer func() { _ = plain.Unlock(ctx) }()

	opts := DefaultLockOptions()
	opts.Tries = 1

	explicit, acquired, err := lock.TryLockWithOptions(ctx, viaOptions, opts)
	require.NoError(t, err)
	require.True(t, acquired)

	defer func() { _ = explicit.Unlock(ctx) }()

	assert.Equal(t, mr.TTL(viaOptions), mr.TTL(viaTryLock),
		"TryLock no longer behaves like TryLockWithOptions with the default expiry")
	assert.Equal(t, DefaultLockOptions().Expiry, mr.TTL(viaTryLock),
		"TryLock stopped using the default 10s expiry")
}

// TestTryLockWithOptions_CallerCancellationIsAnError pins the half of TryLock's
// contract that only bites once Tries is above one: a caller that has given up
// is not a busy lock. Reported as "busy", a worker following this method's own
// godoc example returns nil and reports a skipped cycle as success.
func TestTryLockWithOptions_CallerCancellationIsAnError(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handle, acquired, err := lock.TryLockWithOptions(ctx, "test:try-opts:cancelled", LockOptions{
		Expiry:      time.Second,
		Tries:       3,
		RetryDelay:  10 * time.Millisecond,
		DriftFactor: 0.01,
	})

	require.Error(t, err, "a cancelled caller was reported as a busy lock")
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, acquired)
	assert.Nil(t, handle)
}

// TestTryLockWithOptions_StalledRedisIsAnError is the same guard for the other
// failure: a Redis that stopped answering must not read as a busy lock either,
// or a sweep skips every cycle for the whole outage and reports success each
// time.
func TestTryLockWithOptions_StalledRedisIsAnError(t *testing.T) {
	lock := setupStalledLock(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	handle, acquired, err := lock.TryLockWithOptions(ctx, "test:try-opts:stalled", LockOptions{
		Expiry:      5 * time.Second,
		Tries:       5,
		RetryDelay:  100 * time.Millisecond,
		DriftFactor: 0.01,
	})

	require.Error(t, err, "a Redis that stopped answering was reported as a busy lock")
	assert.False(t, acquired)
	assert.Nil(t, handle)
}

// TestTryLock_MakesASingleAttempt pins the guarantee TryLock's own godoc makes —
// "without retrying" — against the commands miniredis actually served, so the
// single attempt survives the value moving out of the mutex construction. A
// retrying TryLock blocks for Tries x RetryDelay (a second at the defaults) on
// every contended call.
func TestTryLock_MakesASingleAttempt(t *testing.T) {
	mr, lock := setupTestLockWithServer(t)
	ctx := context.Background()

	const key = "test:try-opts:single-attempt"

	held, acquired, err := lock.TryLock(ctx, key)
	require.NoError(t, err)
	require.True(t, acquired)

	defer func() { _ = held.Unlock(ctx) }()

	opts := DefaultLockOptions()
	opts.Tries = 1

	// Warm the connection pool first: the handshake commands go-redis serves on
	// a fresh connection would otherwise be counted as an extra attempt.
	_, _, err = lock.TryLockWithOptions(ctx, key, opts)
	require.NoError(t, err)

	before := mr.Server().TotalCommands()
	_, acquired, err = lock.TryLockWithOptions(ctx, key, opts)
	singleAttempt := mr.Server().TotalCommands() - before

	require.NoError(t, err)
	require.False(t, acquired)

	before = mr.Server().TotalCommands()
	start := time.Now()
	_, acquired, err = lock.TryLock(ctx, key)
	elapsed := time.Since(start)
	viaTryLock := mr.Server().TotalCommands() - before

	require.NoError(t, err)
	require.False(t, acquired)

	// Two independent mechanisms, because either alone can be satisfied by
	// accident: the commands a second attempt would have to serve, and the
	// retry delay a second attempt would have to wait out.
	assert.Equal(t, singleAttempt, viaTryLock,
		"TryLock stopped making exactly one attempt: its godoc promises no retrying")
	assert.Less(t, elapsed, DefaultLockOptions().RetryDelay,
		"TryLock waited out a retry delay: a non-blocking probe became a call that blocks for Tries x RetryDelay")
}

// TestTryLockWithOptions_HonoursDriftFactor pins the last option that reaches
// redsync. Dropping it silently reverts to redsync's own default, which is the
// same 0.01 every other test uses — so only a caller asking for a different one
// can tell.
func TestTryLockWithOptions_HonoursDriftFactor(t *testing.T) {
	_, lock := setupTestLock(t)
	ctx := context.Background()

	const (
		key    = "test:try-opts:drift"
		expiry = 10 * time.Second
	)

	handle, acquired, err := lock.TryLockWithOptions(ctx, key, LockOptions{
		Expiry:      expiry,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.5,
	})
	require.NoError(t, err)
	require.True(t, acquired)

	defer func() { _ = handle.Unlock(ctx) }()

	inner, ok := handle.(*lockHandle)
	require.True(t, ok)

	// redsync discards expiry x DriftFactor from the validity window it vouches
	// for. At its default 0.01 that window is ~9.9s, so half an expiry of slack
	// is only reachable if the supplied factor was the one used.
	validity := time.Until(inner.mutex.Until())

	assert.Less(t, validity, expiry*3/4, "DriftFactor was not passed through to redsync")
	assert.Greater(t, validity, expiry/4, "the drift allowance swallowed the whole validity window")
}
