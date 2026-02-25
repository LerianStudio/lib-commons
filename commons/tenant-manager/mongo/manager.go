// Package mongo provides multi-tenant MongoDB connection management.
// It fetches tenant-specific database credentials from Tenant Manager service
// and manages connections per tenant using LRU eviction with idle timeout.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v3/commons"
	"github.com/LerianStudio/lib-commons/v3/commons/log"
	mongolib "github.com/LerianStudio/lib-commons/v3/commons/mongo"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v3/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/core"
	"go.mongodb.org/mongo-driver/mongo"
)

// mongoPingTimeout is the maximum duration for MongoDB connection health check pings.
const mongoPingTimeout = 3 * time.Second

// DefaultMaxConnections is the default max connections for MongoDB.
const DefaultMaxConnections uint64 = 100

// defaultIdleTimeout is the default duration before a tenant connection becomes
// eligible for eviction. Connections accessed within this window are considered
// active and will not be evicted, allowing the pool to grow beyond maxConnections.
const defaultIdleTimeout = 5 * time.Minute

// Stats contains statistics for the Manager.
type Stats struct {
	TotalConnections  int      `json:"totalConnections"`
	MaxConnections    int      `json:"maxConnections"`
	ActiveConnections int      `json:"activeConnections"`
	TenantIDs         []string `json:"tenantIds"`
	Closed            bool     `json:"closed"`
}

// Manager manages MongoDB connections per tenant.
// Credentials are provided directly by the tenant-manager settings endpoint.
// When maxConnections is set (> 0), the manager uses LRU eviction with an idle
// timeout as a soft limit. Connections idle longer than the timeout are eligible
// for eviction when the pool exceeds maxConnections. If all connections are active
// (used within the idle timeout), the pool grows beyond the soft limit and
// naturally shrinks back as tenants become idle.
type Manager struct {
	client  *client.Client
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

// Option configures a Manager.
type Option func(*Manager)

// WithModule sets the module name for the MongoDB manager.
func WithModule(module string) Option {
	return func(p *Manager) {
		p.module = module
	}
}

// WithLogger sets the logger for the MongoDB manager.
func WithLogger(logger log.Logger) Option {
	return func(p *Manager) {
		p.logger = logger
	}
}

// WithMaxTenantPools sets the soft limit for the number of tenant connections in the pool.
// When the pool reaches this limit and a new tenant needs a connection, only connections
// that have been idle longer than the idle timeout are eligible for eviction. If all
// connections are active (used within the idle timeout), the pool grows beyond this limit.
// A value of 0 (default) means unlimited.
func WithMaxTenantPools(maxSize int) Option {
	return func(p *Manager) {
		p.maxConnections = maxSize
	}
}

// WithIdleTimeout sets the duration after which an unused tenant connection becomes
// eligible for eviction. Only connections idle longer than this duration will be evicted
// when the pool exceeds the soft limit (maxConnections). If all connections are active
// (used within the idle timeout), the pool is allowed to grow beyond the soft limit.
// Default: 5 minutes.
func WithIdleTimeout(d time.Duration) Option {
	return func(p *Manager) {
		p.idleTimeout = d
	}
}

// NewManager creates a new MongoDB connection manager.
func NewManager(c *client.Client, service string, opts ...Option) *Manager {
	p := &Manager{
		client:       c,
		service:      service,
		connections:  make(map[string]*mongolib.MongoConnection),
		lastAccessed: make(map[string]time.Time),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// GetConnection returns a MongoDB client for the tenant.
// If a cached client fails a health check (e.g., due to credential rotation
// after a tenant purge+re-associate), the stale client is evicted and a new
// one is created with fresh credentials from the Tenant Manager.
func (p *Manager) GetConnection(ctx context.Context, tenantID string) (*mongo.Client, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	p.mu.RLock()

	if p.closed {
		p.mu.RUnlock()
		return nil, core.ErrManagerClosed
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

				_ = p.CloseConnection(ctx, tenantID)

				// Fall through to create a new client with fresh credentials
				return p.createConnection(ctx, tenantID)
			}
		}

		// Update LRU tracking on cache hit
		p.mu.Lock()
		p.lastAccessed[tenantID] = time.Now()
		p.mu.Unlock()

		return conn.DB, nil
	}

	p.mu.RUnlock()

	return p.createConnection(ctx, tenantID)
}

// createConnection fetches config from Tenant Manager and creates a MongoDB client.
func (p *Manager) createConnection(ctx context.Context, tenantID string) (*mongo.Client, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "mongo.create_connection")
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
		return nil, core.ErrManagerClosed
	}

	// Fetch tenant config from Tenant Manager
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		// Propagate TenantSuspendedError directly so callers (e.g., middleware)
		// can detect suspended/purged tenants without unwrapping generic messages.
		var suspErr *core.TenantSuspendedError
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

		return nil, core.ErrServiceNotConfigured
	}

	// Build connection URI
	uri := buildMongoURI(mongoConfig)

	// Determine max connections: global default, optionally overridden by MongoDBConfig.MaxPoolSize.
	// Per-tenant ConnectionSettings are NOT applied for MongoDB because the Go driver does not
	// support changing maxPoolSize after client creation. Per-tenant pool sizing is PostgreSQL-only.
	maxConnections := DefaultMaxConnections
	if mongoConfig.MaxPoolSize > 0 {
		maxConnections = mongoConfig.MaxPoolSize
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
func (p *Manager) evictLRU(ctx context.Context, logger log.Logger) {
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

// ApplyConnectionSettings is a no-op for MongoDB. The MongoDB Go driver does not
// support changing maxPoolSize after client creation. All MongoDB connections use
// the global default pool size (DefaultMaxConnections or MongoDBConfig.MaxPoolSize).
// Per-tenant pool sizing is only supported for PostgreSQL via SetMaxOpenConns.
func (p *Manager) ApplyConnectionSettings(tenantID string, config *core.TenantConfig) {
	// No-op: MongoDB driver does not support runtime pool resize.
	// Pool size is determined at connection creation time and remains fixed.
}

// GetDatabase returns a MongoDB database for the tenant.
func (p *Manager) GetDatabase(ctx context.Context, tenantID, database string) (*mongo.Database, error) {
	mongoClient, err := p.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return mongoClient.Database(database), nil
}

// GetDatabaseForTenant returns the MongoDB database for a tenant by fetching the config
// and resolving the database name automatically. This is useful when you only have the
// tenant ID and don't know the database name in advance.
// It fetches the config once and reuses it, avoiding a redundant GetTenantConfig call
// inside GetConnection/createConnection.
func (p *Manager) GetDatabaseForTenant(ctx context.Context, tenantID string) (*mongo.Database, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	// GetConnection handles config fetching internally, so we only need
	// the config here to resolve the database name.
	mongoClient, err := p.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Fetch tenant config to resolve the database name.
	// GetConnection already cached the connection, so this is just for the DB name.
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		// Propagate TenantSuspendedError directly so the middleware can
		// return a specific 403 response instead of a generic 503.
		if core.IsTenantSuspendedError(err) {
			return nil, err
		}

		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	// Get MongoDB config which has the database name
	mongoConfig := config.GetMongoDBConfig(p.service, p.module)
	if mongoConfig == nil {
		return nil, core.ErrServiceNotConfigured
	}

	return mongoClient.Database(mongoConfig.Database), nil
}

// Close closes all MongoDB connections.
func (p *Manager) Close(ctx context.Context) error {
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

// CloseConnection closes the MongoDB client for a specific tenant.
func (p *Manager) CloseConnection(ctx context.Context, tenantID string) error {
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

// Stats returns connection statistics.
func (p *Manager) Stats() Stats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	tenantIDs := make([]string, 0, len(p.connections))

	activeCount := 0
	now := time.Now()

	idleTimeout := p.idleTimeout
	if idleTimeout == 0 {
		idleTimeout = defaultIdleTimeout
	}

	for id := range p.connections {
		tenantIDs = append(tenantIDs, id)

		if t, ok := p.lastAccessed[id]; ok && now.Sub(t) < idleTimeout {
			activeCount++
		}
	}

	return Stats{
		TotalConnections:  len(p.connections),
		MaxConnections:    p.maxConnections,
		ActiveConnections: activeCount,
		TenantIDs:         tenantIDs,
		Closed:            p.closed,
	}
}

// IsMultiTenant returns true if the manager is configured with a Tenant Manager client.
func (p *Manager) IsMultiTenant() bool {
	return p.client != nil
}

// buildMongoURI builds MongoDB connection URI from config.
func buildMongoURI(cfg *core.MongoDBConfig) string {
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
