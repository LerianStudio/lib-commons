//go:build unit

package license_test

import (
	"errors"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/license"
	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	manager := license.New()
	assert.NotNil(t, manager, "New should return a non-nil manager")
}

func TestSetHandler(t *testing.T) {
	manager := license.New()
	handlerCalled := false
	testHandler := func(reason string) {
		handlerCalled = true
	}

	manager.SetHandler(testHandler)
	manager.Terminate("test")

	assert.True(t, handlerCalled, "Custom handler should be called")
}

func TestSetHandlerWithNil(t *testing.T) {
	manager := license.New()
	handlerCalled := false
	testHandler := func(reason string) {
		handlerCalled = true
	}

	manager.SetHandler(testHandler)
	manager.SetHandler(nil) // This should not change the handler
	manager.Terminate("test")

	assert.True(t, handlerCalled, "Original handler should still be called when nil is passed")
}

func TestDefaultHandler(t *testing.T) {
	manager := license.New()

	assert.NotPanics(t, func() {
		manager.Terminate("default handler test")
	}, "Default handler should not panic")
}

func TestDefaultHandlerWithError(t *testing.T) {
	err := license.DefaultHandlerWithError("test reason")

	assert.Error(t, err)
	assert.True(t, errors.Is(err, license.ErrLicenseValidationFailed))
	assert.Contains(t, err.Error(), "test reason")
}

func TestTerminateWithError(t *testing.T) {
	manager := license.New()

	err := manager.TerminateWithError("validation failed")

	assert.Error(t, err)
	assert.True(t, errors.Is(err, license.ErrLicenseValidationFailed))
	assert.Contains(t, err.Error(), "validation failed")
}

func TestErrLicenseValidationFailed(t *testing.T) {
	assert.NotNil(t, license.ErrLicenseValidationFailed)
	assert.Equal(t, "license validation failed", license.ErrLicenseValidationFailed.Error())
}

func TestErrManagerNotInitialized(t *testing.T) {
	assert.NotNil(t, license.ErrManagerNotInitialized)
	assert.Contains(t, license.ErrManagerNotInitialized.Error(), "license.ManagerShutdown used without initialization")
}

func TestTerminateWithError_UninitializedManager(t *testing.T) {
	// TerminateWithError does not require initialization and works on zero-value manager.
	// This is by design: TerminateWithError always returns an error without invoking
	// any handler, so it doesn't need the manager to be properly initialized.
	var manager license.ManagerShutdown

	err := manager.TerminateWithError("test reason")

	assert.Error(t, err)
	assert.True(t, errors.Is(err, license.ErrLicenseValidationFailed))
	assert.Contains(t, err.Error(), "test reason")
}

func TestTerminate_UninitializedManagerDoesNotPanic(t *testing.T) {
	// Terminate on zero-value manager should fail safely without panic.
	var manager license.ManagerShutdown

	assert.NotPanics(t, func() {
		manager.Terminate("test reason")
	}, "Terminate on uninitialized manager should not panic")
}

func TestDefaultHandlerWithError_EmptyReason(t *testing.T) {
	err := license.DefaultHandlerWithError("")

	assert.Error(t, err)
	assert.True(t, errors.Is(err, license.ErrLicenseValidationFailed))
}

func TestTerminateWithError_EmptyReason(t *testing.T) {
	manager := license.New()

	err := manager.TerminateWithError("")

	assert.Error(t, err)
	assert.True(t, errors.Is(err, license.ErrLicenseValidationFailed))
}

func TestTerminateSafe_Success(t *testing.T) {
	manager := license.New()
	handlerCalled := false
	testHandler := func(reason string) {
		handlerCalled = true
	}

	manager.SetHandler(testHandler)
	err := manager.TerminateSafe("test")

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called")
}

func TestTerminateSafe_UninitializedManager(t *testing.T) {
	var manager license.ManagerShutdown

	err := manager.TerminateSafe("test reason")

	assert.Error(t, err)
	assert.True(t, errors.Is(err, license.ErrManagerNotInitialized))
}

func TestTerminateSafe_WithDefaultHandler(t *testing.T) {
	manager := license.New()

	err := manager.TerminateSafe("test")
	assert.NoError(t, err)
}

func TestNew_NilOptionSkipped(t *testing.T) {
	t.Parallel()

	// Nil options in the variadic list should be silently skipped.
	assert.NotPanics(t, func() {
		manager := license.New(nil, nil)
		assert.NotNil(t, manager)
	})
}

func TestNew_NilOptionMixedWithValid(t *testing.T) {
	t.Parallel()

	handlerCalled := false
	customHandler := func(reason string) {
		handlerCalled = true
	}

	// Mix nil options with valid options.
	manager := license.New(nil, license.WithLogger(nil), nil)
	assert.NotNil(t, manager)

	manager.SetHandler(customHandler)
	manager.Terminate("test")
	assert.True(t, handlerCalled)
}

func TestWithFailClosed(t *testing.T) {
	t.Parallel()

	// WithFailClosed should set the handler to TerminateSafe behavior.
	manager := license.New(license.WithFailClosed())

	// TerminateSafe returns nil when handler is non-nil (it invokes handler then returns nil).
	// The WithFailClosed handler itself calls TerminateSafe internally.
	// Since the manager IS initialized (New was called), the handler should not return errors.
	assert.NotPanics(t, func() {
		manager.Terminate("fail-closed test")
	})
}
