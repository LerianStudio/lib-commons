package license_test

import (
	"bytes"
	"os"
	"os/exec"
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
	// DefaultHandler calls os.Exit(1), so we test it in a subprocess
	if os.Getenv("TEST_DEFAULT_HANDLER_EXIT") == "1" {
		manager := license.New()
		manager.Terminate("default handler test")

		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestDefaultHandler")
	cmd.Env = append(os.Environ(), "TEST_DEFAULT_HANDLER_EXIT=1")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Verify process exited with non-zero code
	var exitErr *exec.ExitError
	if assert.ErrorAs(t, err, &exitErr) {
		assert.Equal(t, 1, exitErr.ExitCode(), "Expected exit code 1")
	} else {
		t.Fatal("Expected process to exit with code 1")
	}

	// Verify error message was printed to stderr
	assert.Contains(t, stderr.String(), "LICENSE VALIDATION FAILED")
	assert.Contains(t, stderr.String(), "default handler test")
}
