//go:build unit

package errgroup_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v4/commons/errgroup"
)

func TestWithContext_AllSucceed(t *testing.T) {
	t.Parallel()

	group, _ := errgroup.WithContext(context.Background())

	group.Go(func() error { return nil })
	group.Go(func() error { return nil })
	group.Go(func() error { return nil })

	err := group.Wait()
	assert.NoError(t, err)
}

func TestWithContext_OneError(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("something failed")
	group, groupCtx := errgroup.WithContext(context.Background())

	group.Go(func() error { return expectedErr })
	group.Go(func() error {
		<-groupCtx.Done()
		return nil
	})

	err := group.Wait()
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

func TestWithContext_MultipleErrors_ReturnsFirst(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first error")
	group, _ := errgroup.WithContext(context.Background())

	started := make(chan struct{})
	firstDone := make(chan struct{})

	group.Go(func() error {
		<-started
		close(firstDone)

		return firstErr
	})

	group.Go(func() error {
		<-started
		<-firstDone // Wait for first goroutine to signal before returning

		return errors.New("second error")
	})

	close(started)

	err := group.Wait()
	require.Error(t, err)
	assert.Equal(t, firstErr, err)
}

func TestWithContext_ZeroGoroutines(t *testing.T) {
	t.Parallel()

	group, _ := errgroup.WithContext(context.Background())

	err := group.Wait()
	assert.NoError(t, err)
}

func TestWithContext_ContextCancellation(t *testing.T) {
	t.Parallel()

	var cancelled atomic.Bool

	group, groupCtx := errgroup.WithContext(context.Background())

	group.Go(func() error {
		return errors.New("trigger cancel")
	})

	group.Go(func() error {
		<-groupCtx.Done()
		cancelled.Store(true)
		return nil
	})

	_ = group.Wait()
	assert.True(t, cancelled.Load())
}

func TestWithContext_PanicRecovery(t *testing.T) {
	t.Parallel()

	group, _ := errgroup.WithContext(context.Background())

	group.Go(func() error {
		panic("something went wrong")
	})

	err := group.Wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, errgroup.ErrPanicRecovered)
	assert.Contains(t, err.Error(), "something went wrong")
}

func TestWithContext_PanicAlongsideSuccess(t *testing.T) {
	t.Parallel()

	var completed atomic.Bool

	group, _ := errgroup.WithContext(context.Background())

	group.Go(func() error {
		panic("boom")
	})

	group.Go(func() error {
		completed.Store(true)
		return nil
	})

	err := group.Wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, errgroup.ErrPanicRecovered)
	assert.True(t, completed.Load())
}

func TestWithContext_PanicAndError_FirstWins(t *testing.T) {
	t.Parallel()

	regularErr := errors.New("regular error")
	group, _ := errgroup.WithContext(context.Background())

	started := make(chan struct{})

	// This goroutine returns a regular error first
	group.Go(func() error {
		<-started
		return regularErr
	})

	// This goroutine panics after a delay
	group.Go(func() error {
		<-started
		time.Sleep(50 * time.Millisecond)
		panic("delayed panic")
	})

	close(started)

	err := group.Wait()
	require.Error(t, err)
	// The regular error should win because it fires first
	assert.Equal(t, regularErr, err)
}

func TestWithContext_PanicWithNonStringValue(t *testing.T) {
	t.Parallel()

	group, _ := errgroup.WithContext(context.Background())

	group.Go(func() error {
		panic(42)
	})

	err := group.Wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, errgroup.ErrPanicRecovered)
}

func TestWithContext_PanicWithNilValue(t *testing.T) {
	t.Parallel()

	group, _ := errgroup.WithContext(context.Background())

	group.Go(func() error {
		panic(nil)
	})

	err := group.Wait()
	require.Error(t, err)
	assert.ErrorIs(t, err, errgroup.ErrPanicRecovered)
}

func TestWithContext_PanicCancelsContext(t *testing.T) {
	t.Parallel()

	var cancelled atomic.Bool

	group, groupCtx := errgroup.WithContext(context.Background())

	group.Go(func() error {
		panic("trigger cancel via panic")
	})

	group.Go(func() error {
		<-groupCtx.Done()
		cancelled.Store(true)
		return nil
	})

	_ = group.Wait()
	assert.True(t, cancelled.Load())
}
