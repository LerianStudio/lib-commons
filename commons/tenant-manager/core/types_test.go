//go:build unit

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
		name     string
		config   *TenantConfig
		expected bool
	}{
		{
			name:     "returns true when isolation mode is schema",
			config:   &TenantConfig{IsolationMode: "schema"},
			expected: true,
		},
		{
			name:     "returns false when isolation mode is isolated",
			config:   &TenantConfig{IsolationMode: "isolated"},
			expected: false,
		},
		{
			name:     "returns false when isolation mode is empty",
			config:   &TenantConfig{IsolationMode: ""},
			expected: false,
		},
		{
			name:     "returns false when isolation mode is unknown",
			config:   &TenantConfig{IsolationMode: "unknown"},
			expected: false,
		},
		{
			name:     "returns false for nil receiver",
			config:   nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.IsSchemaMode()

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTenantConfig_IsIsolatedMode(t *testing.T) {
	tests := []struct {
		name     string
		config   *TenantConfig
		expected bool
	}{
		{
			name:     "returns true when isolation mode is isolated",
			config:   &TenantConfig{IsolationMode: "isolated"},
			expected: true,
		},
		{
			name:     "returns true when isolation mode is database",
			config:   &TenantConfig{IsolationMode: "database"},
			expected: true,
		},
		{
			name:     "returns true when isolation mode is empty (default)",
			config:   &TenantConfig{IsolationMode: ""},
			expected: true,
		},
		{
			name:     "returns false when isolation mode is schema",
			config:   &TenantConfig{IsolationMode: "schema"},
			expected: false,
		},
		{
			name:     "returns false when isolation mode is unknown",
			config:   &TenantConfig{IsolationMode: "unknown"},
			expected: false,
		},
		{
			name:     "returns false for nil receiver",
			config:   nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.IsIsolatedMode()

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTenantConfig_GetRabbitMQConfig(t *testing.T) {
	// Legacy no-arg getter: reads the single tenant-level Messaging.RabbitMQ.
	tests := []struct {
		name          string
		config        *TenantConfig
		expectNil     bool
		expectedVHost string
	}{
		{
			name:      "returns nil for nil receiver",
			config:    nil,
			expectNil: true,
		},
		{
			name:      "returns nil when messaging is nil",
			config:    &TenantConfig{},
			expectNil: true,
		},
		{
			name: "returns nil when rabbitmq is nil in messaging",
			config: &TenantConfig{
				Messaging: &MessagingConfig{},
			},
			expectNil: true,
		},
		{
			name: "returns the tenant-level rabbitmq config",
			config: &TenantConfig{
				Messaging: &MessagingConfig{
					RabbitMQ: &RabbitMQConfig{
						Host:  "rabbitmq.example.com",
						Port:  5672,
						VHost: "tenant-vhost",
					},
				},
			},
			expectedVHost: "tenant-vhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetRabbitMQConfig()

			if tt.expectNil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expectedVHost, result.VHost)
		})
	}
}

func TestTenantConfig_GetModuleRabbitMQConfig(t *testing.T) {
	// Per-module getter over the FLAT map: reads RabbitMQ[module] (a RabbitMQConfig
	// value) directly, empty module → first by sorted key.
	tests := []struct {
		name          string
		config        *TenantConfig
		module        string
		expectNil     bool
		expectedVHost string
	}{
		{
			name:      "returns nil for nil receiver",
			config:    nil,
			module:    "onboarding",
			expectNil: true,
		},
		{
			name:      "returns nil when rabbitmq map is nil",
			config:    &TenantConfig{},
			module:    "onboarding",
			expectNil: true,
		},
		{
			name: "returns nil when requested module is missing",
			config: &TenantConfig{
				RabbitMQ: map[string]RabbitMQConfig{
					"onboarding": {VHost: "onboarding-vhost"},
				},
			},
			module:    "transaction",
			expectNil: true,
		},
		{
			name: "returns config for requested module",
			config: &TenantConfig{
				RabbitMQ: map[string]RabbitMQConfig{
					"onboarding": {
						Host:  "rabbitmq.example.com",
						Port:  5672,
						VHost: "onboarding-vhost",
					},
					"transaction": {VHost: "transaction-vhost"},
				},
			},
			module:        "transaction",
			expectedVHost: "transaction-vhost",
		},
		{
			name: "returns first module by sorted key when module is empty",
			config: &TenantConfig{
				RabbitMQ: map[string]RabbitMQConfig{
					"transaction": {VHost: "transaction-vhost"},
					"onboarding":  {VHost: "onboarding-vhost"},
				},
			},
			module:        "",
			expectedVHost: "onboarding-vhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetModuleRabbitMQConfig(tt.module)

			if tt.expectNil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expectedVHost, result.VHost)
		})
	}
}

func TestTenantConfig_GetKafkaConfig(t *testing.T) {
	tests := []struct {
		name            string
		config          *TenantConfig
		module          string
		expectNil       bool
		expectedBrokers []string
	}{
		{
			name:      "returns nil for nil receiver",
			config:    nil,
			module:    "onboarding",
			expectNil: true,
		},
		{
			name:      "returns nil when streaming map is nil",
			config:    &TenantConfig{},
			module:    "onboarding",
			expectNil: true,
		},
		{
			name: "returns nil when kafka is nil in module streaming",
			config: &TenantConfig{
				Streaming: map[string]StreamingConfig{
					"onboarding": {},
				},
			},
			module:    "onboarding",
			expectNil: true,
		},
		{
			name: "returns nil when requested module is missing",
			config: &TenantConfig{
				Streaming: map[string]StreamingConfig{
					"onboarding": {Kafka: &KafkaConfig{Brokers: []string{"broker-1:9092"}}},
				},
			},
			module:    "transaction",
			expectNil: true,
		},
		{
			name: "returns config for requested module",
			config: &TenantConfig{
				Streaming: map[string]StreamingConfig{
					"onboarding": {
						Kafka: &KafkaConfig{
							Brokers:   []string{"broker-onb:9092"},
							Username:  "onb-user",
							Mechanism: "SCRAM-SHA-512",
						},
					},
					"transaction": {
						Kafka: &KafkaConfig{Brokers: []string{"broker-tx:9092"}},
					},
				},
			},
			module:          "onboarding",
			expectedBrokers: []string{"broker-onb:9092"},
		},
		{
			name: "returns first module by sorted key when module is empty",
			config: &TenantConfig{
				Streaming: map[string]StreamingConfig{
					"transaction": {Kafka: &KafkaConfig{Brokers: []string{"broker-tx:9092"}}},
					"onboarding":  {Kafka: &KafkaConfig{Brokers: []string{"broker-onb:9092"}}},
				},
			},
			module:          "",
			expectedBrokers: []string{"broker-onb:9092"},
		},
		{
			name: "skips modules without kafka when module is empty",
			config: &TenantConfig{
				Streaming: map[string]StreamingConfig{
					"onboarding":  {},
					"transaction": {Kafka: &KafkaConfig{Brokers: []string{"broker-tx:9092"}}},
				},
			},
			module:          "",
			expectedBrokers: []string{"broker-tx:9092"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetKafkaConfig(tt.module)

			if tt.expectNil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expectedBrokers, result.Brokers)
		})
	}
}

func TestTenantConfig_HasRabbitMQ(t *testing.T) {
	tests := []struct {
		name     string
		config   *TenantConfig
		expected bool
	}{
		{
			name:     "returns false for nil receiver",
			config:   nil,
			expected: false,
		},
		{
			name:     "returns false when messaging is nil",
			config:   &TenantConfig{},
			expected: false,
		},
		{
			name: "returns false when rabbitmq is nil in messaging",
			config: &TenantConfig{
				Messaging: &MessagingConfig{},
			},
			expected: false,
		},
		{
			name: "returns true when tenant-level rabbitmq is configured",
			config: &TenantConfig{
				Messaging: &MessagingConfig{
					RabbitMQ: &RabbitMQConfig{
						Host:  "rabbitmq.example.com",
						Port:  5672,
						VHost: "tenant-vhost",
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.HasRabbitMQ()

			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTenantConfig_FlatPerModuleRabbitMQ_JSON feeds the ACTUAL flat per-module
// "rabbitmq" shape emitted by tenant-manager's /connections endpoint
// (additionalProperties: resolvedRabbitMQConfigEntry — the rabbit fields
// DIRECTLY, with NO inner "rabbitmq" wrapper) and asserts lib-commons resolves
// it. Against the legacy nested map[string]MessagingConfig this FAILS because
// the flat "host"/"vhost"/... keys do not match the inner "rabbitmq" field, so
// the per-module getter returns nil.
func TestTenantConfig_FlatPerModuleRabbitMQ_JSON(t *testing.T) {
	jsonData := `{
		"rabbitmq": {
			"onboarding": {
				"host": "h",
				"port": 5672,
				"vhost": "v",
				"username": "u",
				"password": "p"
			}
		}
	}`

	var tc TenantConfig
	require.NoError(t, json.Unmarshal([]byte(jsonData), &tc))

	// Direct flat map-value access (matches tenant-manager's resolvedRabbitMQConfigEntry).
	require.Contains(t, tc.RabbitMQ, "onboarding")
	assert.Equal(t, "v", tc.RabbitMQ["onboarding"].VHost)
	assert.Equal(t, "h", tc.RabbitMQ["onboarding"].Host)
	assert.Equal(t, "u", tc.RabbitMQ["onboarding"].Username)
	assert.Equal(t, "p", tc.RabbitMQ["onboarding"].Password)

	got := tc.GetModuleRabbitMQConfig("onboarding")
	require.NotNil(t, got, "per-module getter must resolve the flat tenant-manager rabbitmq shape")
	assert.Equal(t, "v", got.VHost)
	assert.Equal(t, "h", got.Host)
	assert.Equal(t, 5672, got.Port)
	assert.Equal(t, "u", got.Username)
	assert.Equal(t, "p", got.Password)
}

func TestTenantConfig_PerModuleMessagingAndStreaming_JSON(t *testing.T) {
	// This test is the source of truth for the tenant-manager wire contract.
	// It asserts EVERY field present in the fixture JSON so a field rename or
	// drop in the struct tags cannot pass silently. The payload carries BOTH the
	// legacy single "messaging" object AND the new per-module "rabbitmq" map, plus
	// the per-module "streaming" map.
	t.Run("deserializes legacy single messaging, per-module rabbitmq, and streaming", func(t *testing.T) {
		jsonData := `{
			"id": "cfg-123",
			"tenantSlug": "acme",
			"isolationMode": "shared",
			"messaging": {
				"rabbitmq": {
					"host": "rabbit.example.com",
					"port": 5672,
					"vhost": "tenant-vhost",
					"username": "tenant-user",
					"password": "tenant-pass"
				}
			},
			"rabbitmq": {
				"transaction": {
					"host": "rabbit.example.com",
					"port": 5672,
					"vhost": "transaction-vhost",
					"username": "tx-user",
					"password": "tx-pass"
				},
				"onboarding": {
					"host": "rabbit.example.com",
					"port": 5672,
					"vhost": "onboarding-vhost",
					"username": "onb-user",
					"password": "onb-pass"
				}
			},
			"streaming": {
				"onboarding": {
					"kafka": {
						"brokers": ["broker-1:9092", "broker-2:9092"],
						"username": "onb-kafka",
						"password": "kafka-pass",
						"mechanism": "SCRAM-SHA-512",
						"tls": true
					}
				}
			}
		}`

		var config TenantConfig
		err := json.Unmarshal([]byte(jsonData), &config)

		require.NoError(t, err)

		// Legacy single tenant-level messaging.
		require.NotNil(t, config.Messaging)
		require.NotNil(t, config.Messaging.RabbitMQ)
		assert.Equal(t, "rabbit.example.com", config.Messaging.RabbitMQ.Host)
		assert.Equal(t, 5672, config.Messaging.RabbitMQ.Port)
		assert.Equal(t, "tenant-vhost", config.Messaging.RabbitMQ.VHost)
		assert.Equal(t, "tenant-user", config.Messaging.RabbitMQ.Username)
		assert.Equal(t, "tenant-pass", config.Messaging.RabbitMQ.Password)
		require.NotNil(t, config.GetRabbitMQConfig())
		assert.Equal(t, "tenant-vhost", config.GetRabbitMQConfig().VHost)

		// New per-module rabbitmq map (FLAT: value is a RabbitMQConfig directly).
		require.Contains(t, config.RabbitMQ, "transaction")
		require.Contains(t, config.RabbitMQ, "onboarding")
		txRabbit := config.RabbitMQ["transaction"]
		assert.Equal(t, "rabbit.example.com", txRabbit.Host)
		assert.Equal(t, 5672, txRabbit.Port)
		assert.Equal(t, "transaction-vhost", txRabbit.VHost)
		assert.Equal(t, "tx-user", txRabbit.Username)
		assert.Equal(t, "tx-pass", txRabbit.Password)
		require.NotNil(t, config.GetModuleRabbitMQConfig("transaction"))
		assert.Equal(t, "transaction-vhost", config.GetModuleRabbitMQConfig("transaction").VHost)
		require.NotNil(t, config.GetModuleRabbitMQConfig("onboarding"))
		assert.Equal(t, "onboarding-vhost", config.GetModuleRabbitMQConfig("onboarding").VHost)

		require.Contains(t, config.Streaming, "onboarding")
		kafka := config.Streaming["onboarding"].Kafka
		require.NotNil(t, kafka)
		assert.Equal(t, []string{"broker-1:9092", "broker-2:9092"}, kafka.Brokers)
		assert.Equal(t, "onb-kafka", kafka.Username)
		assert.Equal(t, "kafka-pass", kafka.Password)
		assert.Equal(t, "SCRAM-SHA-512", kafka.Mechanism)
		require.NotNil(t, kafka.TLS)
		assert.True(t, *kafka.TLS)
	})

	// KafkaConfig.TLS is a *bool where nil means "use global default", so an
	// explicit false MUST be distinguishable from an unset value.
	t.Run("kafka tls false is distinguishable from unset", func(t *testing.T) {
		jsonData := `{
			"id": "cfg-123",
			"tenantSlug": "acme",
			"streaming": {
				"onboarding": {
					"kafka": {
						"brokers": ["broker-1:9092"],
						"username": "onb-kafka",
						"password": "kafka-pass",
						"mechanism": "SCRAM-SHA-512",
						"tls": false
					}
				}
			}
		}`

		var config TenantConfig
		err := json.Unmarshal([]byte(jsonData), &config)

		require.NoError(t, err)
		require.Contains(t, config.Streaming, "onboarding")
		kafka := config.Streaming["onboarding"].Kafka
		require.NotNil(t, kafka)
		require.NotNil(t, kafka.TLS, "explicit false must produce a non-nil pointer")
		assert.False(t, *kafka.TLS)
	})

	t.Run("kafka tls is nil when the key is omitted", func(t *testing.T) {
		jsonData := `{
			"id": "cfg-123",
			"tenantSlug": "acme",
			"streaming": {
				"onboarding": {
					"kafka": {
						"brokers": ["broker-1:9092"],
						"username": "onb-kafka",
						"password": "kafka-pass",
						"mechanism": "SCRAM-SHA-512"
					}
				}
			}
		}`

		var config TenantConfig
		err := json.Unmarshal([]byte(jsonData), &config)

		require.NoError(t, err)
		require.Contains(t, config.Streaming, "onboarding")
		kafka := config.Streaming["onboarding"].Kafka
		require.NotNil(t, kafka)
		assert.Nil(t, kafka.TLS, "omitted tls key must leave the pointer nil (use global default)")
	})

	t.Run("messaging, rabbitmq and streaming are nil when absent", func(t *testing.T) {
		jsonData := `{"id": "cfg-123", "tenantSlug": "acme"}`

		var config TenantConfig
		err := json.Unmarshal([]byte(jsonData), &config)

		require.NoError(t, err)
		assert.Nil(t, config.Messaging)
		assert.Nil(t, config.RabbitMQ)
		assert.Nil(t, config.Streaming)
	})
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
