//go:build unit

package license_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/license"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNilReceiver_Terminate(t *testing.T) {
	t.Parallel()

	t.Run("nil pointer Terminate does not panic", func(t *testing.T) {
		t.Parallel()

		var m *license.ManagerShutdown

		assert.NotPanics(t, func() {
			m.Terminate("nil receiver test")
		})
	})

	t.Run("nil pointer TerminateWithError does not panic and returns error", func(t *testing.T) {
		t.Parallel()

		var m *license.ManagerShutdown

		assert.NotPanics(t, func() {
			err := m.TerminateWithError("nil receiver test")
			require.Error(t, err)
			assert.ErrorIs(t, err, license.ErrManagerNotInitialized)
		})
	})

	t.Run("nil pointer TerminateSafe does not panic and returns error", func(t *testing.T) {
		t.Parallel()

		var m *license.ManagerShutdown

		assert.NotPanics(t, func() {
			err := m.TerminateSafe("nil receiver test")
			require.Error(t, err)
			assert.ErrorIs(t, err, license.ErrManagerNotInitialized)
		})
	})

	t.Run("nil pointer SetHandler does not panic", func(t *testing.T) {
		t.Parallel()

		var m *license.ManagerShutdown

		assert.NotPanics(t, func() {
			m.SetHandler(func(_ string) {})
		})
	})
}

func TestNilReceiver_WithLogger(t *testing.T) {
	t.Parallel()

	t.Run("WithLogger configures logger on new manager", func(t *testing.T) {
		t.Parallel()

		nop := log.NewNop()
		m := license.New(license.WithLogger(nop))

		// Verify the manager works — logger is used internally by TerminateWithError
		// when Logger != nil. We verify it doesn't panic and behaves correctly.
		err := m.TerminateWithError("test with logger")
		require.Error(t, err)
		assert.ErrorIs(t, err, license.ErrLicenseValidationFailed)
	})

	t.Run("WithLogger with nil logger is safe", func(t *testing.T) {
		t.Parallel()

		// WithLogger(nil) should be a no-op — Logger remains nil.
		m := license.New(license.WithLogger(nil))

		assert.NotPanics(t, func() {
			err := m.TerminateWithError("test with nil logger")
			require.Error(t, err)
		})
	})

	t.Run("WithLogger can be combined with SetHandler", func(t *testing.T) {
		t.Parallel()

		nop := log.NewNop()
		handlerCalled := false

		m := license.New(license.WithLogger(nop))
		m.SetHandler(func(reason string) {
			handlerCalled = true
			assert.Equal(t, "combo test", reason)
		})

		m.Terminate("combo test")
		assert.True(t, handlerCalled)
	})
}
