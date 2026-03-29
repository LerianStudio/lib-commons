//go:build unit

// Copyright 2025 Lerian Studio.

package bootstrap

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyKeyDefs_SetsApplyBehaviors(t *testing.T) {
	t.Parallel()

	cfg := &BootstrapConfig{
		Backend:  domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{DSN: "postgres://localhost/db"},
	}

	defs := []domain.KeyDef{
		{Key: "app.log_level", ApplyBehavior: domain.ApplyLiveRead},
		{Key: "postgres.max_open_conns", ApplyBehavior: domain.ApplyLiveRead},
		{Key: "bacen_spi.timeout_sec", ApplyBehavior: domain.ApplyBundleRebuild},
	}

	cfg.ApplyKeyDefs(defs)

	require.Len(t, cfg.ApplyBehaviors, 3)
	assert.Equal(t, domain.ApplyLiveRead, cfg.ApplyBehaviors["app.log_level"])
	assert.Equal(t, domain.ApplyBundleRebuild, cfg.ApplyBehaviors["bacen_spi.timeout_sec"])

	// Postgres sub-config gets the same map.
	require.NotNil(t, cfg.Postgres.ApplyBehaviors)
	assert.Equal(t, domain.ApplyLiveRead, cfg.Postgres.ApplyBehaviors["postgres.max_open_conns"])
}

func TestApplyKeyDefs_ConfiguresSecretsWhenMasterKeySet(t *testing.T) {
	t.Setenv(EnvSecretMasterKey, "test-master-key-32-chars-long!!!")

	cfg := &BootstrapConfig{
		Backend:  domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{DSN: "postgres://localhost/db"},
	}

	defs := []domain.KeyDef{
		{Key: "auth.client_secret", ApplyBehavior: domain.ApplyBootstrapOnly, Secret: true},
		{Key: "app.log_level", ApplyBehavior: domain.ApplyLiveRead},
	}

	cfg.ApplyKeyDefs(defs)

	require.NotNil(t, cfg.Secrets)
	assert.Equal(t, "test-master-key-32-chars-long!!!", cfg.Secrets.MasterKey)
	assert.Equal(t, []string{"auth.client_secret"}, cfg.Secrets.SecretKeys)
}

func TestApplyKeyDefs_NoSecretsWithoutMasterKeyOrSecretKeys(t *testing.T) {
	t.Parallel()

	cfg := &BootstrapConfig{
		Backend:  domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{DSN: "postgres://localhost/db"},
	}

	defs := []domain.KeyDef{
		{Key: "app.log_level", ApplyBehavior: domain.ApplyLiveRead},
	}

	cfg.ApplyKeyDefs(defs)

	assert.Nil(t, cfg.Secrets)
}

func TestApplyKeyDefs_NilConfig(t *testing.T) {
	t.Parallel()

	// Must not panic — verified explicitly.
	var cfg *BootstrapConfig
	assert.NotPanics(t, func() {
		cfg.ApplyKeyDefs([]domain.KeyDef{{Key: "k", ApplyBehavior: domain.ApplyLiveRead}})
	})
}

func TestApplyKeyDefs_EmptySlice(t *testing.T) {
	t.Parallel()

	cfg := &BootstrapConfig{
		Backend:  domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{DSN: "postgres://localhost/db"},
	}

	cfg.ApplyKeyDefs([]domain.KeyDef{})

	// ApplyBehaviors should be an initialized (non-nil) empty map, not nil.
	require.NotNil(t, cfg.ApplyBehaviors)
	assert.Empty(t, cfg.ApplyBehaviors)
	// No secret keys → Secrets must remain nil.
	assert.Nil(t, cfg.Secrets)
}

func TestApplyKeyDefs_SecretKeysButNoMasterKey(t *testing.T) {
	// Cannot use t.Parallel() because t.Setenv requires a sequential test.

	// Ensure the env var is NOT set so we exercise the "no master key" branch.
	t.Setenv(EnvSecretMasterKey, "")

	cfg := &BootstrapConfig{
		Backend:  domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{DSN: "postgres://localhost/db"},
	}

	defs := []domain.KeyDef{
		{Key: "auth.client_secret", ApplyBehavior: domain.ApplyBootstrapOnly, Secret: true},
	}

	cfg.ApplyKeyDefs(defs)

	// Even though there are secret keys, no master key → Secrets stays nil.
	assert.Nil(t, cfg.Secrets)
	// But ApplyBehaviors is still populated.
	require.Len(t, cfg.ApplyBehaviors, 1)
	assert.Equal(t, domain.ApplyBootstrapOnly, cfg.ApplyBehaviors["auth.client_secret"])
}

func TestApplyKeyDefs_MapIndependence(t *testing.T) {
	t.Parallel()

	cfg := &BootstrapConfig{
		Backend:  domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{DSN: "postgres://localhost/db"},
	}

	defs := []domain.KeyDef{
		{Key: "app.log_level", ApplyBehavior: domain.ApplyLiveRead},
		{Key: "postgres.max_open_conns", ApplyBehavior: domain.ApplyLiveRead},
	}

	cfg.ApplyKeyDefs(defs)

	// Mutate the Postgres sub-config map…
	cfg.Postgres.ApplyBehaviors["injected.key"] = domain.ApplyBundleRebuild

	// …and verify the top-level map is NOT affected.
	_, exists := cfg.ApplyBehaviors["injected.key"]
	assert.False(t, exists, "top-level ApplyBehaviors must be independent of Postgres sub-config map")

	// Also verify the reverse: mutating the top-level map does not bleed into Postgres.
	cfg.ApplyBehaviors["another.key"] = domain.ApplyBootstrapOnly
	_, existsInPg := cfg.Postgres.ApplyBehaviors["another.key"]
	assert.False(t, existsInPg, "Postgres ApplyBehaviors must be independent of top-level map")
}

func TestSecretStoreConfig_String(t *testing.T) {
	t.Parallel()

	t.Run("non-nil config redacts master key", func(t *testing.T) {
		t.Parallel()

		s := &SecretStoreConfig{MasterKey: "super-secret-key-32-bytes-here!!", SecretKeys: []string{"auth.client_secret"}}
		assert.Equal(t, "SecretStoreConfig{MasterKey:REDACTED}", s.String())
	})

	t.Run("nil receiver returns <nil>", func(t *testing.T) {
		t.Parallel()

		var s *SecretStoreConfig
		assert.Equal(t, "<nil>", s.String())
	})

	t.Run("GoString delegates to String", func(t *testing.T) {
		t.Parallel()

		s := &SecretStoreConfig{MasterKey: "should-not-appear"}
		assert.Equal(t, s.String(), s.GoString())
	})

	t.Run("nil GoString delegates to String", func(t *testing.T) {
		t.Parallel()

		var s *SecretStoreConfig
		assert.Equal(t, s.String(), s.GoString())
	})
}

func TestValidate_WeakMasterKey(t *testing.T) {
	t.Parallel()

	cfg := &BootstrapConfig{
		Backend: domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{
			DSN: "postgres://localhost/db",
		},
		Secrets: &SecretStoreConfig{
			MasterKey:  "tooshort10", // 10 bytes — well below the 32-byte minimum
			SecretKeys: []string{"auth.client_secret"},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "32")
}

func TestValidate_ValidMasterKey_Raw32Bytes(t *testing.T) {
	t.Parallel()

	// Exactly 32 ASCII bytes — the minimum accepted raw form.
	masterKey := "12345678901234567890123456789012"
	require.Len(t, []byte(masterKey), 32, "test precondition: key must be exactly 32 bytes")

	cfg := &BootstrapConfig{
		Backend: domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{
			DSN: "postgres://localhost/db",
		},
		Secrets: &SecretStoreConfig{
			MasterKey:  masterKey,
			SecretKeys: []string{"auth.client_secret"},
		},
	}

	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestApplyKeyDefs_MongoDBSubConfig(t *testing.T) {
	t.Parallel()

	cfg := &BootstrapConfig{
		Backend: domain.BackendMongoDB,
		MongoDB: &MongoBootstrapConfig{URI: "mongodb://localhost"},
	}

	defs := []domain.KeyDef{
		{Key: "app.log_level", ApplyBehavior: domain.ApplyLiveRead},
	}

	cfg.ApplyKeyDefs(defs)

	require.NotNil(t, cfg.MongoDB.ApplyBehaviors)
	assert.Equal(t, domain.ApplyLiveRead, cfg.MongoDB.ApplyBehaviors["app.log_level"])
}
