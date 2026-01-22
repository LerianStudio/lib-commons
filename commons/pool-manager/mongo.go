package poolmanager

import (
	"context"
	"fmt"
	"sync"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Context key for MongoDB
const tenantMongoKey contextKey = "tenantMongo"

// MongoPool manages MongoDB connections per tenant.
type MongoPool struct {
	client  *Client
	service string
	module  string

	mu     sync.RWMutex
	pools  map[string]*mongo.Client
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

// NewMongoPool creates a new MongoDB connection pool.
func NewMongoPool(client *Client, service string, opts ...MongoPoolOption) *MongoPool {
	p := &MongoPool{
		client:  client,
		service: service,
		pools:   make(map[string]*mongo.Client),
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

	if client, ok := p.pools[tenantID]; ok {
		p.mu.RUnlock()
		return client, nil
	}
	p.mu.RUnlock()

	return p.createClient(ctx, tenantID)
}

// createClient fetches config from Pool Manager and creates a MongoDB client.
func (p *MongoPool) createClient(ctx context.Context, tenantID string) (*mongo.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring lock
	if client, ok := p.pools[tenantID]; ok {
		return client, nil
	}

	if p.closed {
		return nil, ErrPoolClosed
	}

	// Fetch tenant config from Pool Manager
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

	// Create MongoDB client
	clientOpts := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Ping to verify connection
	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(ctx)
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	// Cache client
	p.pools[tenantID] = client

	return client, nil
}

// GetDatabase returns a MongoDB database for the tenant.
func (p *MongoPool) GetDatabase(ctx context.Context, tenantID, database string) (*mongo.Database, error) {
	client, err := p.GetClient(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return client.Database(database), nil
}

// Close closes all MongoDB connections.
func (p *MongoPool) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	var lastErr error
	for tenantID, client := range p.pools {
		if err := client.Disconnect(ctx); err != nil {
			lastErr = err
		}
		delete(p.pools, tenantID)
	}

	return lastErr
}

// CloseClient closes the MongoDB client for a specific tenant.
func (p *MongoPool) CloseClient(ctx context.Context, tenantID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	client, ok := p.pools[tenantID]
	if !ok {
		return nil
	}

	err := client.Disconnect(ctx)
	delete(p.pools, tenantID)

	return err
}

// buildMongoURI builds MongoDB connection URI from config.
func buildMongoURI(cfg *MongoDBConfig) string {
	if cfg.URI != "" {
		return cfg.URI
	}

	if cfg.Username != "" && cfg.Password != "" {
		return fmt.Sprintf("mongodb://%s:%s@%s:%d/%s",
			cfg.Username, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	}

	return fmt.Sprintf("mongodb://%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
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
// If no tenant connection is found in context, returns ErrConnectionNotFound.
// For single-tenant mode support, use GetMongoDatabaseForTenant instead.
func GetMongoForTenant(ctx context.Context) (*mongo.Database, error) {
	if db := GetMongoFromContext(ctx); db != nil {
		return db, nil
	}

	return nil, ErrConnectionNotFound
}

// GetMongoDatabaseForTenant returns the MongoDB database for the current tenant from context.
// If no tenant connection is found in context, falls back to the provided default connection.
// This supports both multi-tenant mode (using context) and single-tenant mode (using fallback).
func GetMongoDatabaseForTenant(ctx context.Context, defaultConn MongoConnectionInterface) (*mongo.Database, error) {
	if db := GetMongoFromContext(ctx); db != nil {
		return db, nil
	}

	if defaultConn != nil {
		client, err := defaultConn.GetDB(ctx)
		if err != nil {
			return nil, err
		}
		return client.Database(defaultConn.GetDatabaseName()), nil
	}

	return nil, ErrConnectionNotFound
}

// MongoConnectionInterface defines the interface for MongoDB connections.
// This allows the pool manager to work with different connection implementations.
type MongoConnectionInterface interface {
	GetDB(ctx context.Context) (*mongo.Client, error)
	GetDatabaseName() string
}
