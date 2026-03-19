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
	TLSCAFile        string `json:"tlsCAFile,omitempty"`     // path to CA certificate file
	TLSCertFile      string `json:"tlsCertFile,omitempty"`   // path to client certificate file
	TLSKeyFile       string `json:"tlsKeyFile,omitempty"`    // path to client private key file
	TLSSkipVerify    bool   `json:"tlsSkipVerify,omitempty"` // skip server certificate verification
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

// MessagingConfig holds messaging configuration for a tenant.
type MessagingConfig struct {
	RabbitMQ *RabbitMQConfig `json:"rabbitmq,omitempty"`
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
	MaxOpenConns int `json:"maxOpenConns"`
	MaxIdleConns int `json:"maxIdleConns"`
}

// TenantConfig represents the tenant configuration from Tenant Manager.
// The Databases map is keyed by module name (e.g., "onboarding", "transaction").
// This matches the flat format returned by the tenant-manager /v1/.../connections endpoint.
type TenantConfig struct {
	ID                 string                    `json:"id"`
	TenantSlug         string                    `json:"tenantSlug"`
	TenantName         string                    `json:"tenantName,omitempty"`
	Service            string                    `json:"service,omitempty"`
	Status             string                    `json:"status,omitempty"`
	IsolationMode      string                    `json:"isolationMode,omitempty"`
	Databases          map[string]DatabaseConfig `json:"databases,omitempty"`
	Messaging          *MessagingConfig          `json:"messaging,omitempty"`
	ConnectionSettings *ConnectionSettings       `json:"connectionSettings,omitempty"`
	CreatedAt          time.Time                 `json:"createdAt,omitzero"`
	UpdatedAt          time.Time                 `json:"updatedAt,omitzero"`
}

// sortedDatabaseKeys returns the keys of the Databases map in sorted order.
// This ensures deterministic behavior when module is empty.
func sortedDatabaseKeys(databases map[string]DatabaseConfig) []string {
	keys := make([]string, 0, len(databases))
	for k := range databases {
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
	keys := sortedDatabaseKeys(tc.Databases)
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
	keys := sortedDatabaseKeys(tc.Databases)
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
	keys := sortedDatabaseKeys(tc.Databases)
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

// GetRabbitMQConfig returns the RabbitMQ config for the tenant.
// Returns nil if messaging or RabbitMQ is not configured.
func (tc *TenantConfig) GetRabbitMQConfig() *RabbitMQConfig {
	if tc == nil {
		return nil
	}

	if tc.Messaging == nil {
		return nil
	}

	return tc.Messaging.RabbitMQ
}

// HasRabbitMQ returns true if the tenant has RabbitMQ configured.
func (tc *TenantConfig) HasRabbitMQ() bool {
	if tc == nil {
		return false
	}

	return tc.GetRabbitMQConfig() != nil
}
