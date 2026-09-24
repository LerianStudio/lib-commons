//go:build unit

package redis

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	libobs "github.com/LerianStudio/lib-observability/v4"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redsync/redsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
)

// setupExtendLock returns a lock manager over a miniredis the test can
// fast-forward, which is how a lease is made to age or expire.
func setupExtendLock(t *testing.T) (*miniredis.Miniredis, *RedisLockManager) {
	t.Helper()

	mr := miniredis.RunT(t)

	client, err := New(context.Background(), Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: mr.Addr()}},
		Logger:   obs.Nop(),
	})
	require.NoError(t, err)

	t.Cleanup(func() { _ = client.Close() })

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	return mr, lock
}

// acquireExtender takes key with a 7s expiry — deliberately not the 10s
// default, so a renewal that fell back to the default would be caught.
func acquireExtender(t *testing.T, lock *RedisLockManager, key string) LockExtender {
	t.Helper()

	opts := DefaultLockOptions()
	opts.Expiry = 7 * time.Second
	opts.Tries = 1

	handle, acquired, err := lock.TryLockWithOptions(context.Background(), key, opts)
	require.NoError(t, err)
	require.True(t, acquired)

	extender, ok := handle.(LockExtender)
	require.True(t, ok, "the handle TryLockWithOptions returns must implement LockExtender")

	return extender
}

func TestLockHandle_Extend_RenewsTheLeaseToTheAcquiredExpiry(t *testing.T) {
	mr, lock := setupExtendLock(t)
	extender := acquireExtender(t, lock, "test:extend:renew")

	mr.FastForward(5 * time.Second)
	require.Equal(t, 2*time.Second, mr.TTL("test:extend:renew"))

	renewed, err := extender.Extend(context.Background())
	require.NoError(t, err)
	assert.True(t, renewed)
	assert.Equal(t, 7*time.Second, mr.TTL("test:extend:renew"), "Extend must renew to the expiry the lock was acquired with")
}

func TestLockHandle_Extend_AfterExpiryReportsLost(t *testing.T) {
	mr, lock := setupExtendLock(t)
	extender := acquireExtender(t, lock, "test:extend:expired")

	mr.FastForward(8 * time.Second)

	renewed, err := extender.Extend(context.Background())
	require.NoError(t, err)
	assert.False(t, renewed)
	assert.False(t, mr.Exists("test:extend:expired"), "a lost lease must not be resurrected")
}

func TestLockHandle_Extend_AfterAnotherHolderTookTheKeyReportsLost(t *testing.T) {
	mr, lock := setupExtendLock(t)
	extender := acquireExtender(t, lock, "test:extend:stolen")

	mr.FastForward(8 * time.Second)

	other, acquired, err := lock.TryLock(context.Background(), "test:extend:stolen")
	require.NoError(t, err)
	require.True(t, acquired)

	t.Cleanup(func() { _ = other.Unlock(context.Background()) })

	otherValue, err := mr.Get("test:extend:stolen")
	require.NoError(t, err)

	renewed, err := extender.Extend(context.Background())
	require.NoError(t, err)
	assert.False(t, renewed)

	stillOther, err := mr.Get("test:extend:stolen")
	require.NoError(t, err)
	assert.Equal(t, otherValue, stillOther, "Extend must never touch another holder's key")
	assert.Equal(t, 10*time.Second, mr.TTL("test:extend:stolen"), "the other holder's lease must be left as it was")
}

// TestLockHandle_Extend_ConcurrentCallsOnOneHandle pins that a handle shared by
// goroutines can be extended concurrently: run under -race.
func TestLockHandle_Extend_ConcurrentCallsOnOneHandle(t *testing.T) {
	_, lock := setupExtendLock(t)
	extender := acquireExtender(t, lock, "test:extend:concurrent")

	const callers = 8

	var wg sync.WaitGroup

	errs := make(chan error, callers)

	for range callers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			renewed, err := extender.Extend(context.Background())
			if err == nil && !renewed {
				err = errors.New("lease reported lost")
			}

			errs <- err
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
}

// TestLockHandle_Extend_ExhaustedRenewalIsInconclusive builds redsync's
// ErrExtendFailed deterministically: the quorum accepts the touch (value
// matches, PEXPIRE succeeds) but a 1µs expiry leaves no validity window, so
// redsync cannot vouch for the lease. That is not a lost lease.
func TestLockHandle_Extend_ExhaustedRenewalIsInconclusive(t *testing.T) {
	mr, lock := setupExtendLock(t)
	acquireExtender(t, lock, "test:extend:exhausted")

	value, err := mr.Get("test:extend:exhausted")
	require.NoError(t, err)

	exhausted := &lockHandle{
		mutex: lock.redsync.NewMutex("test:extend:exhausted",
			redsync.WithValue(value), redsync.WithExpiry(time.Microsecond), redsync.WithTries(1)),
		logger: obs.Nop(),
	}

	recorder := recordSpans(t)
	logger := &recordingLogger{}
	ctx := libobs.ContextWithLogger(context.Background(), logger)

	renewed, err := exhausted.Extend(ctx)
	require.ErrorIs(t, err, redsync.ErrExtendFailed)
	assert.False(t, renewed)

	entries := logger.loggedEntries()
	require.Len(t, entries, 1, "Extend logs exactly one line per outcome; got %+v", entries)
	assert.Equal(t, obs.LevelError, entries[0].level)
	assert.Equal(t, codes.Error, lockSpanStatus(t, recorder, "redis.lock.extend").Status().Code)
}

func TestLockHandle_Extend_CancelledContextReturnsTheCallersError(t *testing.T) {
	_, lock := setupExtendLock(t)
	extender := acquireExtender(t, lock, "test:extend:cancelled")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	renewed, err := extender.Extend(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, renewed)
}

func TestLockHandle_Extend_NilHandle(t *testing.T) {
	var h *lockHandle

	renewed, err := h.Extend(context.Background())
	require.ErrorIs(t, err, ErrNilLockHandle)
	assert.False(t, renewed)
}

// TestLockExtender_IsOptional pins that LockExtender is a separate capability:
// a LockHandle from another implementation (a test mock) need not carry it,
// and callers discover it with a type assertion.
func TestLockExtender_IsOptional(t *testing.T) {
	var handle LockHandle = unlockOnlyHandle{}

	_, ok := handle.(LockExtender)
	assert.False(t, ok)

	var _ LockExtender = (*lockHandle)(nil)
}

type unlockOnlyHandle struct{}

func (unlockOnlyHandle) Unlock(context.Context) error { return nil }

func TestLockHandle_Extend_OutcomeSeverity(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(mr *miniredis.Miniredis)
		wantLevel int
		wantSpan  codes.Code
		wantErr   bool
	}{
		{name: "renewed is debug", prepare: func(*miniredis.Miniredis) {}, wantLevel: obs.LevelDebug, wantSpan: codes.Unset},
		{name: "lost lease is a warning, not an error", prepare: func(mr *miniredis.Miniredis) { mr.FastForward(8 * time.Second) }, wantLevel: obs.LevelWarn, wantSpan: codes.Unset},
		{name: "unreachable redis is an error", prepare: func(mr *miniredis.Miniredis) { mr.Close() }, wantLevel: obs.LevelError, wantSpan: codes.Error, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr, lock := setupExtendLock(t)
			extender := acquireExtender(t, lock, "test:extend:severity")
			tt.prepare(mr)

			recorder := recordSpans(t)
			logger := &recordingLogger{}
			ctx := libobs.ContextWithLogger(context.Background(), logger)

			_, err := extender.Extend(ctx)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			entries := logger.loggedEntries()
			require.Len(t, entries, 1, "Extend logs exactly one line per outcome; got %+v", entries)
			assert.Equal(t, tt.wantLevel, entries[0].level)

			span := lockSpanStatus(t, recorder, "redis.lock.extend")
			assert.Equal(t, tt.wantSpan, span.Status().Code)
		})
	}
}

// TestLockHandle_Unlock_WaitsForAnInFlightExtend pins the ordering: a release
// never completes while a renewal is in flight on the same handle, so Extend
// cannot answer true for a lease Unlock already gave up.
func TestLockHandle_Unlock_WaitsForAnInFlightExtend(t *testing.T) {
	_, lock := setupExtendLock(t)
	extender := acquireExtender(t, lock, "test:extend:unlock-waits")

	handle, ok := extender.(*lockHandle)
	require.True(t, ok)

	handle.leaseMu.Lock()

	released := make(chan error, 1)

	go func() { released <- handle.Unlock(context.Background()) }()

	select {
	case err := <-released:
		t.Fatalf("Unlock returned %v while a renewal held the handle", err)
	case <-time.After(50 * time.Millisecond):
	}

	handle.leaseMu.Unlock()

	select {
	case err := <-released:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Unlock did not return after the renewal finished")
	}
}
