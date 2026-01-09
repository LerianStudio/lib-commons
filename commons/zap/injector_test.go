package zap

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInitializeLogger(t *testing.T) {
	os.Setenv("ENV_NAME", "production")
	defer os.Unsetenv("ENV_NAME")
	logger := InitializeLogger()
	assert.NotNil(t, logger)
}

func TestInitializeLoggerWithError_Success(t *testing.T) {
	os.Setenv("ENV_NAME", "production")
	defer os.Unsetenv("ENV_NAME")

	logger, err := InitializeLoggerWithError()

	assert.NoError(t, err)
	assert.NotNil(t, logger)
}

func TestInitializeLoggerWithError_Development(t *testing.T) {
	os.Setenv("ENV_NAME", "development")
	defer os.Unsetenv("ENV_NAME")

	logger, err := InitializeLoggerWithError()

	assert.NoError(t, err)
	assert.NotNil(t, logger)
}

func TestInitializeLoggerWithError_CustomLogLevel(t *testing.T) {
	os.Setenv("ENV_NAME", "production")
	os.Setenv("LOG_LEVEL", "warn")
	defer func() {
		os.Unsetenv("ENV_NAME")
		os.Unsetenv("LOG_LEVEL")
	}()

	logger, err := InitializeLoggerWithError()

	assert.NoError(t, err)
	assert.NotNil(t, logger)
}

func TestInitializeLoggerWithError_InvalidLogLevel(t *testing.T) {
	os.Setenv("ENV_NAME", "production")
	os.Setenv("LOG_LEVEL", "invalid_level")
	defer func() {
		os.Unsetenv("ENV_NAME")
		os.Unsetenv("LOG_LEVEL")
	}()

	logger, err := InitializeLoggerWithError()

	assert.NoError(t, err)
	assert.NotNil(t, logger)
}
