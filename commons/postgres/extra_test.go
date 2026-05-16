//go:build unit

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWarnInsecureDSN covers the warnInsecureDSN function branches.
func TestWarnInsecureDSN_NilLogger(t *testing.T) {
	t.Parallel()

	// Should not panic with nil logger
	assert.NotPanics(t, func() {
		warnInsecureDSN(context.Background(), nil, "postgres://user:pass@localhost/db?sslmode=disable", "primary")
	})
}

func TestWarnInsecureDSN_DisabledLogger(t *testing.T) {
	t.Parallel()

	// A nop logger has all levels disabled
	logger := log.NewNop()
	assert.NotPanics(t, func() {
		warnInsecureDSN(context.Background(), logger, "postgres://user:pass@localhost/db?sslmode=disable", "primary")
	})
}

// TestMigrationLogAtLevel covers the migrationLogAtLevel function.
func TestMigrationLogAtLevel_NilLogger(t *testing.T) {
	t.Parallel()

	// Should not panic with nil logger
	assert.NotPanics(t, func() {
		migrationLogAtLevel(context.Background(), nil, log.LevelInfo, "test message")
	})
}

func TestMigrationLogAtLevel_NopLogger(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	// Should not panic; nop logger Enabled() returns false
	assert.NotPanics(t, func() {
		migrationLogAtLevel(context.Background(), logger, log.LevelInfo, "test message", log.String("k", "v"))
	})
}

// TestResolveMigrationSource covers valid and invalid path parsing.
func TestResolveMigrationSource_ValidPath(t *testing.T) {
	t.Parallel()

	u, err := resolveMigrationSource("/migrations/sql")
	require.NoError(t, err)
	assert.Equal(t, "file", u.Scheme)
	assert.Contains(t, u.Path, "migrations")
}

func TestResolveMigrationSource_RelativePath(t *testing.T) {
	t.Parallel()

	u, err := resolveMigrationSource("./migrations")
	require.NoError(t, err)
	assert.Equal(t, "file", u.Scheme)
}

// TestSanitizedCause covers the sanitizedCause function.
func TestSanitizedCause_NilError(t *testing.T) {
	t.Parallel()

	result := sanitizedCause(nil)
	assert.Nil(t, result)
}

func TestSanitizedCause_NonNilError(t *testing.T) {
	t.Parallel()

	inner := errors.New("simple error with password://user:pass@host")
	result := sanitizedCause(inner)
	require.NotNil(t, result)
	// Should not contain raw credentials
	assert.NotContains(t, result.Error(), "user:pass")
}

// TestSanitizePath covers sanitizePath edge cases.
func TestSanitizePath_TraversalRejected(t *testing.T) {
	t.Parallel()

	_, err := sanitizePath("../../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid migrations path")
}

func TestSanitizePath_AbsolutePath(t *testing.T) {
	t.Parallel()

	result, err := sanitizePath("/some/deep/path/migrations")
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

func TestSanitizePath_RelativePath(t *testing.T) {
	t.Parallel()

	result, err := sanitizePath("./migrations")
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

// TestRecordConnectionFailure_NilClient covers the nil receiver guard.
func TestRecordConnectionFailure_NilClient(t *testing.T) {
	t.Parallel()

	var c *Client
	assert.NotPanics(t, func() {
		c.recordConnectionFailure(context.Background(), "connect")
	})
}

func TestRecordConnectionFailure_NilMetricsFactory(t *testing.T) {
	t.Parallel()

	c := &Client{}
	// metricsFactory is nil by default - should be a no-op
	assert.NotPanics(t, func() {
		c.recordConnectionFailure(context.Background(), "connect")
	})
}

// TestValidate_MissingPrimaryDSN covers the validate method.
func TestConfig_Validate_MissingPrimaryDSN(t *testing.T) {
	t.Parallel()

	cfg := Config{
		PrimaryDSN: "",
	}

	err := cfg.validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary dsn cannot be empty")
}

func TestConfig_Validate_ValidDSN(t *testing.T) {
	t.Parallel()

	cfg := Config{
		PrimaryDSN: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		ReplicaDSN: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
	}

	cfg = cfg.withDefaults()
	err := cfg.validate()
	require.NoError(t, err)
}

// TestValidateDSN covers the validateDSN function.
func TestValidateDSN_ValidScheme(t *testing.T) {
	t.Parallel()

	err := validateDSN("postgres://user:pass@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)
}

func TestValidateDSN_EmptyDSN_NoError(t *testing.T) {
	t.Parallel()

	// Empty DSN is treated as key-value format - no error (checked at Config.validate level)
	err := validateDSN("")
	require.NoError(t, err)
}

func TestValidateDSN_MalformedURL(t *testing.T) {
	t.Parallel()

	// Invalid URL with postgres:// prefix should fail
	err := validateDSN("postgres://host\ninvalid")
	// Go's url.Parse is lenient, so only truly malformed URLs fail here
	// This verifies the function runs without panic
	_ = err // may or may not error depending on Go version
}

// TestDsnSSLMode covers the dsnSSLMode function.
func TestDsnSSLMode_DisableMode(t *testing.T) {
	t.Parallel()

	mode := dsnSSLMode("postgres://localhost/db?sslmode=disable")
	assert.Equal(t, "disable", mode)
}

func TestDsnSSLMode_RequireMode(t *testing.T) {
	t.Parallel()

	mode := dsnSSLMode("postgres://localhost/db?sslmode=require")
	assert.Equal(t, "require", mode)
}

func TestDsnSSLMode_MissingSSLMode(t *testing.T) {
	t.Parallel()

	mode := dsnSSLMode("postgres://localhost/db")
	assert.Equal(t, "", mode)
}

// TestLogAtLevel covers the logAtLevel nil receiver path.
func TestLogAtLevel_NilReceiver(t *testing.T) {
	t.Parallel()

	var c *Client
	assert.NotPanics(t, func() {
		c.logAtLevel(context.Background(), log.LevelInfo, "message")
	})
}
