package tenantmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPostgresManager(t *testing.T) {
	t.Run("creates manager with client and service", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		manager := NewPostgresManager(client, "ledger")

		assert.NotNil(t, manager)
		assert.Equal(t, "ledger", manager.service)
		assert.NotNil(t, manager.connections)
	})
}

func TestPostgresManager_GetConnection_NoTenantID(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewPostgresManager(client, "ledger")

	_, err := manager.GetConnection(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestPostgresManager_Close(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewPostgresManager(client, "ledger")

	err := manager.Close()

	assert.NoError(t, err)
	assert.True(t, manager.closed)
}

func TestPostgresManager_GetConnection_ManagerClosed(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewPostgresManager(client, "ledger")
	manager.Close()

	_, err := manager.GetConnection(context.Background(), "tenant-123")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManagerClosed)
}

func TestIsolationModeConstants(t *testing.T) {
	t.Run("isolation mode constants have expected values", func(t *testing.T) {
		assert.Equal(t, "isolated", IsolationModeIsolated)
		assert.Equal(t, "schema", IsolationModeSchema)
	})
}

func TestBuildConnectionString(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *PostgreSQLConfig
		expected string
	}{
		{
			name: "builds connection string without schema",
			cfg: &PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "disable",
			},
			expected: "host=localhost port=5432 user=user password=pass dbname=testdb sslmode=disable",
		},
		{
			name: "builds connection string with schema in options",
			cfg: &PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "disable",
				Schema:   "tenant_abc",
			},
			expected: "host=localhost port=5432 user=user password=pass dbname=testdb sslmode=disable options=-csearch_path=tenant_abc",
		},
		{
			name: "defaults sslmode to disable when empty",
			cfg: &PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
			},
			expected: "host=localhost port=5432 user=user password=pass dbname=testdb sslmode=disable",
		},
		{
			name: "uses provided sslmode",
			cfg: &PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "testdb",
				SSLMode:  "require",
			},
			expected: "host=localhost port=5432 user=user password=pass dbname=testdb sslmode=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildConnectionString(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildConnectionStrings_PrimaryAndReplica(t *testing.T) {
	t.Run("builds separate connection strings for primary and replica", func(t *testing.T) {
		primaryConfig := &PostgreSQLConfig{
			Host:     "primary-host",
			Port:     5432,
			Username: "user",
			Password: "pass",
			Database: "testdb",
			SSLMode:  "disable",
		}
		replicaConfig := &PostgreSQLConfig{
			Host:     "replica-host",
			Port:     5433,
			Username: "user",
			Password: "pass",
			Database: "testdb",
			SSLMode:  "disable",
		}

		primaryConnStr := buildConnectionString(primaryConfig)
		replicaConnStr := buildConnectionString(replicaConfig)

		assert.Contains(t, primaryConnStr, "host=primary-host")
		assert.Contains(t, primaryConnStr, "port=5432")
		assert.Contains(t, replicaConnStr, "host=replica-host")
		assert.Contains(t, replicaConnStr, "port=5433")
		assert.NotEqual(t, primaryConnStr, replicaConnStr)
	})

	t.Run("fallback to primary when replica not configured", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							PostgreSQL: &PostgreSQLConfig{
								Host:     "primary-host",
								Port:     5432,
								Username: "user",
								Password: "pass",
								Database: "testdb",
							},
							// No PostgreSQLReplica configured
						},
					},
				},
			},
		}

		pgConfig := config.GetPostgreSQLConfig("ledger", "onboarding")
		pgReplicaConfig := config.GetPostgreSQLReplicaConfig("ledger", "onboarding")

		assert.NotNil(t, pgConfig)
		assert.Nil(t, pgReplicaConfig)

		// When replica is nil, system should use primary connection string
		primaryConnStr := buildConnectionString(pgConfig)

		replicaConnStr := primaryConnStr
		if pgReplicaConfig != nil {
			replicaConnStr = buildConnectionString(pgReplicaConfig)
		}

		assert.Equal(t, primaryConnStr, replicaConnStr)
	})

	t.Run("uses replica config when available", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							PostgreSQL: &PostgreSQLConfig{
								Host:     "primary-host",
								Port:     5432,
								Username: "user",
								Password: "pass",
								Database: "testdb",
							},
							PostgreSQLReplica: &PostgreSQLConfig{
								Host:     "replica-host",
								Port:     5433,
								Username: "user",
								Password: "pass",
								Database: "testdb",
							},
						},
					},
				},
			},
		}

		pgConfig := config.GetPostgreSQLConfig("ledger", "onboarding")
		pgReplicaConfig := config.GetPostgreSQLReplicaConfig("ledger", "onboarding")

		assert.NotNil(t, pgConfig)
		assert.NotNil(t, pgReplicaConfig)

		primaryConnStr := buildConnectionString(pgConfig)

		replicaConnStr := primaryConnStr
		if pgReplicaConfig != nil {
			replicaConnStr = buildConnectionString(pgReplicaConfig)
		}

		assert.NotEqual(t, primaryConnStr, replicaConnStr)
		assert.Contains(t, primaryConnStr, "host=primary-host")
		assert.Contains(t, replicaConnStr, "host=replica-host")
	})

	t.Run("handles replica with different database name", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							PostgreSQL: &PostgreSQLConfig{
								Host:     "primary-host",
								Port:     5432,
								Username: "user",
								Password: "pass",
								Database: "primary_db",
							},
							PostgreSQLReplica: &PostgreSQLConfig{
								Host:     "replica-host",
								Port:     5433,
								Username: "user",
								Password: "pass",
								Database: "replica_db",
							},
						},
					},
				},
			},
		}

		pgConfig := config.GetPostgreSQLConfig("ledger", "onboarding")
		pgReplicaConfig := config.GetPostgreSQLReplicaConfig("ledger", "onboarding")

		assert.Equal(t, "primary_db", pgConfig.Database)
		assert.Equal(t, "replica_db", pgReplicaConfig.Database)
	})
}
