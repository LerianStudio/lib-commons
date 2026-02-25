package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTenantConfigFixture returns a fully populated TenantConfig with PostgreSQL,
// PostgreSQL replica, and MongoDB configurations for two modules (onboarding
// and transaction). Callers can override or nil-out fields for edge case tests.
func newTenantConfigFixture() *TenantConfig {
	return &TenantConfig{
		ID:            "tenant-fixture",
		TenantSlug:    "fixture-tenant",
		Service:       "ledger",
		Status:        "active",
		IsolationMode: "database",
		Databases: map[string]DatabaseConfig{
			"onboarding": {
				PostgreSQL: &PostgreSQLConfig{
					Host: "onboarding-db.example.com",
					Port: 5432,
				},
				PostgreSQLReplica: &PostgreSQLConfig{
					Host: "onboarding-replica.example.com",
					Port: 5433,
				},
				MongoDB: &MongoDBConfig{
					Host:     "onboarding-mongo.example.com",
					Port:     27017,
					Database: "onboarding_db",
				},
			},
			"transaction": {
				PostgreSQL: &PostgreSQLConfig{
					Host: "transaction-db.example.com",
					Port: 5432,
				},
				PostgreSQLReplica: &PostgreSQLConfig{
					Host: "transaction-replica.example.com",
					Port: 5433,
				},
				MongoDB: &MongoDBConfig{
					Host:     "transaction-mongo.example.com",
					Port:     27017,
					Database: "transaction_db",
				},
			},
		},
	}
}

func TestTenantConfig_GetPostgreSQLConfig(t *testing.T) {
	tests := []struct {
		name         string
		config       *TenantConfig
		service      string
		module       string
		expectNil    bool
		expectedHost string
	}{
		{
			name:         "returns config for onboarding module",
			config:       newTenantConfigFixture(),
			service:      "ledger",
			module:       "onboarding",
			expectedHost: "onboarding-db.example.com",
		},
		{
			name:         "returns config for transaction module",
			config:       newTenantConfigFixture(),
			service:      "ledger",
			module:       "transaction",
			expectedHost: "transaction-db.example.com",
		},
		{
			name:      "returns nil for unknown module",
			config:    newTenantConfigFixture(),
			service:   "ledger",
			module:    "unknown",
			expectNil: true,
		},
		{
			name:         "returns first config when module is empty",
			config:       newTenantConfigFixture(),
			service:      "ledger",
			module:       "",
			expectedHost: "", // non-nil but host depends on map iteration order
		},
		{
			name:      "returns nil when databases is nil",
			config:    &TenantConfig{},
			service:   "ledger",
			module:    "onboarding",
			expectNil: true,
		},
		{
			name:         "service parameter is ignored in flat format",
			config:       newTenantConfigFixture(),
			service:      "audit",
			module:       "onboarding",
			expectedHost: "onboarding-db.example.com",
		},
		{
			name:         "empty service still resolves module",
			config:       newTenantConfigFixture(),
			service:      "",
			module:       "onboarding",
			expectedHost: "onboarding-db.example.com",
		},
		{
			name: "returns nil when module exists but has no PostgreSQL config",
			config: &TenantConfig{
				Databases: map[string]DatabaseConfig{
					"onboarding": {
						MongoDB: &MongoDBConfig{Host: "mongo.example.com"},
					},
				},
			},
			service:   "ledger",
			module:    "onboarding",
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetPostgreSQLConfig(tt.service, tt.module)

			if tt.expectNil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			if tt.expectedHost != "" {
				assert.Equal(t, tt.expectedHost, result.Host)
			}
		})
	}
}

func TestTenantConfig_GetPostgreSQLReplicaConfig(t *testing.T) {
	tests := []struct {
		name         string
		config       *TenantConfig
		service      string
		module       string
		expectNil    bool
		expectedHost string
		expectedPort int
	}{
		{
			name:         "returns replica config for onboarding module",
			config:       newTenantConfigFixture(),
			service:      "ledger",
			module:       "onboarding",
			expectedHost: "onboarding-replica.example.com",
			expectedPort: 5433,
		},
		{
			name:         "returns replica config for transaction module",
			config:       newTenantConfigFixture(),
			service:      "ledger",
			module:       "transaction",
			expectedHost: "transaction-replica.example.com",
			expectedPort: 5433,
		},
		{
			name: "returns nil when replica not configured",
			config: &TenantConfig{
				Databases: map[string]DatabaseConfig{
					"onboarding": {
						PostgreSQL: &PostgreSQLConfig{
							Host: "primary-db.example.com",
							Port: 5432,
						},
					},
				},
			},
			service:   "ledger",
			module:    "onboarding",
			expectNil: true,
		},
		{
			name:      "returns nil for unknown module",
			config:    newTenantConfigFixture(),
			service:   "ledger",
			module:    "unknown",
			expectNil: true,
		},
		{
			name:         "returns first replica config when module is empty",
			config:       newTenantConfigFixture(),
			service:      "ledger",
			module:       "",
			expectedHost: "", // non-nil but host depends on map iteration order
		},
		{
			name:      "returns nil when databases is nil",
			config:    &TenantConfig{},
			service:   "ledger",
			module:    "onboarding",
			expectNil: true,
		},
		{
			name: "returns nil when module exists but has no replica config",
			config: &TenantConfig{
				Databases: map[string]DatabaseConfig{
					"onboarding": {
						PostgreSQL: &PostgreSQLConfig{Host: "primary.example.com"},
					},
				},
			},
			service:   "ledger",
			module:    "onboarding",
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetPostgreSQLReplicaConfig(tt.service, tt.module)

			if tt.expectNil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			if tt.expectedHost != "" {
				assert.Equal(t, tt.expectedHost, result.Host)
			}
			if tt.expectedPort != 0 {
				assert.Equal(t, tt.expectedPort, result.Port)
			}
		})
	}
}

func TestTenantConfig_GetMongoDBConfig(t *testing.T) {
	tests := []struct {
		name             string
		config           *TenantConfig
		service          string
		module           string
		expectNil        bool
		expectedHost     string
		expectedDatabase string
	}{
		{
			name:             "returns config for onboarding module",
			config:           newTenantConfigFixture(),
			service:          "ledger",
			module:           "onboarding",
			expectedHost:     "onboarding-mongo.example.com",
			expectedDatabase: "onboarding_db",
		},
		{
			name:             "returns config for transaction module",
			config:           newTenantConfigFixture(),
			service:          "ledger",
			module:           "transaction",
			expectedHost:     "transaction-mongo.example.com",
			expectedDatabase: "transaction_db",
		},
		{
			name:      "returns nil for unknown module",
			config:    newTenantConfigFixture(),
			service:   "ledger",
			module:    "unknown",
			expectNil: true,
		},
		{
			name:         "returns first config when module is empty",
			config:       newTenantConfigFixture(),
			service:      "ledger",
			module:       "",
			expectedHost: "", // non-nil but host depends on map iteration order
		},
		{
			name:      "returns nil when databases is nil",
			config:    &TenantConfig{},
			service:   "ledger",
			module:    "onboarding",
			expectNil: true,
		},
		{
			name: "returns nil when module exists but has no MongoDB config",
			config: &TenantConfig{
				Databases: map[string]DatabaseConfig{
					"onboarding": {
						PostgreSQL: &PostgreSQLConfig{Host: "pg.example.com"},
					},
				},
			},
			service:   "ledger",
			module:    "onboarding",
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetMongoDBConfig(tt.service, tt.module)

			if tt.expectNil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			if tt.expectedHost != "" {
				assert.Equal(t, tt.expectedHost, result.Host)
			}
			if tt.expectedDatabase != "" {
				assert.Equal(t, tt.expectedDatabase, result.Database)
			}
		})
	}
}

func TestTenantConfig_IsSchemaMode(t *testing.T) {
	tests := []struct {
		name          string
		isolationMode string
		expected      bool
	}{
		{
			name:          "returns true when isolation mode is schema",
			isolationMode: "schema",
			expected:      true,
		},
		{
			name:          "returns false when isolation mode is isolated",
			isolationMode: "isolated",
			expected:      false,
		},
		{
			name:          "returns false when isolation mode is empty",
			isolationMode: "",
			expected:      false,
		},
		{
			name:          "returns false when isolation mode is unknown",
			isolationMode: "unknown",
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &TenantConfig{
				IsolationMode: tt.isolationMode,
			}

			result := config.IsSchemaMode()

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTenantConfig_IsIsolatedMode(t *testing.T) {
	tests := []struct {
		name          string
		isolationMode string
		expected      bool
	}{
		{
			name:          "returns true when isolation mode is isolated",
			isolationMode: "isolated",
			expected:      true,
		},
		{
			name:          "returns true when isolation mode is database",
			isolationMode: "database",
			expected:      true,
		},
		{
			name:          "returns true when isolation mode is empty (default)",
			isolationMode: "",
			expected:      true,
		},
		{
			name:          "returns false when isolation mode is schema",
			isolationMode: "schema",
			expected:      false,
		},
		{
			name:          "returns false when isolation mode is unknown",
			isolationMode: "unknown",
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &TenantConfig{
				IsolationMode: tt.isolationMode,
			}

			result := config.IsIsolatedMode()

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTenantConfig_ConnectionSettings(t *testing.T) {
	t.Run("deserializes connectionSettings from JSON", func(t *testing.T) {
		jsonData := `{
			"id": "cfg-123",
			"tenantSlug": "acme",
			"isolationMode": "schema",
			"connectionSettings": {
				"maxOpenConns": 20,
				"maxIdleConns": 10
			},
			"databases": {
				"onboarding": {
					"postgresql": {
						"host": "localhost",
						"port": 5432,
						"database": "testdb",
						"username": "user",
						"password": "pass"
					}
				}
			}
		}`

		var config TenantConfig
		err := json.Unmarshal([]byte(jsonData), &config)

		require.NoError(t, err)
		require.NotNil(t, config.ConnectionSettings)
		assert.Equal(t, 20, config.ConnectionSettings.MaxOpenConns)
		assert.Equal(t, 10, config.ConnectionSettings.MaxIdleConns)
	})

	t.Run("connectionSettings is nil when not present in JSON", func(t *testing.T) {
		jsonData := `{
			"id": "cfg-123",
			"tenantSlug": "acme",
			"isolationMode": "schema",
			"databases": {
				"onboarding": {
					"postgresql": {
						"host": "localhost",
						"port": 5432,
						"database": "testdb",
						"username": "user",
						"password": "pass"
					}
				}
			}
		}`

		var config TenantConfig
		err := json.Unmarshal([]byte(jsonData), &config)

		require.NoError(t, err)
		assert.Nil(t, config.ConnectionSettings)
	})

	t.Run("connectionSettings with zero values deserializes correctly", func(t *testing.T) {
		jsonData := `{
			"id": "cfg-123",
			"connectionSettings": {
				"maxOpenConns": 0,
				"maxIdleConns": 0
			}
		}`

		var config TenantConfig
		err := json.Unmarshal([]byte(jsonData), &config)

		require.NoError(t, err)
		require.NotNil(t, config.ConnectionSettings)
		assert.Equal(t, 0, config.ConnectionSettings.MaxOpenConns)
		assert.Equal(t, 0, config.ConnectionSettings.MaxIdleConns)
	})

	t.Run("connectionSettings with partial values deserializes correctly", func(t *testing.T) {
		jsonData := `{
			"id": "cfg-123",
			"connectionSettings": {
				"maxOpenConns": 30
			}
		}`

		var config TenantConfig
		err := json.Unmarshal([]byte(jsonData), &config)

		require.NoError(t, err)
		require.NotNil(t, config.ConnectionSettings)
		assert.Equal(t, 30, config.ConnectionSettings.MaxOpenConns)
		assert.Equal(t, 0, config.ConnectionSettings.MaxIdleConns)
	})
}
