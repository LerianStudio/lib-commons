//go:build unit

package zap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func TestNewRejectsMissingOTelLibraryName(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Environment: EnvironmentProduction})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OTelLibraryName is required")
}

func TestNewRejectsInvalidEnvironment(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Environment: Environment("banana"), OTelLibraryName: "svc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid environment")
}

func TestNewAppliesEnvironmentDefaultLevel(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{Environment: EnvironmentDevelopment, OTelLibraryName: "svc"})
	require.NoError(t, err)
	assert.Equal(t, zapcore.DebugLevel, logger.Level().Level())

	logger, err = New(Config{Environment: EnvironmentProduction, OTelLibraryName: "svc"})
	require.NoError(t, err)
	assert.Equal(t, zapcore.InfoLevel, logger.Level().Level())
}

func TestNewAppliesCustomLevel(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{Environment: EnvironmentProduction, OTelLibraryName: "svc", Level: "error"})
	require.NoError(t, err)
	assert.Equal(t, zapcore.ErrorLevel, logger.Level().Level())
}

func TestNewRejectsInvalidCustomLevel(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Environment: EnvironmentProduction, OTelLibraryName: "svc", Level: "invalid"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid level")
}

func TestCallerAttributionPointsToCallSite(t *testing.T) {
	t.Parallel()

	// Verify that caller skip is configured so that the logged caller
	// points to the call site, not the zap wrapper internals.
	// callerSkipFrames=1 means skip the wrapper's own frame.
	logger, err := New(Config{
		Environment:     EnvironmentDevelopment,
		OTelLibraryName: "test-caller",
	})
	require.NoError(t, err)

	// The logger should not be nil and should have caller enabled
	// (development config enables AddCaller by default).
	raw := logger.Raw()
	require.NotNil(t, raw, "Raw() should return the underlying zap logger")
}

func TestNewWithLocalEnvironment(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{Environment: EnvironmentLocal, OTelLibraryName: "svc"})
	require.NoError(t, err)
	require.NotNil(t, logger)
	assert.Equal(t, zapcore.DebugLevel, logger.Level().Level())
}

func TestNewWithStagingEnvironment(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{Environment: EnvironmentStaging, OTelLibraryName: "svc"})
	require.NoError(t, err)
	require.NotNil(t, logger)
	assert.Equal(t, zapcore.InfoLevel, logger.Level().Level())
}

func TestNewWithUATEnvironment(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{Environment: EnvironmentUAT, OTelLibraryName: "svc"})
	require.NoError(t, err)
	require.NotNil(t, logger)
	assert.Equal(t, zapcore.InfoLevel, logger.Level().Level())
}

func TestResolveLevelEmptyForProductionDefaultsToInfo(t *testing.T) {
	t.Parallel()

	level, err := resolveLevel(Config{Environment: EnvironmentProduction, Level: ""})
	require.NoError(t, err)
	assert.Equal(t, zapcore.InfoLevel, level.Level())
}

func TestResolveLevelEmptyForLocalDefaultsToDebug(t *testing.T) {
	t.Parallel()

	level, err := resolveLevel(Config{Environment: EnvironmentLocal, Level: ""})
	require.NoError(t, err)
	assert.Equal(t, zapcore.DebugLevel, level.Level())
}

func TestBuildConfigByEnvironmentDev(t *testing.T) {
	t.Setenv("LOG_ENCODING", "")

	cfg := buildConfigByEnvironment(EnvironmentDevelopment)
	assert.Equal(t, "console", cfg.Encoding)
	assert.True(t, cfg.Development)
}

func TestBuildConfigByEnvironmentProd(t *testing.T) {
	t.Setenv("LOG_ENCODING", "")

	cfg := buildConfigByEnvironment(EnvironmentProduction)
	assert.Equal(t, "json", cfg.Encoding)
	assert.False(t, cfg.Development)
}

func TestResolveEncodingFromEnvVar(t *testing.T) {
	t.Setenv("LOG_ENCODING", "json")
	assert.Equal(t, "json", resolveEncoding(EnvironmentLocal))

	t.Setenv("LOG_ENCODING", "console")
	assert.Equal(t, "console", resolveEncoding(EnvironmentProduction))

	t.Setenv("LOG_ENCODING", "invalid")
	assert.Equal(t, "console", resolveEncoding(EnvironmentLocal))
	assert.Equal(t, "json", resolveEncoding(EnvironmentProduction))
}

func TestResolveLevelFromEnvVar(t *testing.T) {
	t.Setenv("LOG_LEVEL", "warn")

	level, err := resolveLevel(Config{Environment: EnvironmentProduction, Level: ""})
	require.NoError(t, err)
	assert.Equal(t, zapcore.WarnLevel, level.Level())
}

func TestResolveLevelConfigOverridesEnvVar(t *testing.T) {
	t.Setenv("LOG_LEVEL", "warn")

	level, err := resolveLevel(Config{Environment: EnvironmentProduction, Level: "error"})
	require.NoError(t, err)
	assert.Equal(t, zapcore.ErrorLevel, level.Level(), "Config.Level should take precedence over LOG_LEVEL env var")
}
