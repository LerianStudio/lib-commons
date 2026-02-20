package tenantmanager

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	"github.com/LerianStudio/lib-commons/v2/commons/log"
	mongolib "github.com/LerianStudio/lib-commons/v2/commons/mongo"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	"go.mongodb.org/mongo-driver/mongo"
)

// mongoPingTimeout is the maximum duration for MongoDB connection health check pings.
const mongoPingTimeout = 3 * time.Second

// Context key for MongoDB
const tenantMongoKey contextKey = "tenantMongo"

// DefaultMongoMaxConnections is the default max connections for MongoDB.
const DefaultMongoMaxConnections uint64 = 100

// MongoManager manages MongoDB connections per tenant.
// Credentials are provided directly by the tenant-manager settings endpoint.
// When maxConnections is set (> 0), the manager uses LRU eviction with an idle
// timeout as a soft limit. Connections idle longer than the timeout are eligible
// for eviction when the pool exceeds maxConnections. If all connections are active
// (used within the idle timeout), the pool grows beyond the soft limit and
// naturally shrinks back as tenants become idle.
type MongoManager struct {
	client  *Client
	service string
	module  string
	logger  log.Logger

	mu             sync.RWMutex
	connections    map[string]*mongolib.MongoConnection
	closed         bool
	maxConnections int                  // soft limit for pool size (0 = unlimited)
	idleTimeout    time.Duration        // how long before a connection is eligible for eviction
	lastAccessed   map[string]time.Time // LRU tracking per tenant
}

// MongoOption configures a MongoManager.
type MongoOption func(*MongoManager)

// WithMongoModule sets the module name for the MongoDB manager.
func WithMongoModule(module string) MongoOption {
	return func(p *MongoManager) {
		p.module = module
	}
}

// WithMongoLogger sets the logger for the MongoDB manager.
func WithMongoLogger(logger log.Logger) MongoOption {
	return func(p *MongoManager) {
		p.logger = logger
	}
}

// WithMongoMaxTenantPools sets the soft limit for the number of tenant connections in the pool.
// When the pool reaches this limit and a new tenant needs a connection, only connections
// that have been idle longer than the idle timeout are eligible for eviction. If all
// connections are active (used within the idle timeout), the pool grows beyond this limit.
// A value of 0 (default) means unlimited.
func WithMongoMaxTenantPools(maxSize int) MongoOption {
	return func(p *MongoManager) {
		p.maxConnections = maxSize
	}
}

// WithMongoIdleTimeout sets the duration after which an unused tenant connection becomes
// eligible for eviction. Only connections idle longer than this duration will be evicted
// when the pool exceeds the soft limit (maxConnections). If all connections are active
// (used within the idle timeout), the pool is allowed to grow beyond the soft limit.
// Default: 5 minutes.
func WithMongoIdleTimeout(d time.Duration) MongoOption {
	return func(p *MongoManager) {
		p.idleTimeout = d
	}
}

// Deprecated: Use WithMongoMaxTenantPools instead.
func WithMongoMaxConnections(maxSize int) MongoOption { return WithMongoMaxTenantPools(maxSize) }

// NewMongoManager creates a new MongoDB connection manager.
func NewMongoManager(client *Client, service string, opts ...MongoOption) *MongoManager {
	p := &MongoManager{
		client:       client,
		service:      service,
		connections:  make(map[string]*mongolib.MongoConnection),
		lastAccessed: make(map[string]time.Time),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// GetClient returns a MongoDB client for the tenant.
// If a cached client fails a health check (e.g., due to credential rotation
// after a tenant purge+re-associate), the stale client is evicted and a new
// one is created with fresh credentials from the Tenant Manager.
func (p *MongoManager) GetClient(ctx context.Context, tenantID string) (*mongo.Client, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	p.mu.RLock()

	if p.closed {
		p.mu.RUnlock()
		return nil, ErrManagerClosed
	}

	if conn, ok := p.connections[tenantID]; ok {
		p.mu.RUnlock()

		// Validate cached connection is still healthy (e.g., credentials may have changed)
		if conn.DB != nil {
			pingCtx, cancel := context.WithTimeout(ctx, mongoPingTimeout)
			defer cancel()

			if pingErr := conn.DB.Ping(pingCtx, nil); pingErr != nil {
				if p.logger != nil {
					p.logger.Warnf("cached mongo connection unhealthy for tenant %s, reconnecting: %v", tenantID, pingErr)
				}

				_ = p.CloseClient(ctx, tenantID)

				// Fall through to create a new client with fresh credentials
				return p.createClient(ctx, tenantID)
			}
		}

		// Update LRU tracking on cache hit
		p.mu.Lock()
		p.lastAccessed[tenantID] = time.Now()
		p.mu.Unlock()

		return conn.DB, nil
	}

	p.mu.RUnlock()

	return p.createClient(ctx, tenantID)
}

// createClient fetches config from Tenant Manager and creates a MongoDB client.
func (p *MongoManager) createClient(ctx context.Context, tenantID string) (*mongo.Client, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "mongo.create_client")
	defer span.End()

	p.mu.Lock()

	// Double-check after acquiring lock: re-validate cached connection before returning
	if conn, ok := p.connections[tenantID]; ok {
		cached := conn
		p.mu.Unlock()

		if cached.DB != nil {
			pingCtx, cancel := context.WithTimeout(ctx, mongoPingTimeout)
			pingErr := cached.DB.Ping(pingCtx, nil)

			cancel()

			if pingErr == nil {
				return cached.DB, nil
			}

			if p.logger != nil {
				p.logger.Warnf("cached mongo connection unhealthy for tenant %s, reconnecting: %v", tenantID, pingErr)
			}
		}

		p.mu.Lock()
		delete(p.connections, tenantID)
		// fall through to create a fresh client
	}

	if p.closed {
		p.mu.Unlock()
		return nil, ErrManagerClosed
	}

	// Fetch tenant config from Tenant Manager
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		// Propagate TenantSuspendedError directly so callers (e.g., middleware)
		// can detect suspended/purged tenants without unwrapping generic messages.
		var suspErr *TenantSuspendedError
		if errors.As(err, &suspErr) {
			logger.Warnf("tenant service is %s: tenantID=%s", suspErr.Status, tenantID)
			libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "tenant service suspended", err)

			p.mu.Unlock()

			return nil, err
		}

		logger.Errorf("failed to get tenant config: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to get tenant config", err)
		p.mu.Unlock()

		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	// Get MongoDB config
	mongoConfig := config.GetMongoDBConfig(p.service, p.module)
	if mongoConfig == nil {
		logger.Errorf("no MongoDB config for tenant %s service %s module %s", tenantID, p.service, p.module)

		p.mu.Unlock()

		return nil, ErrServiceNotConfigured
	}

	// Build connection URI
	uri := buildMongoURI(mongoConfig)

	// Determine max connections (start with global default, then per-config, then per-tenant override)
	maxConnections := DefaultMongoMaxConnections
	if mongoConfig.MaxPoolSize > 0 {
		maxConnections = mongoConfig.MaxPoolSize
	}

	// Apply per-tenant connection pool settings from Tenant Manager (overrides all defaults)
	if config.ConnectionSettings != nil {
		if config.ConnectionSettings.MaxOpenConns > 0 {
			maxConnections = uint64(config.ConnectionSettings.MaxOpenConns)
			logger.Infof("applying per-tenant maxPoolSize=%d for tenant %s (mongo)", maxConnections, tenantID)
		}
	}

	// Create MongoConnection using lib-commons/commons/mongo pattern
	conn := &mongolib.MongoConnection{
		ConnectionStringSource: uri,
		Database:               mongoConfig.Database,
		Logger:                 p.logger,
		MaxPoolSize:            maxConnections,
	}

	// Connect to MongoDB (handles client creation and ping internally)
	if err := conn.Connect(ctx); err != nil {
		logger.Errorf("failed to connect to MongoDB for tenant %s: %v", tenantID, err)
		libOpentelemetry.HandleSpanError(&span, "failed to connect to MongoDB", err)
		p.mu.Unlock()

		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	logger.Infof("MongoDB connection created for tenant %s (database: %s)", tenantID, mongoConfig.Database)

	// Evict least recently used connection if pool is full

	p.evictLRU(ctx, logger)

	// Cache connection
	p.connections[tenantID] = conn
	p.lastAccessed[tenantID] = time.Now()

	p.mu.Unlock()

	return conn.DB, nil
}

// evictLRU removes the least recently used idle connection when the pool reaches the
// soft limit. Only connections that have been idle longer than the idle timeout are
// eligible for eviction. If all connections are active (used within the idle timeout),
// the pool is allowed to grow beyond the soft limit.
// Caller MUST hold p.mu write lock.
func (p *MongoManager) evictLRU(ctx context.Context, logger log.Logger) {
	if p.maxConnections <= 0 || len(p.connections) < p.maxConnections {
		return
	}

	now := time.Now()

	idleTimeout := p.idleTimeout
	if idleTimeout == 0 {
		idleTimeout = defaultIdleTimeout
	}

	// Find the oldest connection that has been idle longer than the timeout
	var oldestID string

	var oldestTime time.Time

	for id, t := range p.lastAccessed {
		idleDuration := now.Sub(t)
		if idleDuration < idleTimeout {
			continue // still active, skip
		}

		if oldestID == "" || t.Before(oldestTime) {
			oldestID = id
			oldestTime = t
		}
	}

	if oldestID == "" {
		// All connections are active (used within idle timeout)
		// Allow pool to grow beyond soft limit
		return
	}

	// Evict the idle connection
	if conn, ok := p.connections[oldestID]; ok {
		if conn.DB != nil {
			_ = conn.DB.Disconnect(ctx)
		}

		delete(p.connections, oldestID)
		delete(p.lastAccessed, oldestID)

		if logger != nil {
			logger.Infof("LRU evicted idle mongo connection for tenant %s (idle for %s)", oldestID, now.Sub(oldestTime))
		}
	}
}

// ApplyConnectionSettings checks if connection pool settings have changed for the
// given tenant. Unlike PostgreSQL, the MongoDB Go driver does not support changing
// pool size (maxPoolSize) after client creation. If settings differ, a warning is
// logged indicating that changes will take effect on the next connection recreation
// (e.g., after eviction or health check failure).
func (p *MongoManager) ApplyConnectionSettings(tenantID string, config *TenantConfig) {
	p.mu.RLock()
	_, ok := p.connections[tenantID]
	p.mu.RUnlock()

	if !ok {
		return // no cached connection, settings will be applied on creation
	}

	// Check if connection settings exist in the config
	var hasSettings bool

	if config.ConnectionSettings != nil && config.ConnectionSettings.MaxOpenConns > 0 {
		hasSettings = true
	}

	if config.Databases != nil && p.module != "" {
		if db, ok := config.Databases[p.module]; ok && db.ConnectionSettings != nil {
			if db.ConnectionSettings.MaxOpenConns > 0 {
				hasSettings = true
			}
		}
	}

	if hasSettings && p.logger != nil {
		p.logger.Warnf("MongoDB connection settings changed for tenant %s, "+
			"but MongoDB driver does not support pool resize after creation. "+
			"Changes will take effect on next connection recreation.", tenantID)
	}
}

// GetDatabase returns a MongoDB database for the tenant.
func (p *MongoManager) GetDatabase(ctx context.Context, tenantID, database string) (*mongo.Database, error) {
	client, err := p.GetClient(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return client.Database(database), nil
}

// GetDatabaseForTenant returns the MongoDB database for a tenant by fetching the config
// and resolving the database name automatically. This is useful when you only have the
// tenant ID and don't know the database name in advance.
// It fetches the config once and reuses it, avoiding a redundant GetTenantConfig call
// inside GetClient/createClient.
func (p *MongoManager) GetDatabaseForTenant(ctx context.Context, tenantID string) (*mongo.Database, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	// GetClient handles config fetching internally, so we only need
	// the config here to resolve the database name.
	client, err := p.GetClient(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Fetch tenant config to resolve the database name.
	// GetClient already cached the connection, so this is just for the DB name.
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		// Propagate TenantSuspendedError directly so the middleware can
		// return a specific 403 response instead of a generic 503.
		if IsTenantSuspendedError(err) {
			return nil, err
		}

		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	// Get MongoDB config which has the database name
	mongoConfig := config.GetMongoDBConfig(p.service, p.module)
	if mongoConfig == nil {
		return nil, ErrServiceNotConfigured
	}

	return client.Database(mongoConfig.Database), nil
}

// Close closes all MongoDB connections.
func (p *MongoManager) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	var errs []error

	for tenantID, conn := range p.connections {
		if conn.DB != nil {
			if err := conn.DB.Disconnect(ctx); err != nil {
				errs = append(errs, err)
			}
		}

		delete(p.connections, tenantID)
		delete(p.lastAccessed, tenantID)
	}

	return errors.Join(errs...)
}

// CloseClient closes the MongoDB client for a specific tenant.
func (p *MongoManager) CloseClient(ctx context.Context, tenantID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	conn, ok := p.connections[tenantID]
	if !ok {
		return nil
	}

	var err error

	if conn.DB != nil {
		err = conn.DB.Disconnect(ctx)
	}

	delete(p.connections, tenantID)
	delete(p.lastAccessed, tenantID)

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
			url.QueryEscape(cfg.Username), url.QueryEscape(cfg.Password),
			cfg.Host, cfg.Port, cfg.Database)

		if len(params) > 0 {
			uri += "?" + strings.Join(params, "&")
		}

		return uri
	}

	uri := fmt.Sprintf("mongodb://%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
	if len(params) > 0 {
		uri += "?" + strings.Join(params, "&")
	}

	return uri
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
