package license_test

import (
	"errors"
	"testing"

	"github.com/LerianStudio/lib-commons/v2/commons/license"
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

	assert.Panics(t, func() {
		manager.Terminate("default handler test")
	}, "Default handler should panic")
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

func TestTerminate_UninitializedManagerPanics(t *testing.T) {
	// Terminate requires a handler to be set. On a zero-value manager,
	// the handler is nil, causing a panic with ErrManagerNotInitialized.
	var manager license.ManagerShutdown

	assert.Panics(t, func() {
		manager.Terminate("test reason")
	}, "Terminate on uninitialized manager should panic")
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
