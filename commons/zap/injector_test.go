package zap

// Note on error path testing for InitializeLoggerWithError:
// The zap logger Build() function only returns an error in cases that are
// difficult to simulate in unit tests (e.g., invalid output paths, encoder errors).
// With the default configuration used in InitializeLoggerWithError, the Build()
// call is very unlikely to fail.
//
// The error path IS covered in InitializeLoggerWithError (injector.go):
// When zap.Build() fails, the function returns a wrapped error via
// fmt.Errorf("can't initialize zap logger: %w", err)
// This ensures proper error chaining for callers using errors.Is() or errors.As().
//
// To trigger an actual error in Build(), one would need to:
//   - Provide an invalid output path (not possible with current implementation)
//   - Corrupt the zap configuration (not exposed)
//
// Therefore, error handling exists and is correct, but cannot be easily tested
// without modifying the production code to accept external configuration.

import (
	"bytes"
	"log"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInitializeLogger(t *testing.T) {
	t.Setenv("ENV_NAME", "production")

	logger := InitializeLogger()
	assert.NotNil(t, logger)
}

func TestInitializeLoggerWithError_Success(t *testing.T) {
	t.Setenv("ENV_NAME", "production")

	logger, err := InitializeLoggerWithError()

	assert.NoError(t, err)
	assert.NotNil(t, logger)
}

func TestInitializeLoggerWithError_Development(t *testing.T) {
	t.Setenv("ENV_NAME", "development")

	logger, err := InitializeLoggerWithError()

	assert.NoError(t, err)
	assert.NotNil(t, logger)
}

func TestInitializeLoggerWithError_CustomLogLevel(t *testing.T) {
	t.Setenv("ENV_NAME", "production")
	t.Setenv("LOG_LEVEL", "warn")

	logger, err := InitializeLoggerWithError()

	assert.NoError(t, err)
	assert.NotNil(t, logger)
}

// This test must not call t.Parallel() because it mutates the global log.Writer
// via log.SetOutput(&buf) and relies on the defer to restore originalOutput.
func TestInitializeLoggerWithError_InvalidLogLevel(t *testing.T) {
	t.Setenv("ENV_NAME", "production")
	t.Setenv("LOG_LEVEL", "invalid_level")

	var buf bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&buf)

	defer log.SetOutput(originalOutput)

	logger, err := InitializeLoggerWithError()

	assert.NoError(t, err)
	assert.NotNil(t, logger)
	assert.Contains(t, buf.String(), "Invalid LOG_LEVEL")
	assert.Contains(t, buf.String(), "fallback to InfoLevel")
}
