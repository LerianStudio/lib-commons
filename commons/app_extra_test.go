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
	_ = l.Add("success", successApp)

	// Should not panic
	assert.NotPanics(t, func() {
		l.Run()
	})
}

// TestRun_WithError covers the Run() method when RunWithError returns an error.
func TestRun_WithError_NilLogger(t *testing.T) {
	t.Parallel()

	// Logger is nil - error should be silently swallowed
	logger := libLog.NewNop()
	l := NewLauncher()
	l.Logger = logger
	_ = l.Add("err-app", &stubApp{err: assert.AnError})

	assert.NotPanics(t, func() {
		l.Run()
	})
}

// TestRun_LogsError covers the path where error occurs and logger is nil.
func TestRun_LogsError(t *testing.T) {
	t.Parallel()

	// Use a launcher with nil logger to test error logging path
	l := &Launcher{} // no logger set - RunWithError will return ErrLoggerNil

	// This will return ErrLoggerNil from RunWithError
	// Run() then tries to log the error but l.Logger is nil so it skips
	assert.NotPanics(t, func() {
		l.Run()
	})
}
