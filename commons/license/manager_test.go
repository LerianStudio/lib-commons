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
