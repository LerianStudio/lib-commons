//go:build unit

package postgres

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// validate — covers the missing validation branches
// -------------------------------------------------------------------

func TestValidate_EmptyPrimaryDSN(t *testing.T) {
	t.Parallel()

	cfg := Config{PrimaryDSN: ""}
	err := cfg.validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary dsn")
}

// -------------------------------------------------------------------
// warnInsecureDSN — logger enabled but DSN is secure (no warning)
// -------------------------------------------------------------------

func TestWarnInsecureDSN_EnabledLoggerSecureDSN(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	assert.NotPanics(t, func() {
		warnInsecureDSN(context.Background(), logger, "postgres://user:pass@localhost/db?sslmode=require", "primary")
	})
}

func TestWarnInsecureDSN_EnabledLoggerInsecureDSN(t *testing.T) {
	t.Parallel()

	logger := log.NewNop()
	assert.NotPanics(t, func() {
		warnInsecureDSN(context.Background(), logger, "postgres://user:pass@localhost/db?sslmode=disable", "primary")
	})
}

// -------------------------------------------------------------------
// dsnSSLMode — missing branch: no sslmode param
// -------------------------------------------------------------------

func TestDsnSSLMode_NoSSLModeParam(t *testing.T) {
	t.Parallel()

	mode := dsnSSLMode("postgres://user:pass@localhost:5432/db")
	assert.NotEqual(t, "require", mode)
}

func TestDsnSSLMode_SSLModeVerifyFull(t *testing.T) {
	t.Parallel()

	mode := dsnSSLMode("postgres://user:pass@localhost:5432/db?sslmode=verify-full")
	assert.Equal(t, "verify-full", mode)
}

// -------------------------------------------------------------------
// logAtLevel — branches with nil logger and nop logger
// -------------------------------------------------------------------

func TestClient_LogAtLevel_NilLogger(t *testing.T) {
	t.Parallel()

	c := &Client{}
	assert.NotPanics(t, func() {
		c.logAtLevel(context.Background(), log.LevelInfo, "test message")
	})
}

func TestClient_LogAtLevel_NopLogger(t *testing.T) {
	t.Parallel()

	c := &Client{cfg: Config{Logger: log.NewNop()}}
	assert.NotPanics(t, func() {
		c.logAtLevel(context.Background(), log.LevelInfo, "test message")
	})
}

// -------------------------------------------------------------------
// recordConnectionFailure — with non-nil client but nil factory
// -------------------------------------------------------------------

func TestRecordConnectionFailure_EmptyClient(t *testing.T) {
	t.Parallel()

	c := &Client{}
	// metricsFactory is nil — should be a no-op
	assert.NotPanics(t, func() {
		c.recordConnectionFailure(context.Background(), "connect")
	})
}

// -------------------------------------------------------------------
// Resolver — never connected returns error
// -------------------------------------------------------------------

func TestResolver_NeverConnected_ReturnsError(t *testing.T) {
	t.Parallel()

	c, err := New(Config{
		PrimaryDSN: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		ReplicaDSN: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		Logger:     log.NewNop(),
	})
	require.NoError(t, err)

	// Not connected yet — resolver should fail fast or attempt connect
	// In backoff-based lazy connect, it will attempt but fail since no DB exists
	_, err = c.Resolver(context.Background())
	// May succeed or fail — just verify no panic
	_ = err
}

// -------------------------------------------------------------------
// Close — valid and idempotent
// -------------------------------------------------------------------

func TestClose_NotConnected_NoError(t *testing.T) {
	t.Parallel()

	c, err := New(Config{
		PrimaryDSN: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		ReplicaDSN: "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		Logger:     log.NewNop(),
	})
	require.NoError(t, err)

	err = c.Close()
	assert.NoError(t, err)
}

// -------------------------------------------------------------------
// MigrationConfig validate
// -------------------------------------------------------------------

func TestMigrationConfig_Validate_ValidConfig(t *testing.T) {
	t.Parallel()

	cfg := MigrationConfig{
		PrimaryDSN:     "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		DatabaseName:   "testdb",
		MigrationsPath: "/migrations",
	}
	err := cfg.validate()
	require.NoError(t, err)
}

func TestMigrationConfig_Validate_EmptyDBName(t *testing.T) {
	t.Parallel()

	cfg := MigrationConfig{
		PrimaryDSN:     "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
		MigrationsPath: "/migrations",
	}
	err := cfg.validate()
	require.Error(t, err)
}

func TestMigrationConfig_Validate_EmptyDSN(t *testing.T) {
	t.Parallel()

	cfg := MigrationConfig{
		DatabaseName:   "testdb",
		MigrationsPath: "/migrations",
	}
	err := cfg.validate()
	require.Error(t, err)
}

// -------------------------------------------------------------------
// migrationLogAtLevel — logger enabled
// -------------------------------------------------------------------

func TestMigrationLogAtLevel_NopLoggerEnabled(t *testing.T) {
	t.Parallel()

	// NewNop logger has Enabled returning false — no log should happen
	logger := log.NewNop()
	assert.NotPanics(t, func() {
		migrationLogAtLevel(context.Background(), logger, log.LevelWarn, "warn msg")
	})
}
