//go:build unit

package errgroup_test

import (
	"errors"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/errgroup"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNilReceiver_SetLogger(t *testing.T) {
	t.Parallel()

	t.Run("nil pointer SetLogger does not panic", func(t *testing.T) {
		t.Parallel()

		var g *errgroup.Group

		assert.NotPanics(t, func() {
			g.SetLogger(log.NewNop())
		})
	})

	t.Run("nil pointer SetLogger with nil logger does not panic", func(t *testing.T) {
		t.Parallel()

		var g *errgroup.Group

		assert.NotPanics(t, func() {
			g.SetLogger(nil)
		})
	})
}

func TestZeroValueGroup(t *testing.T) {
	t.Parallel()

	t.Run("Go and Wait work without WithContext", func(t *testing.T) {
		t.Parallel()

		var g errgroup.Group

		g.Go(func() error {
			return nil
		})

		err := g.Wait()
		assert.NoError(t, err)
	})

	t.Run("Go returns error through Wait", func(t *testing.T) {
		t.Parallel()

		var g errgroup.Group
		expectedErr := errors.New("zero-value error")

		g.Go(func() error {
			return expectedErr
		})

		err := g.Wait()
		require.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})

	t.Run("Wait with no goroutines returns nil", func(t *testing.T) {
		t.Parallel()

		var g errgroup.Group

		err := g.Wait()
		assert.NoError(t, err)
	})

	t.Run("panic in Go recovers and returns ErrPanicRecovered", func(t *testing.T) {
		t.Parallel()

		var g errgroup.Group

		g.Go(func() error {
			panic("boom from zero-value group")
		})

		err := g.Wait()
		require.Error(t, err)
		assert.ErrorIs(t, err, errgroup.ErrPanicRecovered)
		assert.Contains(t, err.Error(), "boom from zero-value group")
	})

	t.Run("panic with nil cancel does not double-panic", func(t *testing.T) {
		t.Parallel()

		// Zero-value Group has nil cancel. The panic recovery path
		// checks cancel != nil before calling it. This test ensures
		// the nil-guard works correctly.
		var g errgroup.Group

		assert.NotPanics(t, func() {
			g.Go(func() error {
				panic("nil cancel test")
			})
			_ = g.Wait()
		})
	})

	t.Run("multiple goroutines on zero-value group", func(t *testing.T) {
		t.Parallel()

		var g errgroup.Group
		firstErr := errors.New("first")

		g.Go(func() error {
			return firstErr
		})

		g.Go(func() error {
			return errors.New("second")
		})

		g.Go(func() error {
			return nil
		})

		err := g.Wait()
		require.Error(t, err)
		// errOnce guarantees the first recorded error wins.
		// Due to goroutine scheduling, either error could be first,
		// but we'll always get exactly one error back.
		assert.True(t, err.Error() == "first" || err.Error() == "second",
			"expected error to be one of the goroutine errors, got: %v", err)
	})
}
