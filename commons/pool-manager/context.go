package poolmanager

import (
	"context"

	libMongo "github.com/LerianStudio/lib-commons/v2/commons/mongo"
	libPostgres "github.com/LerianStudio/lib-commons/v2/commons/postgres"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/gofiber/fiber/v2"
)

// ContextKey is the type used for context keys in tenant operations.
// It provides type safety and prevents collisions with other context keys.
type ContextKey string

// Context keys for storing tenant information.
// These are exported to allow external packages to reference them if needed,
// while the actual context operations should use the helper functions.
const (
	// TenantIDContextKey is the key for storing tenant ID in context.
	TenantIDContextKey ContextKey = "tenant_id"

	// TenantConfigContextKey is the key for storing tenant configuration in context.
	TenantConfigContextKey ContextKey = "tenant_config"

	// TenantPGContextKey is the key for storing PostgreSQL configuration in context.
	TenantPGContextKey ContextKey = "tenant_pg"

	// TenantMongoContextKey is the key for storing MongoDB configuration in context.
	TenantMongoContextKey ContextKey = "tenant_mongo"

	// TenantValkeyContextKey is the key for storing Valkey configuration in context.
	TenantValkeyContextKey ContextKey = "tenant_valkey"

	// TenantRabbitMQContextKey is the key for storing RabbitMQ configuration in context.
	TenantRabbitMQContextKey ContextKey = "tenant_rabbitmq"

	// TenantPGConnectionContextKey is the key for storing actual PostgreSQL connection in context.
	TenantPGConnectionContextKey ContextKey = "tenant_pg_connection"

	// TenantMongoDBConnectionContextKey is the key for storing actual MongoDB database in context.
	TenantMongoDBConnectionContextKey ContextKey = "tenant_mongo_connection"
)

// DBType represents the type of database.
type DBType string

// Database type constants.
const (
	// DBTypePostgreSQL represents PostgreSQL database type.
	DBTypePostgreSQL DBType = "postgresql"

	// DBTypeMongoDB represents MongoDB database type.
	DBTypeMongoDB DBType = "mongodb"
)

// GetDBFromContext returns the database configuration for the specified type from context.
// It supports both standard context.Context and *fiber.Ctx.
// Returns nil if the context is nil, the database type is unknown, or no configuration is found.
//
// Example:
//
//	pgConfig := GetDBFromContext(ctx, DBTypePostgreSQL)
//	if pgConfig != nil {
//	    config := pgConfig.(*PostgreSQLConfig)
//	    // Use config...
//	}
func GetDBFromContext(ctx context.Context, dbType DBType) interface{} {
	if ctx == nil {
		return nil
	}

	switch dbType {
	case DBTypePostgreSQL:
		return GetTenantPostgreSQL(ctx)
	case DBTypeMongoDB:
		return GetTenantMongoDB(ctx)
	default:
		return nil
	}
}

// GetTenantIDFromFiber extracts the tenant ID from a Fiber context.
// Returns an empty string if the context is nil or no tenant ID is found.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    tenantID := GetTenantIDFromFiber(c)
//	    if tenantID == "" {
//	        return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no tenant"})
//	    }
//	    // Use tenantID...
//	})
func GetTenantIDFromFiber(c *fiber.Ctx) string {
	if c == nil {
		return ""
	}

	return GetTenantID(c.UserContext())
}

// GetTenantConfigFromFiber extracts the tenant configuration from a Fiber context.
// Returns nil if the context is nil or no tenant configuration is found.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    config := GetTenantConfigFromFiber(c)
//	    if config == nil {
//	        return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no tenant config"})
//	    }
//	    // Use config...
//	})
func GetTenantConfigFromFiber(c *fiber.Ctx) *TenantConfig {
	if c == nil {
		return nil
	}

	return GetTenantConfig(c.UserContext())
}

// GetTenantPGFromFiber extracts the PostgreSQL configuration from a Fiber context.
// Returns nil if the context is nil or no PostgreSQL configuration is found.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    pgConfig := GetTenantPGFromFiber(c)
//	    if pgConfig == nil {
//	        return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no database"})
//	    }
//	    // Use pgConfig...
//	})
func GetTenantPGFromFiber(c *fiber.Ctx) *PostgreSQLConfig {
	if c == nil {
		return nil
	}

	return GetTenantPostgreSQL(c.UserContext())
}

// GetTenantMongoFromFiber extracts the MongoDB configuration from a Fiber context.
// Returns nil if the context is nil or no MongoDB configuration is found.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    mongoConfig := GetTenantMongoFromFiber(c)
//	    if mongoConfig == nil {
//	        return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no database"})
//	    }
//	    // Use mongoConfig...
//	})
func GetTenantMongoFromFiber(c *fiber.Ctx) *MongoDBConfig {
	if c == nil {
		return nil
	}

	return GetTenantMongoDB(c.UserContext())
}

// GetTenantValkeyFromFiber extracts the Valkey configuration from a Fiber context.
// Returns nil if the context is nil or no Valkey configuration is found.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    valkeyConfig := GetTenantValkeyFromFiber(c)
//	    if valkeyConfig == nil {
//	        return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no cache"})
//	    }
//	    // Use valkeyConfig...
//	})
func GetTenantValkeyFromFiber(c *fiber.Ctx) *ValkeyConfig {
	if c == nil {
		return nil
	}

	return GetTenantValkey(c.UserContext())
}

// GetTenantRabbitMQFromFiber extracts the RabbitMQ configuration from a Fiber context.
// Returns nil if the context is nil or no RabbitMQ configuration is found.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    rabbitConfig := GetTenantRabbitMQFromFiber(c)
//	    if rabbitConfig == nil {
//	        return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no message queue"})
//	    }
//	    // Use rabbitConfig...
//	})
func GetTenantRabbitMQFromFiber(c *fiber.Ctx) *RabbitMQConfig {
	if c == nil {
		return nil
	}

	return GetTenantRabbitMQ(c.UserContext())
}

// GetTenantValkey retrieves the Valkey configuration from the context.
// Returns nil if not found.
func GetTenantValkey(ctx context.Context) *ValkeyConfig {
	if ctx == nil {
		return nil
	}

	value := ctx.Value(TenantValkeyContextKey)
	if value == nil {
		return nil
	}

	config, ok := value.(*ValkeyConfig)
	if !ok {
		return nil
	}

	return config
}

// GetTenantRabbitMQ retrieves the RabbitMQ configuration from the context.
// Returns nil if not found.
func GetTenantRabbitMQ(ctx context.Context) *RabbitMQConfig {
	if ctx == nil {
		return nil
	}

	value := ctx.Value(TenantRabbitMQContextKey)
	if value == nil {
		return nil
	}

	config, ok := value.(*RabbitMQConfig)
	if !ok {
		return nil
	}

	return config
}

// WithTenantID returns a new context with the tenant ID set.
// This is useful for testing and for propagating tenant context in workers.
//
// Example:
//
//	ctx := WithTenantID(context.Background(), "tenant-123")
//	tenantID := GetTenantID(ctx) // Returns "tenant-123"
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, TenantContextKey, tenantID)
}

// WithTenantConfig returns a new context with the tenant configuration set.
// This is useful for testing and for propagating tenant context in workers.
//
// Example:
//
//	config := &TenantConfig{ID: "tenant-123", Status: "active"}
//	ctx := WithTenantConfig(context.Background(), config)
//	retrieved := GetTenantConfig(ctx) // Returns config
func WithTenantConfig(ctx context.Context, config *TenantConfig) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, TenantConfigContextKey, config)
}

// WithTenantPG returns a new context with the PostgreSQL configuration set.
// This is useful for testing and for propagating tenant context in workers.
//
// Example:
//
//	pgConfig := &PostgreSQLConfig{Host: "localhost", Port: 5432}
//	ctx := WithTenantPG(context.Background(), pgConfig)
//	retrieved := GetTenantPostgreSQL(ctx) // Returns pgConfig
func WithTenantPG(ctx context.Context, config *PostgreSQLConfig) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, TenantPGContextKey, config)
}

// WithTenantMongo returns a new context with the MongoDB configuration set.
// This is useful for testing and for propagating tenant context in workers.
//
// Example:
//
//	mongoConfig := &MongoDBConfig{URI: "mongodb://localhost:27017", Database: "test"}
//	ctx := WithTenantMongo(context.Background(), mongoConfig)
//	retrieved := GetTenantMongoDB(ctx) // Returns mongoConfig
func WithTenantMongo(ctx context.Context, config *MongoDBConfig) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, TenantMongoContextKey, config)
}

// WithTenantValkey returns a new context with the Valkey configuration set.
// This is useful for testing and for propagating tenant context in workers.
//
// Example:
//
//	valkeyConfig := &ValkeyConfig{Addresses: []string{"localhost:6379"}}
//	ctx := WithTenantValkey(context.Background(), valkeyConfig)
//	retrieved := GetTenantValkey(ctx) // Returns valkeyConfig
func WithTenantValkey(ctx context.Context, config *ValkeyConfig) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, TenantValkeyContextKey, config)
}

// WithTenantRabbitMQ returns a new context with the RabbitMQ configuration set.
// This is useful for testing and for propagating tenant context in workers.
//
// Example:
//
//	rabbitConfig := &RabbitMQConfig{URL: "amqp://localhost:5672"}
//	ctx := WithTenantRabbitMQ(context.Background(), rabbitConfig)
//	retrieved := GetTenantRabbitMQ(ctx) // Returns rabbitConfig
func WithTenantRabbitMQ(ctx context.Context, config *RabbitMQConfig) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, TenantRabbitMQContextKey, config)
}

// WithAllTenantContext returns a new context with all tenant-related values set.
// This is a convenience function for testing scenarios where full tenant context is needed.
// Individual configs can be nil if not needed.
//
// Example:
//
//	ctx := WithAllTenantContext(
//	    context.Background(),
//	    "tenant-123",
//	    tenantConfig,
//	    pgConfig,
//	    mongoConfig,
//	    valkeyConfig,
//	    rabbitConfig,
//	)
func WithAllTenantContext(
	ctx context.Context,
	tenantID string,
	tenantConfig *TenantConfig,
	pgConfig *PostgreSQLConfig,
	mongoConfig *MongoDBConfig,
	valkeyConfig *ValkeyConfig,
	rabbitConfig *RabbitMQConfig,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	if tenantID != "" {
		ctx = WithTenantID(ctx, tenantID)
	}

	if tenantConfig != nil {
		ctx = WithTenantConfig(ctx, tenantConfig)
	}

	if pgConfig != nil {
		ctx = WithTenantPG(ctx, pgConfig)
	}

	if mongoConfig != nil {
		ctx = WithTenantMongo(ctx, mongoConfig)
	}

	if valkeyConfig != nil {
		ctx = WithTenantValkey(ctx, valkeyConfig)
	}

	if rabbitConfig != nil {
		ctx = WithTenantRabbitMQ(ctx, rabbitConfig)
	}

	return ctx
}

// GetTenantPGConnection retrieves the tenant-specific PostgreSQL connection from the context.
// Returns the connection and true if found, otherwise nil and false.
// This returns an actual database connection, not just configuration.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    conn, ok := GetTenantPGConnection(c.UserContext())
//	    if !ok {
//	        return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no database connection"})
//	    }
//	    // Use conn for database operations...
//	})
func GetTenantPGConnection(ctx context.Context) (dbresolver.DB, bool) {
	if ctx == nil {
		return nil, false
	}

	// Check the internal middleware key first
	if conn, ok := ctx.Value(tenantPGConnectionContextKey).(dbresolver.DB); ok {
		return conn, true
	}

	// Check the exported key for compatibility
	if conn, ok := ctx.Value(TenantPGConnectionContextKey).(dbresolver.DB); ok {
		return conn, true
	}

	return nil, false
}

// GetTenantMongoDatabase retrieves the tenant-specific MongoDB database from the context.
// Returns the TenantDatabase and true if found, otherwise nil and false.
// This returns an actual database handle with automatic collection prefixing for schema mode.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    db, ok := GetTenantMongoDatabase(c.UserContext())
//	    if !ok {
//	        return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no database connection"})
//	    }
//	    collection := db.Collection("users") // Auto-prefixed in schema mode
//	    // Use collection for database operations...
//	})
func GetTenantMongoDatabase(ctx context.Context) (*TenantDatabase, bool) {
	if ctx == nil {
		return nil, false
	}

	// Check the internal middleware key first
	if db, ok := ctx.Value(tenantMongoDBContextKeyConn).(*TenantDatabase); ok {
		return db, true
	}

	// Check the exported key for compatibility
	if db, ok := ctx.Value(TenantMongoDBConnectionContextKey).(*TenantDatabase); ok {
		return db, true
	}

	return nil, false
}

// GetTenantPGConnectionFromFiber extracts the tenant PostgreSQL connection from a Fiber context.
// Returns the connection and true if found, otherwise nil and false.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    conn, ok := GetTenantPGConnectionFromFiber(c)
//	    if !ok {
//	        // Fall back to config-based connection
//	    }
//	    // Use conn...
//	})
func GetTenantPGConnectionFromFiber(c *fiber.Ctx) (dbresolver.DB, bool) {
	if c == nil {
		return nil, false
	}

	return GetTenantPGConnection(c.UserContext())
}

// GetTenantMongoDatabaseFromFiber extracts the tenant MongoDB database from a Fiber context.
// Returns the TenantDatabase and true if found, otherwise nil and false.
//
// Example:
//
//	app.Get("/api/resource", func(c *fiber.Ctx) error {
//	    db, ok := GetTenantMongoDatabaseFromFiber(c)
//	    if !ok {
//	        // Fall back to config-based connection
//	    }
//	    // Use db...
//	})
func GetTenantMongoDatabaseFromFiber(c *fiber.Ctx) (*TenantDatabase, bool) {
	if c == nil {
		return nil, false
	}

	return GetTenantMongoDatabase(c.UserContext())
}

// WithTenantPGConnection returns a new context with the tenant PostgreSQL connection set.
// This is useful for testing and for propagating tenant connections in workers.
//
// Example:
//
//	ctx := WithTenantPGConnection(context.Background(), dbConn)
//	conn, _ := GetTenantPGConnection(ctx) // Returns dbConn
func WithTenantPGConnection(ctx context.Context, conn dbresolver.DB) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, TenantPGConnectionContextKey, conn)
}

// WithTenantMongoDatabase returns a new context with the tenant MongoDB database set.
// This is useful for testing and for propagating tenant connections in workers.
//
// Example:
//
//	ctx := WithTenantMongoDatabase(context.Background(), tenantDB)
//	db, _ := GetTenantMongoDatabase(ctx) // Returns tenantDB
func WithTenantMongoDatabase(ctx context.Context, db *TenantDatabase) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, TenantMongoDBConnectionContextKey, db)
}

// GetDBForTenant returns the appropriate database connection based on multi-tenant mode.
//
// When multi-tenant is ENABLED (tenant ID in context):
//   - Returns the tenant-specific connection from context
//   - Returns ERROR if tenant connection not found (NO fallback to default!)
//   - This prevents data being written to wrong database
//
// When multi-tenant is DISABLED (no tenant ID in context):
//   - Returns the default connection (single-tenant mode)
//
// Usage in repositories:
//
//	func (r *Repository) Create(ctx context.Context, entity *Entity) error {
//	    db, err := poolmanager.GetDBForTenant(ctx, r.connection)
//	    if err != nil {
//	        return err // Don't proceed without correct connection!
//	    }
//	    // Use db for queries...
//	}
func GetDBForTenant(ctx context.Context, defaultConn *libPostgres.PostgresConnection) (dbresolver.DB, error) {
	// Check if multi-tenant mode is enabled via context
	// The middleware sets this when processing requests
	if IsMultiTenantContext(ctx) {
		// Multi-tenant mode: MUST have tenant connection
		tenantConn, ok := GetTenantPGConnection(ctx)
		if !ok {
			return nil, ErrTenantConnectionRequired
		}

		return tenantConn, nil
	}

	// Single-tenant mode: use default connection
	if defaultConn == nil {
		return nil, ErrNoConnectionAvailable
	}

	return defaultConn.GetDB()
}

// IsMultiTenantContext checks if the request is in multi-tenant mode.
// This is set by the middleware when it processes a request with tenant context.
func IsMultiTenantContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	// If there's a tenant ID in context, we're in multi-tenant mode
	tenantID := GetTenantID(ctx)

	return tenantID != ""
}

// GetMongoDatabaseForTenant returns the appropriate MongoDB database based on multi-tenant mode.
//
// When multi-tenant is ENABLED (tenant ID in context):
//   - Returns the tenant-specific database from context
//   - Returns ERROR if tenant database not found (NO fallback!)
//
// When multi-tenant is DISABLED (no tenant ID in context):
//   - Returns the default database (single-tenant mode)
//
// Usage in repositories:
//
//	func (r *Repository) Create(ctx context.Context, entity *Entity) error {
//	    db, err := poolmanager.GetMongoDatabaseForTenant(ctx, r.connection)
//	    if err != nil {
//	        return err // Don't proceed without correct connection!
//	    }
//	    collection := db.Collection("mycollection")
//	    // Use collection for queries...
//	}
func GetMongoDatabaseForTenant(ctx context.Context, defaultConn *libMongo.MongoConnection) (*TenantDatabase, error) {
	if IsMultiTenantContext(ctx) {
		tenantDB, ok := GetTenantMongoDatabase(ctx)
		if !ok {
			return nil, ErrTenantConnectionRequired
		}

		return tenantDB, nil
	}

	// Single-tenant mode: wrap default connection
	if defaultConn == nil {
		return nil, ErrNoConnectionAvailable
	}

	db, err := defaultConn.GetDB(ctx)
	if err != nil {
		return nil, err
	}

	// Return wrapped database for consistent interface
	return &TenantDatabase{
		db:               db.Database(defaultConn.Database),
		collectionPrefix: "", // No prefix in single-tenant mode
	}, nil
}
