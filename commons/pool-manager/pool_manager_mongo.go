package poolmanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	libMongo "github.com/LerianStudio/lib-commons/v2/commons/mongo"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoPoolManager defines the interface for managing MongoDB client pools
// across multiple tenants with support for different isolation modes.
// It leverages the existing commons/mongo infrastructure for connection management.
type MongoPoolManager interface {
	// GetClient returns a MongoDB client for the specified tenant and application.
	// In database mode: returns a dedicated client per tenant.
	// In schema mode: returns a shared client (multiple tenants share the same client).
	GetClient(ctx context.Context, tenantID, applicationName string) (*mongo.Client, error)

	// GetDatabase returns a TenantDatabase for the specified tenant and application.
	// TenantDatabase wraps mongo.Database and applies collection prefix for schema mode.
	// In database mode: collection prefix is empty (no prefixing).
	// In schema mode: collection prefix is "tenant_{sanitized_tenant_id}_".
	GetDatabase(ctx context.Context, tenantID, applicationName string) (*TenantDatabase, error)

	// GetDefaultConnection returns the default (single-tenant) MongoConnection.
	// Use this when operating in single-tenant mode.
	GetDefaultConnection() *libMongo.MongoConnection

	// CloseClient closes the MongoDB client for a specific tenant and application.
	CloseClient(tenantID, applicationName string) error

	// CloseAll closes all managed MongoDB clients and stops background cleanup.
	CloseAll(ctx context.Context) error

	// Stats returns statistics for all managed clients.
	Stats() map[string]MongoPoolStats
}

// MongoPoolStats contains statistics for a MongoDB client.
type MongoPoolStats struct {
	TenantID        string
	ApplicationName string
	IsolationMode   string
	CreatedAt       time.Time
	LastUsedAt      time.Time
	DatabaseName    string
}

// TenantDatabase wraps mongo.Database and prefixes collection names for schema isolation mode.
// This provides transparent collection prefixing without changing application code.
type TenantDatabase struct {
	db               *mongo.Database
	tenantID         string
	collectionPrefix string
}

// Collection returns a handle for a collection with the tenant prefix applied.
// In database mode (empty prefix): returns collection as-is.
// In schema mode: returns collection with "tenant_{id}_" prefix.
func (td *TenantDatabase) Collection(name string, opts ...*options.CollectionOptions) *mongo.Collection {
	prefixedName := td.getPrefixedName(name)
	if td.db == nil {
		return nil
	}

	return td.db.Collection(prefixedName, opts...)
}

// getPrefixedName returns the collection name with the tenant prefix applied.
func (td *TenantDatabase) getPrefixedName(name string) string {
	if td.collectionPrefix == "" {
		return name
	}

	return td.collectionPrefix + name
}

// Database returns the underlying mongo.Database pointer.
func (td *TenantDatabase) Database() *mongo.Database {
	return td.db
}

// TenantID returns the tenant ID associated with this database.
func (td *TenantDatabase) TenantID() string {
	return td.tenantID
}

// Prefix returns the collection prefix for this tenant database.
func (td *TenantDatabase) Prefix() string {
	return td.collectionPrefix
}

// MongoPoolManagerConfig holds configuration for the pool manager.
type MongoPoolManagerConfig struct {
	// DefaultConnection is the default MongoConnection for single-tenant/schema mode.
	// Required for schema mode, optional for database-only mode.
	DefaultConnection *libMongo.MongoConnection

	// Resolver is the tenant resolver for fetching tenant configurations.
	// Required.
	Resolver Resolver

	// Logger is the logger instance.
	// Required.
	Logger libLog.Logger

	// MaxConnections is the maximum number of tenant connections to maintain.
	// Default: 100
	MaxConnections int

	// IdleTimeout is the duration after which idle connections are closed.
	// Default: 30 minutes
	IdleTimeout time.Duration

	// CleanupInterval is the interval for the background cleanup goroutine.
	// Default: 5 minutes
	CleanupInterval time.Duration

	// MaxPoolSize sets the maximum number of connections in each MongoDB pool.
	// Default: 100
	MaxPoolSize uint64
}

// mongoConnEntry holds a MongoConnection along with metadata.
type mongoConnEntry struct {
	conn          *libMongo.MongoConnection
	tenantID      string
	appName       string
	isolationMode string
	databaseName  string
	createdAt     time.Time
	lastUsedAt    time.Time
	mu            sync.RWMutex
}

func (e *mongoConnEntry) updateLastUsed() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.lastUsedAt = time.Now()
}

func (e *mongoConnEntry) getLastUsed() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.lastUsedAt
}

// mongoPoolManagerImpl is the default implementation of MongoPoolManager.
type mongoPoolManagerImpl struct {
	defaultConn     *libMongo.MongoConnection
	resolver        Resolver
	logger          libLog.Logger
	maxConns        int
	idleTimeout     time.Duration
	cleanupInterval time.Duration
	maxPoolSize     uint64

	// tenantConns stores MongoConnection instances keyed by "tenantID:appName" for database mode.
	tenantConns map[string]*mongoConnEntry

	// sharedConns stores MongoConnection instances keyed by URI for schema mode.
	sharedConns map[string]*mongoConnEntry

	// tenantToSharedConn maps "tenantID:appName" to URI for schema mode.
	tenantToSharedConn map[string]string

	mu sync.RWMutex

	// cleanup goroutine control
	cleanupStop chan struct{}
	cleanupDone chan struct{}

	// ctx is the context for the cleanup goroutine lifecycle
	ctx context.Context
}

// MongoPoolManagerOption is a function that configures a MongoPoolManager.
type MongoPoolManagerOption func(*mongoPoolManagerImpl)

// WithMongoMaxClients sets the maximum number of MongoDB clients.
// Default is 100.
func WithMongoMaxClients(maxClients int) MongoPoolManagerOption {
	return func(pm *mongoPoolManagerImpl) {
		if maxClients > 0 {
			pm.maxConns = maxClients
		}
	}
}

// WithMongoIdleTimeout sets the duration after which idle clients are closed.
// Default is 30 minutes.
func WithMongoIdleTimeout(timeout time.Duration) MongoPoolManagerOption {
	return func(pm *mongoPoolManagerImpl) {
		if timeout > 0 {
			pm.idleTimeout = timeout
		}
	}
}

// WithMongoCleanupInterval sets the interval for the background cleanup goroutine.
// Default is 5 minutes.
func WithMongoCleanupInterval(interval time.Duration) MongoPoolManagerOption {
	return func(pm *mongoPoolManagerImpl) {
		if interval > 0 {
			pm.cleanupInterval = interval
		}
	}
}

// WithMongoDefaultConnection sets the default MongoConnection for schema mode.
func WithMongoDefaultConnection(conn *libMongo.MongoConnection) MongoPoolManagerOption {
	return func(pm *mongoPoolManagerImpl) {
		pm.defaultConn = conn
	}
}

// WithMongoLogger sets the logger for the pool manager.
func WithMongoLogger(logger libLog.Logger) MongoPoolManagerOption {
	return func(pm *mongoPoolManagerImpl) {
		pm.logger = logger
	}
}

// WithMongoMaxPoolSize sets the maximum pool size for new MongoDB connections.
// Default is 100.
func WithMongoMaxPoolSize(size uint64) MongoPoolManagerOption {
	return func(pm *mongoPoolManagerImpl) {
		if size > 0 {
			pm.maxPoolSize = size
		}
	}
}

// WithMongoContext sets the context for the pool manager lifecycle.
// When the context is cancelled, the cleanup goroutine will stop.
// If not set, the cleanup goroutine will only stop when CloseAll is called.
func WithMongoContext(ctx context.Context) MongoPoolManagerOption {
	return func(pm *mongoPoolManagerImpl) {
		if ctx != nil {
			pm.ctx = ctx
		}
	}
}

// NewMongoPoolManager creates a new MongoPoolManager with the given resolver and options.
func NewMongoPoolManager(resolver Resolver, opts ...MongoPoolManagerOption) MongoPoolManager {
	pm := &mongoPoolManagerImpl{
		resolver:           resolver,
		maxConns:           100,
		idleTimeout:        30 * time.Minute,
		cleanupInterval:    5 * time.Minute,
		maxPoolSize:        100,
		tenantConns:        make(map[string]*mongoConnEntry),
		sharedConns:        make(map[string]*mongoConnEntry),
		tenantToSharedConn: make(map[string]string),
		cleanupStop:        make(chan struct{}),
		cleanupDone:        make(chan struct{}),
	}

	for _, opt := range opts {
		opt(pm)
	}

	// Start background cleanup goroutine
	go pm.cleanupIdleConns()

	return pm
}

// NewMongoPoolManagerWithConfig creates a new MongoPoolManager with explicit configuration.
// This is the recommended constructor for production use.
func NewMongoPoolManagerWithConfig(cfg MongoPoolManagerConfig) (MongoPoolManager, error) {
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("resolver is required")
	}

	if cfg.Logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	// Apply defaults
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = 100
	}

	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}

	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	if cfg.MaxPoolSize <= 0 {
		cfg.MaxPoolSize = 100
	}

	pm := &mongoPoolManagerImpl{
		defaultConn:        cfg.DefaultConnection,
		resolver:           cfg.Resolver,
		logger:             cfg.Logger,
		maxConns:           cfg.MaxConnections,
		idleTimeout:        cfg.IdleTimeout,
		cleanupInterval:    cfg.CleanupInterval,
		maxPoolSize:        cfg.MaxPoolSize,
		tenantConns:        make(map[string]*mongoConnEntry),
		sharedConns:        make(map[string]*mongoConnEntry),
		tenantToSharedConn: make(map[string]string),
		cleanupStop:        make(chan struct{}),
		cleanupDone:        make(chan struct{}),
	}

	// Start background cleanup goroutine
	go pm.cleanupIdleConns()

	return pm, nil
}

// GetClient returns a MongoDB client for the specified tenant and application.
func (pm *mongoPoolManagerImpl) GetClient(ctx context.Context, tenantID, applicationName string) (*mongo.Client, error) {
	// Validate inputs
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: empty tenant ID", ErrInvalidTenantID)
	}

	if strings.TrimSpace(applicationName) == "" {
		return nil, fmt.Errorf("application name is required")
	}

	// Check context cancellation
	if ctx.Err() != nil {
		return nil, fmt.Errorf("context error: %w", ctx.Err())
	}

	// Resolve tenant configuration with service filter to get database credentials
	config, err := pm.resolver.ResolveWithService(ctx, tenantID, applicationName)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve tenant config: %w", err)
	}

	// Validate tenant status
	if config.Status != "active" {
		return nil, fmt.Errorf("%w: tenant status is %s", ErrTenantInactive, config.Status)
	}

	// Get database configuration for the application
	dbServices, ok := config.Databases[applicationName]
	if !ok || dbServices.MongoDB == nil {
		return nil, fmt.Errorf("MongoDB config not found for application %s", applicationName)
	}

	mongoConfig := dbServices.MongoDB

	// Route based on isolation mode
	switch config.IsolationMode {
	case "database":
		return pm.getClientDatabaseMode(ctx, tenantID, applicationName, mongoConfig)
	case "schema":
		return pm.getClientSchemaMode(ctx, tenantID, applicationName, mongoConfig)
	default:
		return nil, fmt.Errorf("unsupported isolation mode: %s", config.IsolationMode)
	}
}

// GetDatabase returns a TenantDatabase for the specified tenant and application.
func (pm *mongoPoolManagerImpl) GetDatabase(ctx context.Context, tenantID, applicationName string) (*TenantDatabase, error) {
	// Validate inputs
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: empty tenant ID", ErrInvalidTenantID)
	}

	if strings.TrimSpace(applicationName) == "" {
		return nil, fmt.Errorf("application name is required")
	}

	// Check context cancellation
	if ctx.Err() != nil {
		return nil, fmt.Errorf("context error: %w", ctx.Err())
	}

	// Resolve tenant configuration with service filter to get database credentials
	config, err := pm.resolver.ResolveWithService(ctx, tenantID, applicationName)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve tenant config: %w", err)
	}

	// Validate tenant status
	if config.Status != "active" {
		return nil, fmt.Errorf("%w: tenant status is %s", ErrTenantInactive, config.Status)
	}

	// Get database configuration for the application
	dbServices, ok := config.Databases[applicationName]
	if !ok || dbServices.MongoDB == nil {
		return nil, fmt.Errorf("MongoDB config not found for application %s", applicationName)
	}

	mongoConfig := dbServices.MongoDB

	// Get or create client based on isolation mode
	var client *mongo.Client

	var collectionPrefix string

	switch config.IsolationMode {
	case "database":
		client, err = pm.getClientDatabaseMode(ctx, tenantID, applicationName, mongoConfig)
		collectionPrefix = "" // No prefix in database mode
	case "schema":
		client, err = pm.getClientSchemaMode(ctx, tenantID, applicationName, mongoConfig)
		collectionPrefix = generateCollectionPrefix(tenantID)
	default:
		return nil, fmt.Errorf("unsupported isolation mode: %s", config.IsolationMode)
	}

	if err != nil {
		return nil, err
	}

	// Get the database from the client
	db := client.Database(mongoConfig.Database)

	return &TenantDatabase{
		db:               db,
		tenantID:         tenantID,
		collectionPrefix: collectionPrefix,
	}, nil
}

// getClientDatabaseMode returns a client from a dedicated MongoConnection per tenant.
func (pm *mongoPoolManagerImpl) getClientDatabaseMode(ctx context.Context, tenantID, appName string, mongoConfig *MongoDBConfig) (*mongo.Client, error) {
	connKey := pm.makeConnKey(tenantID, appName)

	// Check if connection exists
	pm.mu.RLock()
	entry, exists := pm.tenantConns[connKey]
	pm.mu.RUnlock()

	if exists && entry.conn != nil && entry.conn.Connected {
		entry.updateLastUsed()
		return entry.conn.GetDB(ctx)
	}

	// Create new connection
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, exists = pm.tenantConns[connKey]; exists && entry != nil && entry.conn != nil && entry.conn.Connected {
		entry.updateLastUsed()
		return entry.conn.GetDB(ctx)
	}

	// Check max connections limit
	if len(pm.tenantConns)+len(pm.sharedConns) >= pm.maxConns {
		// Evict least recently used connection
		if err := pm.evictLRUConn(ctx); err != nil {
			return nil, fmt.Errorf("max connections reached and failed to evict: %w", err)
		}
	}

	// Create new MongoConnection using commons/mongo infrastructure
	mongoConn := &libMongo.MongoConnection{
		ConnectionStringSource: mongoConfig.URI,
		Database:               mongoConfig.Database,
		MaxPoolSize:            pm.maxPoolSize,
	}

	// Set logger if available
	if pm.logger != nil {
		mongoConn.Logger = pm.logger
	}

	// Connect to MongoDB
	if err := mongoConn.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to tenant MongoDB: %w", err)
	}

	// Store connection
	now := time.Now()
	pm.tenantConns[connKey] = &mongoConnEntry{
		conn:          mongoConn,
		tenantID:      tenantID,
		appName:       appName,
		isolationMode: "database",
		databaseName:  mongoConfig.Database,
		createdAt:     now,
		lastUsedAt:    now,
	}

	if pm.logger != nil {
		pm.logger.Infof("Created new tenant MongoDB connection for %s (database mode)", connKey)
	}

	return mongoConn.GetDB(ctx)
}

// getClientSchemaMode returns a client from a shared MongoConnection.
func (pm *mongoPoolManagerImpl) getClientSchemaMode(ctx context.Context, tenantID, appName string, mongoConfig *MongoDBConfig) (*mongo.Client, error) {
	connKey := pm.makeConnKey(tenantID, appName)

	// Check if we already have mapping to shared connection
	pm.mu.RLock()
	sharedURI, mapped := pm.tenantToSharedConn[connKey]

	var entry *mongoConnEntry

	if mapped {
		entry = pm.sharedConns[sharedURI]
	}

	pm.mu.RUnlock()

	if entry != nil && entry.conn != nil && entry.conn.Connected {
		entry.updateLastUsed()

		return entry.conn.GetDB(ctx)
	}

	// Create or get shared connection
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Check if shared connection exists for this URI
	var exists bool

	if entry, exists = pm.sharedConns[mongoConfig.URI]; exists && entry != nil && entry.conn != nil && entry.conn.Connected {
		// Map this tenant to the shared connection
		pm.tenantToSharedConn[connKey] = mongoConfig.URI

		entry.updateLastUsed()

		return entry.conn.GetDB(ctx)
	}

	// Check max connections limit
	if len(pm.tenantConns)+len(pm.sharedConns) >= pm.maxConns {
		if err := pm.evictLRUConn(ctx); err != nil {
			return nil, fmt.Errorf("max connections reached and failed to evict: %w", err)
		}
	}

	// Create new shared MongoConnection using commons/mongo infrastructure
	mongoConn := &libMongo.MongoConnection{
		ConnectionStringSource: mongoConfig.URI,
		Database:               mongoConfig.Database,
		MaxPoolSize:            pm.maxPoolSize,
	}

	// Set logger if available
	if pm.logger != nil {
		mongoConn.Logger = pm.logger
	}

	// Connect to MongoDB
	if err := mongoConn.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to shared MongoDB: %w", err)
	}

	// Store shared connection
	now := time.Now()
	pm.sharedConns[mongoConfig.URI] = &mongoConnEntry{
		conn:          mongoConn,
		tenantID:      "", // Shared connections don't belong to a single tenant
		appName:       appName,
		isolationMode: "schema",
		databaseName:  mongoConfig.Database,
		createdAt:     now,
		lastUsedAt:    now,
	}

	// Map tenant to shared connection
	pm.tenantToSharedConn[connKey] = mongoConfig.URI

	if pm.logger != nil {
		pm.logger.Infof("Created new shared MongoDB connection for application %s (schema mode)", appName)
	}

	return mongoConn.GetDB(ctx)
}

// GetDefaultConnection returns the default (single-tenant) MongoConnection.
func (pm *mongoPoolManagerImpl) GetDefaultConnection() *libMongo.MongoConnection {
	return pm.defaultConn
}

// CloseClient closes the MongoDB client for a specific tenant and application.
func (pm *mongoPoolManagerImpl) CloseClient(tenantID, applicationName string) error {
	connKey := pm.makeConnKey(tenantID, applicationName)

	pm.mu.Lock()
	defer pm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check tenant connections (database mode)
	if entry, exists := pm.tenantConns[connKey]; exists {
		if entry.conn != nil && entry.conn.DB != nil {
			if err := entry.conn.DB.Disconnect(ctx); err != nil {
				return fmt.Errorf("failed to disconnect client: %w", err)
			}
		}

		delete(pm.tenantConns, connKey)

		if pm.logger != nil {
			pm.logger.Infof("Closed tenant MongoDB connection for %s", connKey)
		}

		return nil
	}

	// Check if mapped to shared connection (schema mode)
	if sharedURI, mapped := pm.tenantToSharedConn[connKey]; mapped {
		// Just remove the mapping, don't close shared connection
		// (other tenants might be using it)
		delete(pm.tenantToSharedConn, connKey)

		// Check if any other tenants are using this shared connection
		stillInUse := false

		for _, uri := range pm.tenantToSharedConn {
			if uri == sharedURI {
				stillInUse = true
				break
			}
		}

		// If no one else is using it, close the shared connection
		if !stillInUse {
			if entry, exists := pm.sharedConns[sharedURI]; exists {
				if entry.conn != nil && entry.conn.DB != nil {
					if err := entry.conn.DB.Disconnect(ctx); err != nil {
						return fmt.Errorf("failed to disconnect shared client: %w", err)
					}
				}

				delete(pm.sharedConns, sharedURI)

				if pm.logger != nil {
					pm.logger.Infof("Closed shared MongoDB connection %s", pm.sanitizeURIForLog(sharedURI))
				}
			}
		}

		return nil
	}

	return fmt.Errorf("client not found for tenant %s and application %s", tenantID, applicationName)
}

// CloseAll closes all managed MongoDB clients and stops background cleanup.
func (pm *mongoPoolManagerImpl) CloseAll(ctx context.Context) error {
	// Signal cleanup goroutine to stop
	close(pm.cleanupStop)

	// Wait for cleanup goroutine to finish (with timeout)
	select {
	case <-pm.cleanupDone:
	case <-time.After(5 * time.Second):
		// Timeout waiting for cleanup goroutine
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	var errs []string

	disconnectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Close all tenant connections
	for key, entry := range pm.tenantConns {
		if entry.conn != nil && entry.conn.DB != nil {
			if err := entry.conn.DB.Disconnect(disconnectCtx); err != nil {
				errs = append(errs, fmt.Sprintf("failed to close connection %s: %v", key, err))
			}
		}
	}

	pm.tenantConns = make(map[string]*mongoConnEntry)

	// Close all shared connections
	for key, entry := range pm.sharedConns {
		if entry.conn != nil && entry.conn.DB != nil {
			if err := entry.conn.DB.Disconnect(disconnectCtx); err != nil {
				errs = append(errs, fmt.Sprintf("failed to close shared connection %s: %v", key, err))
			}
		}
	}

	pm.sharedConns = make(map[string]*mongoConnEntry)
	pm.tenantToSharedConn = make(map[string]string)

	if pm.logger != nil {
		pm.logger.Info("Closed all MongoDB connections")
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing clients: %s", strings.Join(errs, "; "))
	}

	return nil
}

// Stats returns statistics for all managed clients.
func (pm *mongoPoolManagerImpl) Stats() map[string]MongoPoolStats {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	stats := make(map[string]MongoPoolStats)

	// Stats for tenant connections (database mode)
	for key, entry := range pm.tenantConns {
		stats[key] = MongoPoolStats{
			TenantID:        entry.tenantID,
			ApplicationName: entry.appName,
			IsolationMode:   entry.isolationMode,
			CreatedAt:       entry.createdAt,
			LastUsedAt:      entry.getLastUsed(),
			DatabaseName:    entry.databaseName,
		}
	}

	// Stats for shared connections (schema mode)
	for uri, entry := range pm.sharedConns {
		key := "shared:" + pm.sanitizeURIForKey(uri)
		stats[key] = MongoPoolStats{
			TenantID:        "", // Shared client
			ApplicationName: entry.appName,
			IsolationMode:   entry.isolationMode,
			CreatedAt:       entry.createdAt,
			LastUsedAt:      entry.getLastUsed(),
			DatabaseName:    entry.databaseName,
		}
	}

	return stats
}

// cleanupIdleConns runs in the background and closes idle connections.
// It respects both the cleanupStop channel and the context cancellation.
func (pm *mongoPoolManagerImpl) cleanupIdleConns() {
	defer close(pm.cleanupDone)

	ticker := time.NewTicker(pm.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pm.cleanupStop:
			return
		case <-pm.getContextDone():
			return
		case <-ticker.C:
			pm.doCleanup()
		}
	}
}

// getContextDone returns the Done channel from the context, or a nil channel if no context is set.
// A nil channel blocks forever, which is the desired behavior when no context is provided.
func (pm *mongoPoolManagerImpl) getContextDone() <-chan struct{} {
	if pm.ctx != nil {
		return pm.ctx.Done()
	}

	return nil
}

// doCleanup performs the actual cleanup of idle connections.
func (pm *mongoPoolManagerImpl) doCleanup() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now()
	threshold := now.Add(-pm.idleTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Cleanup idle tenant connections
	for key, entry := range pm.tenantConns {
		if entry.getLastUsed().Before(threshold) {
			if entry.conn != nil && entry.conn.DB != nil {
				_ = entry.conn.DB.Disconnect(ctx) // Ignore errors during cleanup
			}

			delete(pm.tenantConns, key)

			if pm.logger != nil {
				pm.logger.Infof("Cleaned up idle tenant MongoDB connection: %s", key)
			}
		}
	}

	// Cleanup idle shared connections (only if no tenants are using them)
	for uri, entry := range pm.sharedConns {
		if entry.getLastUsed().Before(threshold) {
			// Check if any tenant is still mapped to this connection
			stillInUse := false

			for _, mappedURI := range pm.tenantToSharedConn {
				if mappedURI == uri {
					stillInUse = true
					break
				}
			}

			if !stillInUse {
				if entry.conn != nil && entry.conn.DB != nil {
					_ = entry.conn.DB.Disconnect(ctx)
				}

				delete(pm.sharedConns, uri)

				if pm.logger != nil {
					pm.logger.Infof("Cleaned up idle shared MongoDB connection: %s", pm.sanitizeURIForLog(uri))
				}
			}
		}
	}
}

// evictLRUConn evicts the least recently used connection to make room for a new one.
// Must be called with pm.mu held.
func (pm *mongoPoolManagerImpl) evictLRUConn(ctx context.Context) error {
	var oldestKey string

	var oldestTime time.Time

	var isShared bool

	// Find LRU in tenant connections
	for key, entry := range pm.tenantConns {
		lastUsed := entry.getLastUsed()
		if oldestKey == "" || lastUsed.Before(oldestTime) {
			oldestKey = key
			oldestTime = lastUsed
			isShared = false
		}
	}

	// Find LRU in shared connections
	for uri, entry := range pm.sharedConns {
		lastUsed := entry.getLastUsed()
		if oldestKey == "" || lastUsed.Before(oldestTime) {
			oldestKey = uri
			oldestTime = lastUsed
			isShared = true
		}
	}

	if oldestKey == "" {
		return fmt.Errorf("no connections to evict")
	}

	disconnectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Evict the LRU connection
	if isShared {
		entry := pm.sharedConns[oldestKey]
		if entry.conn != nil && entry.conn.DB != nil {
			_ = entry.conn.DB.Disconnect(disconnectCtx)
		}

		delete(pm.sharedConns, oldestKey)

		// Remove all tenant mappings to this URI
		for key, uri := range pm.tenantToSharedConn {
			if uri == oldestKey {
				delete(pm.tenantToSharedConn, key)
			}
		}
	} else {
		entry := pm.tenantConns[oldestKey]
		if entry.conn != nil && entry.conn.DB != nil {
			_ = entry.conn.DB.Disconnect(disconnectCtx)
		}

		delete(pm.tenantConns, oldestKey)
	}

	if pm.logger != nil {
		pm.logger.Infof("Evicted LRU MongoDB connection: %s", oldestKey)
	}

	return nil
}

// makeConnKey creates a unique key for a tenant/application combination.
func (pm *mongoPoolManagerImpl) makeConnKey(tenantID, appName string) string {
	return tenantID + ":" + appName
}

// sanitizeURIForKey removes sensitive information from URI for use as a map key.
func (pm *mongoPoolManagerImpl) sanitizeURIForKey(uri string) string {
	// Simple sanitization - extract host and database from URI
	// This is for display purposes in stats only
	sanitized := strings.TrimPrefix(uri, "mongodb://")
	sanitized = strings.TrimPrefix(sanitized, "mongodb+srv://")

	// Remove credentials if present (user:pass@)
	if idx := strings.Index(sanitized, "@"); idx != -1 {
		sanitized = sanitized[idx+1:]
	}

	// Take only host portion
	if idx := strings.Index(sanitized, "/"); idx != -1 {
		sanitized = sanitized[:idx]
	}

	return sanitized
}

// sanitizeURIForLog removes sensitive information from URI for logging.
func (pm *mongoPoolManagerImpl) sanitizeURIForLog(uri string) string {
	// Simple sanitization - extract host portion for logging
	return pm.sanitizeURIForKey(uri)
}

// generateCollectionPrefix generates a collection prefix for schema isolation mode.
// Sanitizes the tenant ID to be MongoDB collection name safe.
func generateCollectionPrefix(tenantID string) string {
	// Sanitize tenant ID for use in collection names
	// Replace any non-alphanumeric characters with underscore
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}

		return '_'
	}, tenantID)

	return "tenant_" + sanitized + "_"
}
