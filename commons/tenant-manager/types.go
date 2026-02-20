// Package tenantmanager provides multi-tenant database connection management.
// It fetches tenant-specific database credentials from Tenant Manager service
// and manages connections per tenant.
package tenantmanager

import (
	"sort"
	"time"
)

// PostgreSQLConfig holds PostgreSQL connection configuration.
// Credentials are provided directly by the tenant-manager settings endpoint.
type PostgreSQLConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"` // #nosec G101 -- DTO field for tenant credentials, not a hardcoded secret
	Schema   string `json:"schema,omitempty"`
	SSLMode  string `json:"sslmode,omitempty"`
}

// MongoDBConfig holds MongoDB connection configuration.
// Credentials are provided directly by the tenant-manager settings endpoint.
type MongoDBConfig struct {
	Host             string `json:"host,omitempty"`
	Port             int    `json:"port,omitempty"`
	Database         string `json:"database"`
	Username         string `json:"username,omitempty"`
	Password         string `json:"password,omitempty"` // #nosec G101 -- DTO field for tenant credentials, not a hardcoded secret
	URI              string `json:"uri,omitempty"`
	AuthSource       string `json:"authSource,omitempty"`
	DirectConnection bool   `json:"directConnection,omitempty"`
	MaxPoolSize      uint64 `json:"maxPoolSize,omitempty"`
}

// RabbitMQConfig holds RabbitMQ connection configuration for tenant vhosts.
type RabbitMQConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	VHost    string `json:"vhost"`
	Username string `json:"username"`
	Password string `json:"password"` // #nosec G101 -- DTO field for tenant credentials, not a hardcoded secret
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
// This matches the flat format returned by the tenant-manager /settings endpoint.
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
	CreatedAt          time.Time                 `json:"createdAt,omitempty"`
	UpdatedAt          time.Time                 `json:"updatedAt,omitempty"`
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
	return tc.IsolationMode == "schema"
}

// IsIsolatedMode returns true if the tenant has a dedicated database (isolated mode).
// This is the default mode when IsolationMode is empty or explicitly set to "isolated" or "database".
func (tc *TenantConfig) IsIsolatedMode() bool {
	return tc.IsolationMode == "" || tc.IsolationMode == "isolated" || tc.IsolationMode == "database"
}

// GetRabbitMQConfig returns the RabbitMQ config for the tenant.
// Returns nil if messaging or RabbitMQ is not configured.
func (tc *TenantConfig) GetRabbitMQConfig() *RabbitMQConfig {
	if tc.Messaging == nil {
		return nil
	}

	return tc.Messaging.RabbitMQ
}

// HasRabbitMQ returns true if the tenant has RabbitMQ configured.
func (tc *TenantConfig) HasRabbitMQ() bool {
	return tc.GetRabbitMQConfig() != nil
}
