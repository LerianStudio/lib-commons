package tenantmanager

import (
	"context"
	"fmt"
	"sync"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	mongolib "github.com/LerianStudio/lib-commons/v2/commons/mongo"
	"go.mongodb.org/mongo-driver/mongo"
)

// Context key for MongoDB
const tenantMongoKey contextKey = "tenantMongo"

// Module-specific MongoDB connection keys for multi-tenant unified mode.
// These keys allow each module to have its own MongoDB connection in context,
// solving the issue where in-process calls between modules would get the wrong connection.
const (
	// tenantOnboardingMongoKey is the context key for storing the onboarding module's MongoDB connection.
	tenantOnboardingMongoKey contextKey = "tenantOnboardingMongo"
	// tenantTransactionMongoKey is the context key for storing the transaction module's MongoDB connection.
	tenantTransactionMongoKey contextKey = "tenantTransactionMongo"
)

// DefaultMongoMaxPoolSize is the default max pool size for MongoDB connections.
const DefaultMongoMaxPoolSize uint64 = 100

// MongoPool manages MongoDB connections per tenant.
type MongoPool struct {
	client  *Client
	service string
	module  string
	logger  log.Logger

	mu     sync.RWMutex
	pools  map[string]*mongolib.MongoConnection
	closed bool
}

// MongoPoolOption configures a MongoPool.
type MongoPoolOption func(*MongoPool)

// WithMongoModule sets the module name for the MongoDB pool.
func WithMongoModule(module string) MongoPoolOption {
	return func(p *MongoPool) {
		p.module = module
	}
}

// WithMongoLogger sets the logger for the MongoDB pool.
func WithMongoLogger(logger log.Logger) MongoPoolOption {
	return func(p *MongoPool) {
		p.logger = logger
	}
}

// NewMongoPool creates a new MongoDB connection pool.
func NewMongoPool(client *Client, service string, opts ...MongoPoolOption) *MongoPool {
	p := &MongoPool{
		client:  client,
		service: service,
		pools:   make(map[string]*mongolib.MongoConnection),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// GetClient returns a MongoDB client for the tenant.
func (p *MongoPool) GetClient(ctx context.Context, tenantID string) (*mongo.Client, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return nil, ErrPoolClosed
	}

	if conn, ok := p.pools[tenantID]; ok {
		p.mu.RUnlock()
		return conn.DB, nil
	}
	p.mu.RUnlock()

	return p.createClient(ctx, tenantID)
}

// createClient fetches config from Tenant Manager and creates a MongoDB client.
func (p *MongoPool) createClient(ctx context.Context, tenantID string) (*mongo.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring lock
	if conn, ok := p.pools[tenantID]; ok {
		return conn.DB, nil
	}

	if p.closed {
		return nil, ErrPoolClosed
	}

	// Fetch tenant config from Tenant Manager
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	// Get MongoDB config
	mongoConfig := config.GetMongoDBConfig(p.service, p.module)
	if mongoConfig == nil {
		return nil, ErrServiceNotConfigured
	}

	// Build connection URI
	uri := buildMongoURI(mongoConfig)

	// Determine max pool size
	maxPoolSize := DefaultMongoMaxPoolSize
	if mongoConfig.MaxPoolSize > 0 {
		maxPoolSize = mongoConfig.MaxPoolSize
	}

	// Create MongoConnection using lib-commons/commons/mongo pattern
	conn := &mongolib.MongoConnection{
		ConnectionStringSource: uri,
		Database:               mongoConfig.Database,
		Logger:                 p.logger,
		MaxPoolSize:            maxPoolSize,
	}

	// Connect to MongoDB (handles client creation and ping internally)
	if err := conn.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Cache connection
	p.pools[tenantID] = conn

	return conn.DB, nil
}

// GetDatabase returns a MongoDB database for the tenant.
func (p *MongoPool) GetDatabase(ctx context.Context, tenantID, database string) (*mongo.Database, error) {
	client, err := p.GetClient(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return client.Database(database), nil
}

// GetDatabaseForTenant returns the MongoDB database for a tenant by fetching the config
// and resolving the database name automatically. This is useful when you only have the
// tenant ID and don't know the database name in advance.
func (p *MongoPool) GetDatabaseForTenant(ctx context.Context, tenantID string) (*mongo.Database, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	// Fetch tenant config from Tenant Manager
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	// Get MongoDB config which has the database name
	mongoConfig := config.GetMongoDBConfig(p.service, p.module)
	if mongoConfig == nil {
		return nil, ErrServiceNotConfigured
	}

	return p.GetDatabase(ctx, tenantID, mongoConfig.Database)
}

// Close closes all MongoDB connections.
func (p *MongoPool) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	var lastErr error
	for tenantID, conn := range p.pools {
		if conn.DB != nil {
			if err := conn.DB.Disconnect(ctx); err != nil {
				lastErr = err
			}
		}
		delete(p.pools, tenantID)
	}

	return lastErr
}

// CloseClient closes the MongoDB client for a specific tenant.
func (p *MongoPool) CloseClient(ctx context.Context, tenantID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	conn, ok := p.pools[tenantID]
	if !ok {
		return nil
	}

	var err error
	if conn.DB != nil {
		err = conn.DB.Disconnect(ctx)
	}
	delete(p.pools, tenantID)

	return err
}

// buildMongoURI builds MongoDB connection URI from config.
func buildMongoURI(cfg *MongoDBConfig) string {
	if cfg.URI != "" {
		return cfg.URI
	}

	var params []string

	// Add authSource only if explicitly configured in secrets
	if cfg.AuthSource != "" {
		params = append(params, "authSource="+cfg.AuthSource)
	}

	// Add directConnection for single-node replica sets where the server's
	// self-reported hostname may differ from the connection hostname
	if cfg.DirectConnection {
		params = append(params, "directConnection=true")
	}

	if cfg.Username != "" && cfg.Password != "" {
		uri := fmt.Sprintf("mongodb://%s:%s@%s:%d/%s",
			cfg.Username, cfg.Password, cfg.Host, cfg.Port, cfg.Database)

		if len(params) > 0 {
			uri += "?" + joinParams(params)
		}

		return uri
	}

	uri := fmt.Sprintf("mongodb://%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
	if len(params) > 0 {
		uri += "?" + joinParams(params)
	}

	return uri
}

// joinParams joins URI parameters with &
func joinParams(params []string) string {
	result := ""
	for i, p := range params {
		if i > 0 {
			result += "&"
		}
		result += p
	}
	return result
}

// ContextWithTenantMongo stores the MongoDB database in the context.
func ContextWithTenantMongo(ctx context.Context, db *mongo.Database) context.Context {
	return context.WithValue(ctx, tenantMongoKey, db)
}

// GetMongoFromContext retrieves the MongoDB database from the context.
// Returns nil if not found.
func GetMongoFromContext(ctx context.Context) *mongo.Database {
	if db, ok := ctx.Value(tenantMongoKey).(*mongo.Database); ok {
		return db
	}
	return nil
}

// GetMongoForTenant returns the MongoDB database for the current tenant from context.
// If no tenant connection is found in context, returns ErrTenantContextRequired.
// This function ALWAYS requires tenant context - there is no fallback to default connections.
func GetMongoForTenant(ctx context.Context) (*mongo.Database, error) {
	if db := GetMongoFromContext(ctx); db != nil {
		return db, nil
	}

	return nil, ErrTenantContextRequired
}

// ContextWithOnboardingMongo stores the onboarding module's MongoDB connection in context.
// This is used in multi-tenant unified mode where multiple modules run in the same process
// and each module needs its own MongoDB connection.
func ContextWithOnboardingMongo(ctx context.Context, db *mongo.Database) context.Context {
	return context.WithValue(ctx, tenantOnboardingMongoKey, db)
}

// ContextWithTransactionMongo stores the transaction module's MongoDB connection in context.
// This is used in multi-tenant unified mode where multiple modules run in the same process
// and each module needs its own MongoDB connection.
func ContextWithTransactionMongo(ctx context.Context, db *mongo.Database) context.Context {
	return context.WithValue(ctx, tenantTransactionMongoKey, db)
}

// GetOnboardingMongoForTenant returns the onboarding MongoDB connection from context.
// Returns ErrTenantContextRequired if not found.
// This function does NOT fallback to the generic tenantMongoKey - it strictly returns
// only the module-specific connection. This ensures proper isolation in multi-tenant unified mode.
func GetOnboardingMongoForTenant(ctx context.Context) (*mongo.Database, error) {
	if db, ok := ctx.Value(tenantOnboardingMongoKey).(*mongo.Database); ok && db != nil {
		return db, nil
	}

	return nil, ErrTenantContextRequired
}

// GetTransactionMongoForTenant returns the transaction MongoDB connection from context.
// Returns ErrTenantContextRequired if not found.
// This function does NOT fallback to the generic tenantMongoKey - it strictly returns
// only the module-specific connection. This ensures proper isolation in multi-tenant unified mode.
func GetTransactionMongoForTenant(ctx context.Context) (*mongo.Database, error) {
	if db, ok := ctx.Value(tenantTransactionMongoKey).(*mongo.Database); ok && db != nil {
		return db, nil
	}

	return nil, ErrTenantContextRequired
}
