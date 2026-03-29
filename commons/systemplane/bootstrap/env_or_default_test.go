//go:build unit

// Copyright 2025 Lerian Studio.

package bootstrap

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromEnvOrDefault_FallbackDSN(t *testing.T) {
	// No SYSTEMPLANE_BACKEND set — should use fallback DSN.
	cfg, err := LoadFromEnvOrDefault("postgres://user:pass@localhost:5432/mydb")
	require.NoError(t, err)
	assert.Equal(t, domain.BackendPostgres, cfg.Backend)
	assert.NotNil(t, cfg.Postgres)
	assert.Equal(t, "postgres://user:pass@localhost:5432/mydb", cfg.Postgres.DSN)
	// Defaults applied.
	assert.Equal(t, DefaultPostgresSchema, cfg.Postgres.Schema)
	assert.Equal(t, DefaultPostgresEntriesTable, cfg.Postgres.EntriesTable)
}

func TestLoadFromEnvOrDefault_DelegatesWhenEnvSet(t *testing.T) {
	t.Setenv(EnvBackend, "postgres")
	t.Setenv(EnvPostgresDSN, "postgres://env@host:5432/envdb")

	cfg, err := LoadFromEnvOrDefault("postgres://fallback@host:5432/fallbackdb")
	require.NoError(t, err)
	// Should use the env DSN, not the fallback.
	assert.Equal(t, "postgres://env@host:5432/envdb", cfg.Postgres.DSN)
}

func TestLoadFromEnvOrDefault_EmptyFallbackDSN(t *testing.T) {
	_, err := LoadFromEnvOrDefault("")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingPostgresDSN)
}

func TestLoadFromEnvOrDefault_WhitespaceOnlyFallbackDSN(t *testing.T) {
	_, err := LoadFromEnvOrDefault("   ")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingPostgresDSN)
}

func TestLoadFromEnvOrDefault_TrimsFallbackDSN(t *testing.T) {
	cfg, err := LoadFromEnvOrDefault("  postgres://user:pass@localhost:5432/db  ")
	require.NoError(t, err)
	assert.Equal(t, "postgres://user:pass@localhost:5432/db", cfg.Postgres.DSN)
}
