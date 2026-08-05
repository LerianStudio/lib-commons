// Package idempotencytest provides backend-agnostic contract tests for
// idempotency.Store implementations.
package idempotencytest

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/net/http/idempotency"
	"github.com/stretchr/testify/require"
)

const (
	contractTTL        = time.Minute
	expirationTTL      = 50 * time.Millisecond
	contractTimeout    = 3 * time.Second
	concurrentAcquires = 16
)

// Factory constructs a fresh isolated store for one contract subtest.
type Factory func(t *testing.T) idempotency.Store

// Run executes the complete idempotency store contract.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	require.NotNil(t, factory)

	t.Run("AcquireReturnsOpaqueCurrentValue", func(t *testing.T) {
		testAcquireReturnsOpaqueCurrentValue(t, factory)
	})
	t.Run("CompleteIsCompareSafe", func(t *testing.T) {
		testCompleteIsCompareSafe(t, factory)
	})
	t.Run("ReleaseIsCompareSafe", func(t *testing.T) {
		testReleaseIsCompareSafe(t, factory)
	})
	t.Run("ExpirationAllowsFreshAcquire", func(t *testing.T) {
		testExpirationAllowsFreshAcquire(t, factory)
	})
	t.Run("PositiveSubMillisecondTTLIsAccepted", func(t *testing.T) {
		testPositiveSubMillisecondTTLIsAccepted(t, factory)
	})
	t.Run("ConcurrentAcquireHasSingleWinner", func(t *testing.T) {
		testConcurrentAcquireHasSingleWinner(t, factory)
	})
	t.Run("CompleteFailurePreservesProcessingValue", func(t *testing.T) {
		testCompleteFailurePreservesProcessingValue(t, factory)
	})
}

func testAcquireReturnsOpaqueCurrentValue(t *testing.T, factory Factory) {
	t.Helper()

	store := factory(t)
	require.NotNil(t, store)
	ctx := contractContext(t)
	candidate := []byte{0xff, 0x00, 0x7f}

	current, acquired, err := store.Acquire(ctx, "contract:acquire", candidate, contractTTL)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Nil(t, current)

	current, acquired, err = store.Acquire(ctx, "contract:acquire", []byte("other"), contractTTL)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Equal(t, candidate, current)
}

func testCompleteIsCompareSafe(t *testing.T, factory Factory) {
	t.Helper()

	store := factory(t)
	ctx := contractContext(t)
	key := "contract:complete"
	processing := []byte("processing")
	completed := []byte{0xff, 0x00, 0x7f}

	_, acquired, err := store.Acquire(ctx, key, processing, contractTTL)
	require.NoError(t, err)
	require.True(t, acquired)

	applied, err := store.Complete(ctx, key, []byte("stale"), completed, contractTTL)
	require.NoError(t, err)
	require.False(t, applied)

	applied, err = store.Complete(ctx, key, processing, completed, contractTTL)
	require.NoError(t, err)
	require.True(t, applied)

	current, acquired, err := store.Acquire(ctx, key, []byte("replacement"), contractTTL)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Equal(t, completed, current)
}

func testReleaseIsCompareSafe(t *testing.T, factory Factory) {
	t.Helper()

	store := factory(t)
	ctx := contractContext(t)
	key := "contract:release"
	processing := []byte("processing")

	_, acquired, err := store.Acquire(ctx, key, processing, contractTTL)
	require.NoError(t, err)
	require.True(t, acquired)

	applied, err := store.Release(ctx, key, []byte("stale"))
	require.NoError(t, err)
	require.False(t, applied)

	applied, err = store.Release(ctx, key, processing)
	require.NoError(t, err)
	require.True(t, applied)

	_, acquired, err = store.Acquire(ctx, key, []byte("replacement"), contractTTL)
	require.NoError(t, err)
	require.True(t, acquired)
}

func testExpirationAllowsFreshAcquire(t *testing.T, factory Factory) {
	t.Helper()

	store := factory(t)
	ctx := contractContext(t)
	key := "contract:expiration"

	_, acquired, err := store.Acquire(ctx, key, []byte("first"), expirationTTL)
	require.NoError(t, err)
	require.True(t, acquired)

	ticker := time.NewTicker(expirationTTL)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("idempotency record did not expire: %v", ctx.Err())
		case <-ticker.C:
			_, fresh, acquireErr := store.Acquire(ctx, key, []byte("second"), contractTTL)
			require.NoError(t, acquireErr)

			if fresh {
				return
			}
		}
	}
}

func testPositiveSubMillisecondTTLIsAccepted(t *testing.T, factory Factory) {
	t.Helper()

	store := factory(t)
	ctx := contractContext(t)

	_, acquired, err := store.Acquire(ctx, "contract:sub-millisecond", []byte("candidate"), time.Nanosecond)
	require.NoError(t, err)
	require.True(t, acquired)
}

func testConcurrentAcquireHasSingleWinner(t *testing.T, factory Factory) {
	t.Helper()

	store := factory(t)
	ctx := contractContext(t)
	start := make(chan struct{})
	results := make(chan bool, concurrentAcquires)
	errorsCh := make(chan error, concurrentAcquires)

	var group sync.WaitGroup

	for index := range concurrentAcquires {
		group.Go(func() {
			<-start

			_, acquired, err := store.Acquire(ctx, "contract:concurrent",
				fmt.Appendf(nil, "candidate-%d", index), contractTTL)
			if err != nil {
				errorsCh <- err

				return
			}

			results <- acquired
		})
	}

	close(start)
	group.Wait()
	close(results)
	close(errorsCh)

	for err := range errorsCh {
		require.NoError(t, err)
	}

	winners := 0

	for acquired := range results {
		if acquired {
			winners++
		}
	}

	require.Equal(t, 1, winners)

	current, acquired, err := store.Acquire(ctx, "contract:concurrent", []byte("late"), contractTTL)
	require.NoError(t, err)
	require.False(t, acquired)
	require.True(t, bytes.HasPrefix(current, []byte("candidate-")))
}

func testCompleteFailurePreservesProcessingValue(t *testing.T, factory Factory) {
	t.Helper()

	store := factory(t)
	ctx := contractContext(t)
	key := "contract:complete-failure"
	processing := []byte("processing")

	_, acquired, err := store.Acquire(ctx, key, processing, contractTTL)
	require.NoError(t, err)
	require.True(t, acquired)

	_, err = store.Complete(ctx, key, processing, []byte("completed"), 0)
	require.Error(t, err)

	current, acquired, err := store.Acquire(ctx, key, []byte("replacement"), contractTTL)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Equal(t, processing, current)
}

func contractContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), contractTimeout)
	t.Cleanup(cancel)

	return ctx
}
