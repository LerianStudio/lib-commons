//go:build unit

package core

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// GetPostgreSQLConfig — module-empty path (returns first found)
// -------------------------------------------------------------------

func TestGetPostgreSQLConfig_NoModule_ReturnsFirst(t *testing.T) {
	t.Parallel()

	tc := &TenantConfig{
		Databases: map[string]DatabaseConfig{
			"zz-module": {PostgreSQL: &PostgreSQLConfig{Host: "zz-host"}},
			"aa-module": {PostgreSQL: &PostgreSQLConfig{Host: "aa-host"}},
		},
	}

	// With no module, should return first sorted key (aa-module)
	result := tc.GetPostgreSQLConfig("ledger", "")
	require.NotNil(t, result)
	assert.Equal(t, "aa-host", result.Host, "should return first sorted key")
}

func TestGetPostgreSQLConfig_ModuleNotFound(t *testing.T) {
	t.Parallel()

	tc := &TenantConfig{
		Databases: map[string]DatabaseConfig{
			"onboarding": {PostgreSQL: &PostgreSQLConfig{Host: "pg-host"}},
		},
	}

	result := tc.GetPostgreSQLConfig("ledger", "non-existent-module")
	assert.Nil(t, result)
}

func TestGetPostgreSQLConfig_NilDatabases_WithModule(t *testing.T) {
	t.Parallel()

	tc := &TenantConfig{}
	result := tc.GetPostgreSQLConfig("ledger", "module-x")
	assert.Nil(t, result)
}

// -------------------------------------------------------------------
// GetPostgreSQLReplicaConfig — module-empty path
// -------------------------------------------------------------------

func TestGetPostgreSQLReplicaConfig_NoModule_ReturnsFirst(t *testing.T) {
	t.Parallel()

	tc := &TenantConfig{
		Databases: map[string]DatabaseConfig{
			"bb-module": {PostgreSQLReplica: &PostgreSQLConfig{Host: "bb-replica"}},
			"aa-module": {PostgreSQL: &PostgreSQLConfig{Host: "aa-primary"}},
		},
	}

	// aa-module has no replica; bb-module has replica
	result := tc.GetPostgreSQLReplicaConfig("ledger", "")
	require.NotNil(t, result)
	assert.Equal(t, "bb-replica", result.Host)
}

func TestGetPostgreSQLReplicaConfig_ModuleNotFound(t *testing.T) {
	t.Parallel()

	tc := &TenantConfig{
		Databases: map[string]DatabaseConfig{
			"onboarding": {PostgreSQLReplica: &PostgreSQLConfig{Host: "replica"}},
		},
	}

	result := tc.GetPostgreSQLReplicaConfig("ledger", "unknown")
	assert.Nil(t, result)
}

// -------------------------------------------------------------------
// GetMongoDBConfig — module-empty path
// -------------------------------------------------------------------

func TestGetMongoDBConfig_NoModule_ReturnsFirst(t *testing.T) {
	t.Parallel()

	tc := &TenantConfig{
		Databases: map[string]DatabaseConfig{
			"zz-module": {MongoDB: &MongoDBConfig{Host: "zz-mongo"}},
			"aa-module": {MongoDB: &MongoDBConfig{Host: "aa-mongo"}},
		},
	}

	result := tc.GetMongoDBConfig("ledger", "")
	require.NotNil(t, result)
	assert.Equal(t, "aa-mongo", result.Host)
}

func TestGetMongoDBConfig_ModuleNotFound(t *testing.T) {
	t.Parallel()

	tc := &TenantConfig{
		Databases: map[string]DatabaseConfig{
			"data": {MongoDB: &MongoDBConfig{Host: "mongo"}},
		},
	}

	result := tc.GetMongoDBConfig("ledger", "non-existent")
	assert.Nil(t, result)
}

// -------------------------------------------------------------------
// IsTenantNotProvisionedError — all branches
// -------------------------------------------------------------------

func TestIsTenantNotProvisionedError_NilError(t *testing.T) {
	t.Parallel()

	assert.False(t, IsTenantNotProvisionedError(nil))
}

func TestIsTenantNotProvisionedError_SentinelError(t *testing.T) {
	t.Parallel()

	assert.True(t, IsTenantNotProvisionedError(ErrTenantNotProvisioned))
}

func TestIsTenantNotProvisionedError_WrappedSentinel(t *testing.T) {
	t.Parallel()

	wrapped := errors.Join(errors.New("outer"), ErrTenantNotProvisioned)
	assert.True(t, IsTenantNotProvisionedError(wrapped))
}

func TestIsTenantNotProvisionedError_Postgres42P01(t *testing.T) {
	t.Parallel()

	err := errors.New("error: 42P01 undefined table accounts")
	assert.True(t, IsTenantNotProvisionedError(err))
}

func TestIsTenantNotProvisionedError_RelationDoesNotExist(t *testing.T) {
	t.Parallel()

	err := errors.New("ERROR: relation \"accounts\" does not exist")
	assert.True(t, IsTenantNotProvisionedError(err))
}

func TestIsTenantNotProvisionedError_UnrelatedError(t *testing.T) {
	t.Parallel()

	err := errors.New("connection refused")
	assert.False(t, IsTenantNotProvisionedError(err))
}

// -------------------------------------------------------------------
// GetPostgreSQLConfig — nil receiver
// -------------------------------------------------------------------

func TestGetPostgreSQLConfig_NilReceiver(t *testing.T) {
	t.Parallel()

	var tc *TenantConfig
	result := tc.GetPostgreSQLConfig("svc", "mod")
	assert.Nil(t, result)
}

// -------------------------------------------------------------------
// GetPostgreSQLReplicaConfig — nil receiver
// -------------------------------------------------------------------

func TestGetPostgreSQLReplicaConfig_NilReceiver(t *testing.T) {
	t.Parallel()

	var tc *TenantConfig
	result := tc.GetPostgreSQLReplicaConfig("svc", "mod")
	assert.Nil(t, result)
}

// -------------------------------------------------------------------
// GetMongoDBConfig — nil receiver
// -------------------------------------------------------------------

func TestGetMongoDBConfig_NilReceiver(t *testing.T) {
	t.Parallel()

	var tc *TenantConfig
	result := tc.GetMongoDBConfig("svc", "mod")
	assert.Nil(t, result)
}
