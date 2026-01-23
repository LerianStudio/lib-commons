package tenantmanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTenantConfig_GetPostgreSQLConfig(t *testing.T) {
	t.Run("returns config for specific service and module", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							PostgreSQL: &PostgreSQLConfig{
								Host: "onboarding-db.example.com",
								Port: 5432,
							},
						},
						"transaction": {
							PostgreSQL: &PostgreSQLConfig{
								Host: "transaction-db.example.com",
								Port: 5432,
							},
						},
					},
				},
			},
		}

		pg := config.GetPostgreSQLConfig("ledger", "onboarding")

		assert.NotNil(t, pg)
		assert.Equal(t, "onboarding-db.example.com", pg.Host)

		pg = config.GetPostgreSQLConfig("ledger", "transaction")

		assert.NotNil(t, pg)
		assert.Equal(t, "transaction-db.example.com", pg.Host)
	})

	t.Run("returns nil for unknown service", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							PostgreSQL: &PostgreSQLConfig{Host: "localhost"},
						},
					},
				},
			},
		}

		pg := config.GetPostgreSQLConfig("unknown", "onboarding")

		assert.Nil(t, pg)
	})

	t.Run("returns nil for unknown module", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							PostgreSQL: &PostgreSQLConfig{Host: "localhost"},
						},
					},
				},
			},
		}

		pg := config.GetPostgreSQLConfig("ledger", "unknown")

		assert.Nil(t, pg)
	})

	t.Run("returns first config when module is empty", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							PostgreSQL: &PostgreSQLConfig{Host: "localhost"},
						},
					},
				},
			},
		}

		pg := config.GetPostgreSQLConfig("ledger", "")

		assert.NotNil(t, pg)
		assert.Equal(t, "localhost", pg.Host)
	})

	t.Run("returns nil when databases is nil", func(t *testing.T) {
		config := &TenantConfig{}

		pg := config.GetPostgreSQLConfig("ledger", "onboarding")

		assert.Nil(t, pg)
	})

	t.Run("returns nil when services is nil", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {},
			},
		}

		pg := config.GetPostgreSQLConfig("ledger", "onboarding")

		assert.Nil(t, pg)
	})
}

func TestTenantConfig_GetMongoDBConfig(t *testing.T) {
	t.Run("returns config for specific service and module", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							MongoDB: &MongoDBConfig{
								Host:     "onboarding-mongo.example.com",
								Port:     27017,
								Database: "onboarding_db",
							},
						},
						"transaction": {
							MongoDB: &MongoDBConfig{
								Host:     "transaction-mongo.example.com",
								Port:     27017,
								Database: "transaction_db",
							},
						},
					},
				},
			},
		}

		mongo := config.GetMongoDBConfig("ledger", "onboarding")

		assert.NotNil(t, mongo)
		assert.Equal(t, "onboarding-mongo.example.com", mongo.Host)
		assert.Equal(t, "onboarding_db", mongo.Database)

		mongo = config.GetMongoDBConfig("ledger", "transaction")

		assert.NotNil(t, mongo)
		assert.Equal(t, "transaction-mongo.example.com", mongo.Host)
		assert.Equal(t, "transaction_db", mongo.Database)
	})

	t.Run("returns nil for unknown service", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							MongoDB: &MongoDBConfig{Host: "localhost"},
						},
					},
				},
			},
		}

		mongo := config.GetMongoDBConfig("unknown", "onboarding")

		assert.Nil(t, mongo)
	})

	t.Run("returns nil for unknown module", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							MongoDB: &MongoDBConfig{Host: "localhost"},
						},
					},
				},
			},
		}

		mongo := config.GetMongoDBConfig("ledger", "unknown")

		assert.Nil(t, mongo)
	})

	t.Run("returns first config when module is empty", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {
					Services: map[string]DatabaseConfig{
						"onboarding": {
							MongoDB: &MongoDBConfig{Host: "localhost", Database: "test_db"},
						},
					},
				},
			},
		}

		mongo := config.GetMongoDBConfig("ledger", "")

		assert.NotNil(t, mongo)
		assert.Equal(t, "localhost", mongo.Host)
	})

	t.Run("returns nil when databases is nil", func(t *testing.T) {
		config := &TenantConfig{}

		mongo := config.GetMongoDBConfig("ledger", "onboarding")

		assert.Nil(t, mongo)
	})

	t.Run("returns nil when services is nil", func(t *testing.T) {
		config := &TenantConfig{
			Databases: map[string]ServiceDatabaseConfig{
				"ledger": {},
			},
		}

		mongo := config.GetMongoDBConfig("ledger", "onboarding")

		assert.Nil(t, mongo)
	})
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
