package zap

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitializeLogger(t *testing.T) {
	os.Setenv("ENV_NAME", "production")
	defer os.Unsetenv("ENV_NAME")

	logger, err := InitializeLogger()
	require.NoError(t, err)
	assert.NotNil(t, logger)
}

func TestInitializeLogger_Development(t *testing.T) {
	os.Setenv("ENV_NAME", "development")
	defer os.Unsetenv("ENV_NAME")

	logger, err := InitializeLogger()
	require.NoError(t, err)
	assert.NotNil(t, logger)
}

func TestInitializeLogger_ProductionCaseInsensitive(t *testing.T) {
	testCases := []string{"Production", "PRODUCTION", "prod", "Prod"}

	for _, tc := range testCases {
		t.Run(tc, func(t *testing.T) {
			os.Setenv("ENV_NAME", tc)
			defer os.Unsetenv("ENV_NAME")

			logger, err := InitializeLogger()
			require.NoError(t, err)
			assert.NotNil(t, logger)
		})
	}
}

func TestMustInitializeLogger(t *testing.T) {
	os.Setenv("ENV_NAME", "production")
	defer os.Unsetenv("ENV_NAME")

	assert.NotPanics(t, func() {
		logger := MustInitializeLogger()
		assert.NotNil(t, logger)
	})
}
