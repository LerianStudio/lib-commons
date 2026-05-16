//go:build unit

package license_test

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/license"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assertLicenseHandlerExits(t, "default handler test", func() {
		manager := license.New()
		manager.Terminate("default handler test")
	})
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
	assertLicenseHandlerExits(t, "test", func() {
		manager := license.New()
		_ = manager.TerminateSafe("test")
	})
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
	assertLicenseHandlerExits(t, "fail-closed test", func() {
		manager := license.New(license.WithFailClosed())
		manager.Terminate("fail-closed test")
	})
}

func assertLicenseHandlerExits(t *testing.T, reason string, fn func()) {
	t.Helper()

	if os.Getenv("LIB_COMMONS_LICENSE_EXIT_SUBPROCESS") == "1" {
		fn()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), "LIB_COMMONS_LICENSE_EXIT_SUBPROCESS=1")

	output, err := cmd.CombinedOutput()
	require.Error(t, err)

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 1, exitErr.ExitCode())
	assert.Contains(t, string(output), "LICENSE VALIDATION FAILED: "+reason)
}
