// Package core provides shared types, errors, and context helpers used by all
// tenant-manager sub-packages.
package core

import (
	"sort"
	"time"
)

// PostgreSQLConfig holds PostgreSQL connection configuration.
// Credentials are provided directly by the tenant-manager settings endpoint.
type PostgreSQLConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Database    string `json:"database"`
	Username    string `json:"username"`
	Password    string `json:"password"` // #nosec G117
	Schema      string `json:"schema,omitempty"`
	SSLMode     string `json:"sslmode,omitempty"`
	SSLRootCert string `json:"sslrootcert,omitempty"` // path to CA certificate file
	SSLCert     string `json:"sslcert,omitempty"`     // path to client certificate file
	SSLKey      string `json:"sslkey,omitempty"`      // path to client private key file
}

// MongoDBConfig holds MongoDB connection configuration.
// Credentials are provided directly by the tenant-manager settings endpoint.
type MongoDBConfig struct {
	Host             string `json:"host,omitempty"`
	Port             int    `json:"port,omitempty"`
	Database         string `json:"database"`
	Username         string `json:"username,omitempty"`
	Password         string `json:"password,omitempty"` // #nosec G117
	URI              string `json:"uri,omitempty"`
	AuthSource       string `json:"authSource,omitempty"`
	DirectConnection bool   `json:"directConnection,omitempty"`
	MaxPoolSize      uint64 `json:"maxPoolSize,omitempty"`
	TLS              bool   `json:"tls,omitempty"`
	TLSCAFile        string `json:"tlsCAFile,omitempty"`   // path to CA certificate file
	TLSCertFile      string `json:"tlsCertFile,omitempty"` // path to client certificate file
	TLSKeyFile       string `json:"tlsKeyFile,omitempty"`  // path to client private key file
	// TLSSkipVerify disables both certificate-chain validation and hostname
	// verification (maps to MongoDB tlsInsecure). Use only in trusted environments;
	// enabling this flag significantly increases the risk of man-in-the-middle attacks.
	TLSSkipVerify bool `json:"tlsSkipVerify,omitempty"`
}

// RabbitMQConfig holds RabbitMQ connection configuration for tenant vhosts.
type RabbitMQConfig struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	VHost     string `json:"vhost"`
	Username  string `json:"username"`
	Password  string `json:"password"`            // #nosec G117
	TLS       *bool  `json:"tls,omitempty"`       // enable TLS (amqps://); nil = use global default
	TLSCAFile string `json:"tlsCAFile,omitempty"` // path to CA certificate file for custom CAs
}

// MessagingConfig holds messaging configuration for a single module.
type MessagingConfig struct {
	RabbitMQ *RabbitMQConfig `json:"rabbitmq,omitempty"`
}

// KafkaConfig holds Kafka connection configuration for tenant streaming.
type KafkaConfig struct {
	Brokers   []string `json:"brokers"`
	Username  string   `json:"username"`
	Password  string   `json:"password"` // #nosec G117
	Mechanism string   `json:"mechanism"`
	TLS       *bool    `json:"tls,omitempty"` // enable TLS; nil = use global default
}

// StreamingConfig holds streaming configuration for a single module.
type StreamingConfig struct {
	Kafka *KafkaConfig `json:"kafka,omitempty"`
}

// DatabaseConfig holds database configurations for a module (onboarding, transaction, etc.).
// In the flat format returned by tenant-manager, the Databases map is keyed by module name
// directly (e.g., "onboarding", "transaction"), without an intermediate service wrapper.
type DatabaseConfig struct {
	PostgreSQL         *PostgreSQLConfig   `json:"postgresql,omitempty"`
	PostgreSQLReplica  *PostgreSQLConfig   `json:"postgresqlReplica,omitempty"`
	MongoDB            *MongoDBConfig      `json:"mongodb,omitempty"`
	ConnectionSettings *ConnectionSettings `json:"connectionSettings,omitempty"`
}

// ConnectionSettings holds per-tenant database connection pool settings.
// When present in the tenant config response, these values override the global
// defaults configured on the PostgresManager or MongoManager.
// If nil (e.g., for older associations without settings), global defaults apply.
type ConnectionSettings struct {
	MaxOpenConns     int    `json:"maxOpenConns"`
	MaxIdleConns     int    `json:"maxIdleConns"`
	StatementTimeout string `json:"statementTimeout,omitempty"`
}

// TenantConfig represents the tenant configuration from Tenant Manager.
// The Databases map is keyed by module name (e.g., "onboarding", "transaction").
// The Messaging and Streaming maps follow the same convention: they are keyed by
// module name, matching the flat format returned by the tenant-manager
// /v1/.../connections endpoint.
type TenantConfig struct {
	ID                 string                     `json:"id"`
	TenantSlug         string                     `json:"tenantSlug"`
	TenantName         string                     `json:"tenantName,omitempty"`
	Service            string                     `json:"service,omitempty"`
	Status             string                     `json:"status,omitempty"`
	IsolationMode      string                     `json:"isolationMode,omitempty"`
	Databases          map[string]DatabaseConfig  `json:"databases,omitempty"`
	Messaging          map[string]MessagingConfig `json:"messaging,omitempty"`
	Streaming          map[string]StreamingConfig `json:"streaming,omitempty"`
	ConnectionSettings *ConnectionSettings        `json:"connectionSettings,omitempty"`
	CreatedAt          time.Time                  `json:"createdAt,omitzero"`
	UpdatedAt          time.Time                  `json:"updatedAt,omitzero"`
}

// sortedKeys returns the keys of a string-keyed map in sorted order.
// This ensures deterministic behavior when selecting the "first" entry
// (e.g., for the Databases, Messaging, and Streaming maps when module is empty).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

// GetPostgreSQLConfig returns the PostgreSQL config for a module.
// module: e.g., "onboarding", "transaction"
// If module is empty, returns the first PostgreSQL config found (sorted by key for determinism).
// The service parameter is accepted for backward compatibility but is ignored
// since the flat format returned by tenant-manager keys databases by module directly.
func (tc *TenantConfig) GetPostgreSQLConfig(service, module string) *PostgreSQLConfig {
	if tc == nil {
		return nil
	}

	if tc.Databases == nil {
		return nil
	}

	if module != "" {
		if db, ok := tc.Databases[module]; ok {
			return db.PostgreSQL
		}

		return nil
	}

	// Return first PostgreSQL config found (deterministic: sorted by key)
	keys := sortedKeys(tc.Databases)
	for _, key := range keys {
		if db := tc.Databases[key]; db.PostgreSQL != nil {
			return db.PostgreSQL
		}
	}

	return nil
}

// GetPostgreSQLReplicaConfig returns the PostgreSQL replica config for a module.
// module: e.g., "onboarding", "transaction"
// If module is empty, returns the first PostgreSQL replica config found (sorted by key for determinism).
// Returns nil if no replica is configured (callers should fall back to primary).
// The service parameter is accepted for backward compatibility but is ignored
// since the flat format returned by tenant-manager keys databases by module directly.
func (tc *TenantConfig) GetPostgreSQLReplicaConfig(service, module string) *PostgreSQLConfig {
	if tc == nil {
		return nil
	}

	if tc.Databases == nil {
		return nil
	}

	if module != "" {
		if db, ok := tc.Databases[module]; ok {
			return db.PostgreSQLReplica
		}

		return nil
	}

	// Return first PostgreSQL replica config found (deterministic: sorted by key)
	keys := sortedKeys(tc.Databases)
	for _, key := range keys {
		if db := tc.Databases[key]; db.PostgreSQLReplica != nil {
			return db.PostgreSQLReplica
		}
	}

	return nil
}

// GetMongoDBConfig returns the MongoDB config for a module.
// module: e.g., "onboarding", "transaction"
// If module is empty, returns the first MongoDB config found (sorted by key for determinism).
// The service parameter is accepted for backward compatibility but is ignored
// since the flat format returned by tenant-manager keys databases by module directly.
func (tc *TenantConfig) GetMongoDBConfig(service, module string) *MongoDBConfig {
	if tc == nil {
		return nil
	}

	if tc.Databases == nil {
		return nil
	}

	if module != "" {
		if db, ok := tc.Databases[module]; ok {
			return db.MongoDB
		}

		return nil
	}

	// Return first MongoDB config found (deterministic: sorted by key)
	keys := sortedKeys(tc.Databases)
	for _, key := range keys {
		if db := tc.Databases[key]; db.MongoDB != nil {
			return db.MongoDB
		}
	}

	return nil
}

// IsSchemaMode returns true if the tenant is configured for schema-based isolation.
// In schema mode, all tenants share the same database but have separate schemas.
func (tc *TenantConfig) IsSchemaMode() bool {
	if tc == nil {
		return false
	}

	return tc.IsolationMode == "schema"
}

// IsIsolatedMode returns true if the tenant has a dedicated database (isolated mode).
// This is the default mode when IsolationMode is empty or explicitly set to "isolated" or "database".
func (tc *TenantConfig) IsIsolatedMode() bool {
	if tc == nil {
		return false
	}

	return tc.IsolationMode == "" || tc.IsolationMode == "isolated" || tc.IsolationMode == "database"
}

// GetRabbitMQConfig returns the RabbitMQ config for a module.
// module: e.g., "onboarding", "transaction"
// If module is empty, returns the first RabbitMQ config found (sorted by key for determinism).
// Returns nil if messaging or RabbitMQ is not configured for the module.
func (tc *TenantConfig) GetRabbitMQConfig(module string) *RabbitMQConfig {
	if tc == nil {
		return nil
	}

	if tc.Messaging == nil {
		return nil
	}

	if module != "" {
		if msg, ok := tc.Messaging[module]; ok {
			return msg.RabbitMQ
		}

		return nil
	}

	// Return first RabbitMQ config found (deterministic: sorted by key)
	keys := sortedKeys(tc.Messaging)
	for _, key := range keys {
		if msg := tc.Messaging[key]; msg.RabbitMQ != nil {
			return msg.RabbitMQ
		}
	}

	return nil
}

// GetKafkaConfig returns the Kafka config for a module.
// module: e.g., "onboarding", "transaction"
// If module is empty, returns the first Kafka config found (sorted by key for determinism).
// Returns nil if streaming or Kafka is not configured for the module.
func (tc *TenantConfig) GetKafkaConfig(module string) *KafkaConfig {
	if tc == nil {
		return nil
	}

	if tc.Streaming == nil {
		return nil
	}

	if module != "" {
		if str, ok := tc.Streaming[module]; ok {
			return str.Kafka
		}

		return nil
	}

	// Return first Kafka config found (deterministic: sorted by key)
	keys := sortedKeys(tc.Streaming)
	for _, key := range keys {
		if str := tc.Streaming[key]; str.Kafka != nil {
			return str.Kafka
		}
	}

	return nil
}

// HasRabbitMQ returns true if the tenant has RabbitMQ configured for any module.
func (tc *TenantConfig) HasRabbitMQ() bool {
	if tc == nil {
		return false
	}

	return tc.GetRabbitMQConfig("") != nil
}
