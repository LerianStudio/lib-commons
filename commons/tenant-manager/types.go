// Package tenantmanager provides multi-tenant database connection management.
// It fetches tenant-specific database credentials from Tenant Manager service
// and manages connections per tenant.
package tenantmanager

import "time"

// PostgreSQLConfig holds PostgreSQL connection configuration.
type PostgreSQLConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	Schema   string `json:"schema,omitempty"`
	SSLMode  string `json:"sslmode,omitempty"`
}

// MongoDBConfig holds MongoDB connection configuration.
type MongoDBConfig struct {
	Host             string `json:"host,omitempty"`
	Port             int    `json:"port,omitempty"`
	Database         string `json:"database"`
	Username         string `json:"username,omitempty"`
	Password         string `json:"password,omitempty"`
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
	Password string `json:"password"`
}

// MessagingConfig holds messaging configuration for a tenant.
type MessagingConfig struct {
	RabbitMQ *RabbitMQConfig `json:"rabbitmq,omitempty"`
}

// ServiceDatabaseConfig holds database configurations for a service (ledger, audit, etc.).
// It contains a map of module names to their database configurations.
type ServiceDatabaseConfig struct {
	Services map[string]DatabaseConfig `json:"services,omitempty"`
}

// DatabaseConfig holds database configurations for a module (onboarding, transaction, etc.).
type DatabaseConfig struct {
	PostgreSQL        *PostgreSQLConfig `json:"postgresql,omitempty"`
	PostgreSQLReplica *PostgreSQLConfig `json:"postgresqlReplica,omitempty"`
	MongoDB           *MongoDBConfig    `json:"mongodb,omitempty"`
}

// TenantConfig represents the tenant configuration from Tenant Manager.
type TenantConfig struct {
	ID            string                           `json:"id"`
	TenantSlug    string                           `json:"tenantSlug"`
	TenantName    string                           `json:"tenantName,omitempty"`
	Service       string                           `json:"service,omitempty"`
	Status        string                           `json:"status,omitempty"`
	IsolationMode string                           `json:"isolationMode,omitempty"`
	Databases     map[string]ServiceDatabaseConfig `json:"databases,omitempty"`
	Messaging     *MessagingConfig                 `json:"messaging,omitempty"`
	CreatedAt     time.Time                        `json:"createdAt,omitempty"`
	UpdatedAt     time.Time                        `json:"updatedAt,omitempty"`
}

// GetPostgreSQLConfig returns the PostgreSQL config for a service and module.
// service: e.g., "ledger", "audit"
// module: e.g., "onboarding", "transaction"
// If module is empty, returns the first PostgreSQL config found for the service.
func (tc *TenantConfig) GetPostgreSQLConfig(service, module string) *PostgreSQLConfig {
	if tc.Databases == nil {
		return nil
	}

	svc, ok := tc.Databases[service]
	if !ok || svc.Services == nil {
		return nil
	}

	if module != "" {
		if db, ok := svc.Services[module]; ok {
			return db.PostgreSQL
		}
		return nil
	}

	// Return first PostgreSQL config found for the service
	for _, db := range svc.Services {
		if db.PostgreSQL != nil {
			return db.PostgreSQL
		}
	}

	return nil
}

// GetPostgreSQLReplicaConfig returns the PostgreSQL replica config for a service and module.
// service: e.g., "ledger", "audit"
// module: e.g., "onboarding", "transaction"
// If module is empty, returns the first PostgreSQL replica config found for the service.
// Returns nil if no replica is configured (callers should fall back to primary).
func (tc *TenantConfig) GetPostgreSQLReplicaConfig(service, module string) *PostgreSQLConfig {
	if tc.Databases == nil {
		return nil
	}

	svc, ok := tc.Databases[service]
	if !ok || svc.Services == nil {
		return nil
	}

	if module != "" {
		if db, ok := svc.Services[module]; ok {
			return db.PostgreSQLReplica
		}
		return nil
	}

	// Return first PostgreSQL replica config found for the service
	for _, db := range svc.Services {
		if db.PostgreSQLReplica != nil {
			return db.PostgreSQLReplica
		}
	}

	return nil
}

// GetMongoDBConfig returns the MongoDB config for a service and module.
// service: e.g., "ledger", "audit"
// module: e.g., "onboarding", "transaction"
// If module is empty, returns the first MongoDB config found for the service.
func (tc *TenantConfig) GetMongoDBConfig(service, module string) *MongoDBConfig {
	if tc.Databases == nil {
		return nil
	}

	svc, ok := tc.Databases[service]
	if !ok || svc.Services == nil {
		return nil
	}

	if module != "" {
		if db, ok := svc.Services[module]; ok {
			return db.MongoDB
		}
		return nil
	}

	// Return first MongoDB config found for the service
	for _, db := range svc.Services {
		if db.MongoDB != nil {
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
