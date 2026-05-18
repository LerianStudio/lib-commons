//go:build unit

package commons

import (
	"testing"

	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
)

// TestRun_NoError covers the Run() method when no error occurs.
func TestRun_NoError(t *testing.T) {
	t.Parallel()

	logger := libLog.NewNop()
	l := NewLauncher()
	l.Logger = logger

	// App that succeeds
	successApp := &stubApp{err: nil}
	assert.NoError(t, l.Add("success", successApp))

	// Should not panic
	assert.NotPanics(t, func() {
		l.Run()
	})
}

// TestRun_WithError_WithLogger covers Run() when an app returns an error and logger is configured.
func TestRun_WithError_WithLogger(t *testing.T) {
	t.Parallel()

	logger := libLog.NewNop()
	l := NewLauncher()
	l.Logger = logger
	assert.NoError(t, l.Add("err-app", &stubApp{err: assert.AnError}))

	assert.NotPanics(t, func() {
		l.Run()
	})
}

// TestRunWithError_NilLoggerReturnsErrLoggerNil covers the nil logger guard.
func TestRunWithError_NilLoggerReturnsErrLoggerNil(t *testing.T) {
	t.Parallel()

	l := &Launcher{}

	assert.ErrorIs(t, l.RunWithError(), ErrLoggerNil)
	assert.NotPanics(t, func() {
		l.Run()
	})
}
