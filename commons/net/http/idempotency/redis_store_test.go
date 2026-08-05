//go:build unit

package idempotency

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisStore_Lifecycle_PreservesOpaqueValueAndExpiration(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	store := newRedisStore(newRedisClient(t, mr))
	ctx := context.Background()
	key := "idempotency:tenant-a:lifecycle"
	ttl := 2 * time.Hour
	processing := []byte{0xff, 0x00, 0x7f}
	completed := []byte(`{"status":"complete"}`)

	current, acquired, err := store.Acquire(ctx, key, processing, ttl)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Nil(t, current)

	current, acquired, err = store.Acquire(ctx, key, []byte("other"), ttl)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, processing, current)

	applied, err := store.Complete(ctx, key, processing, completed, ttl)
	require.NoError(t, err)
	assert.True(t, applied)

	current, acquired, err = store.Acquire(ctx, key, []byte("other"), ttl)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, completed, current)

	mr.FastForward(ttl + time.Second)

	_, acquired, err = store.Acquire(ctx, key, []byte("fresh"), ttl)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestRedisStore_CompareSafeTransitions_RejectStaleValue(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	store := newRedisStore(newRedisClient(t, mr))
	ctx := context.Background()
	key := "idempotency:tenant-a:compare-safe"
	processing := []byte("processing")

	_, acquired, err := store.Acquire(ctx, key, processing, time.Hour)
	require.NoError(t, err)
	require.True(t, acquired)

	tests := []struct {
		name       string
		transition func() (bool, error)
	}{
		{
			name: "stale completion",
			transition: func() (bool, error) {
				return store.Complete(ctx, key, []byte("stale"), []byte("completed"), time.Hour)
			},
		},
		{
			name: "stale release",
			transition: func() (bool, error) {
				return store.Release(ctx, key, []byte("stale"))
			},
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			applied, transitionErr := testCase.transition()
			require.NoError(t, transitionErr)
			assert.False(t, applied)
		})
	}

	current, acquired, err := store.Acquire(ctx, key, []byte("other"), time.Hour)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, processing, current)
}

func TestRedisStore_ExpiredValueCannotMutateReplacement(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	store := newRedisStore(newRedisClient(t, mr))
	ctx := context.Background()
	key := "idempotency:tenant-a:replacement"
	expired := []byte("expired")
	replacement := []byte("replacement")

	_, acquired, err := store.Acquire(ctx, key, expired, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	mr.FastForward(time.Minute + time.Second)

	_, acquired, err = store.Acquire(ctx, key, replacement, time.Hour)
	require.NoError(t, err)
	require.True(t, acquired)

	completed, err := store.Complete(ctx, key, expired, []byte("stale-completion"), time.Hour)
	require.NoError(t, err)
	assert.False(t, completed)

	released, err := store.Release(ctx, key, expired)
	require.NoError(t, err)
	assert.False(t, released)

	current, acquired, err := store.Acquire(ctx, key, []byte("other"), time.Hour)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, replacement, current)
}

func TestRedisStore_ConcurrentAcquire_HasSingleWinner(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	store := newRedisStore(newRedisClient(t, mr))
	ctx := context.Background()

	const workers = 32

	start := make(chan struct{})
	results := make(chan bool, workers)
	errorsCh := make(chan error, workers)

	var group sync.WaitGroup

	for index := range workers {
		group.Go(func() {
			<-start

			_, acquired, err := store.Acquire(ctx, "idempotency:tenant-a:concurrent",
				[]byte(fmt.Sprintf("candidate-%d", index)), time.Hour)
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

	assert.Equal(t, 1, winners)
}
