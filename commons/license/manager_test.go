// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package license_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/LerianStudio/lib-commons/v3/commons/license"
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

// runSubprocessTest runs the named test in a subprocess with the given env var set to "1".
// It asserts the process exits with code 1 and stderr contains "LICENSE VALIDATION FAILED"
// plus any additional expected messages.
func runSubprocessTest(t *testing.T, testName, envVar string, expectedMessages ...string) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run="+testName)
	cmd.Env = append(os.Environ(), envVar+"=1")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		assert.Equal(t, 1, exitErr.ExitCode(), "Expected exit code 1")
	} else {
		t.Fatal("Expected process to exit with code 1")
	}

	assert.Contains(t, stderr.String(), "LICENSE VALIDATION FAILED")

	for _, msg := range expectedMessages {
		assert.Contains(t, stderr.String(), msg)
	}
}

func TestDefaultHandler(t *testing.T) {
	// DefaultHandler calls os.Exit(1), so we test it in a subprocess
	if os.Getenv("TEST_DEFAULT_HANDLER_EXIT") == "1" {
		manager := license.New()
		manager.Terminate("default handler test")

		return
	}

	runSubprocessTest(t, "TestDefaultHandler", "TEST_DEFAULT_HANDLER_EXIT", "default handler test")
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
	// DefaultHandler calls os.Exit(1), so we test it in a subprocess
	if os.Getenv("TEST_TERMINATE_SAFE_DEFAULT_EXIT") == "1" {
		manager := license.New()
		_ = manager.TerminateSafe("test")

		return
	}

	runSubprocessTest(t, "TestTerminateSafe_WithDefaultHandler", "TEST_TERMINATE_SAFE_DEFAULT_EXIT")
}
