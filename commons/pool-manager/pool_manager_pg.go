package poolmanager

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	libPostgres "github.com/LerianStudio/lib-commons/v2/commons/postgres"
	"github.com/bxcodec/dbresolver/v2"
)

// PostgresPoolManager defines the interface for managing PostgreSQL connection pools
// across multiple tenants with support for different isolation modes.
// It leverages the existing commons/postgres infrastructure for connection management.
type PostgresPoolManager interface {
	// GetConnection returns a database connection for the specified tenant and application.
	// In database mode: returns a connection from a dedicated PostgresConnection per tenant.
	// In schema mode: returns the default connection (caller must SET search_path).
	GetConnection(ctx context.Context, tenantID, applicationName string) (dbresolver.DB, error)

	// GetSchemaConnection returns a connection with search_path set for schema mode.
	// This is a convenience method that gets a connection and sets the search_path.
	GetSchemaConnection(ctx context.Context, tenantID, applicationName string) (dbresolver.DB, error)

	// GetDefaultConnection returns the default (single-tenant) connection.
	// Use this when operating in single-tenant mode.
	GetDefaultConnection() (dbresolver.DB, error)

	// ClosePool closes the connection pool for a specific tenant and application.
	ClosePool(tenantID, applicationName string) error

	// CloseTenant closes all connection pools associated with a tenant.
	CloseTenant(tenantID string) error

	// CloseAll closes all managed connection pools and stops background cleanup.
	CloseAll() error

	// Stats returns statistics for all managed pools.
	Stats() map[string]PoolStats
}

// PoolStats contains statistics for a connection pool.
type PoolStats struct {
	TenantID        string
	ApplicationName string
	IsolationMode   string
	CreatedAt       time.Time
	LastUsedAt      time.Time
	OpenConnections int
	IdleConnections int
}

// PostgresPoolManagerConfig holds configuration for the pool manager.
type PostgresPoolManagerConfig struct {
	// DefaultConnection is the default PostgresConnection for single-tenant/schema mode.
	// Required for schema mode, optional for database-only mode.
	DefaultConnection *libPostgres.PostgresConnection

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

	// ConnectionConfig holds shared connection settings for tenant connections.
	ConnectionConfig ConnectionConfig
}

// ConnectionConfig holds connection pool settings for tenant databases.
type ConnectionConfig struct {
	// MaxOpenConnections sets the maximum number of open connections per tenant.
	// Default: 25
	MaxOpenConnections int

	// MaxIdleConnections sets the maximum number of idle connections per tenant.
	// Default: 5
	MaxIdleConnections int
}

// poolEntry holds a PostgresConnection along with metadata.
type poolEntry struct {
	conn          *libPostgres.PostgresConnection
	tenantID      string
	appName       string
	isolationMode string
	createdAt     time.Time
	lastUsedAt    time.Time
	mu            sync.RWMutex
}

func (p *poolEntry) updateLastUsed() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.lastUsedAt = time.Now()
}

func (p *poolEntry) getLastUsed() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.lastUsedAt
}

// postgresPoolManagerImpl is the default implementation of PostgresPoolManager.
type postgresPoolManagerImpl struct {
	defaultConn     *libPostgres.PostgresConnection
	resolver        Resolver
	logger          libLog.Logger
	maxConns        int
	idleTimeout     time.Duration
	cleanupInterval time.Duration
	connConfig      ConnectionConfig

	// tenantConns stores PostgresConnection instances keyed by "tenantID:appName" for database mode.
	tenantConns map[string]*poolEntry

	// sharedConns stores PostgresConnection instances keyed by DSN for schema mode.
	sharedConns map[string]*poolEntry

	// tenantToSharedConn maps "tenantID:appName" to DSN for schema mode.
	tenantToSharedConn map[string]string

	mu sync.RWMutex

	// cleanup goroutine control
	cleanupStop chan struct{}
	cleanupDone chan struct{}
}

// PoolManagerOption is a function that configures a PostgresPoolManager.
type PoolManagerOption func(*postgresPoolManagerImpl)

// WithMaxPools sets the maximum number of connection pools.
// Default is 100.
func WithMaxPools(maxPools int) PoolManagerOption {
	return func(pm *postgresPoolManagerImpl) {
		if maxPools > 0 {
			pm.maxConns = maxPools
		}
	}
}

// WithIdleTimeout sets the duration after which idle pools are closed.
// Default is 30 minutes.
func WithIdleTimeout(timeout time.Duration) PoolManagerOption {
	return func(pm *postgresPoolManagerImpl) {
		if timeout > 0 {
			pm.idleTimeout = timeout
		}
	}
}

// WithCleanupInterval sets the interval for the background cleanup goroutine.
// Default is 5 minutes.
func WithCleanupInterval(interval time.Duration) PoolManagerOption {
	return func(pm *postgresPoolManagerImpl) {
		if interval > 0 {
			pm.cleanupInterval = interval
		}
	}
}

// WithDefaultConnection sets the default PostgresConnection for schema mode.
func WithDefaultConnection(conn *libPostgres.PostgresConnection) PoolManagerOption {
	return func(pm *postgresPoolManagerImpl) {
		pm.defaultConn = conn
	}
}

// WithLogger sets the logger for the pool manager.
func WithLogger(logger libLog.Logger) PoolManagerOption {
	return func(pm *postgresPoolManagerImpl) {
		pm.logger = logger
	}
}

// WithConnectionConfig sets the connection configuration for tenant databases.
func WithConnectionConfig(config ConnectionConfig) PoolManagerOption {
	return func(pm *postgresPoolManagerImpl) {
		if config.MaxOpenConnections > 0 {
			pm.connConfig.MaxOpenConnections = config.MaxOpenConnections
		}

		if config.MaxIdleConnections > 0 {
			pm.connConfig.MaxIdleConnections = config.MaxIdleConnections
		}
	}
}

// NewPostgresPoolManager creates a new PostgresPoolManager with the given resolver and options.
func NewPostgresPoolManager(resolver Resolver, opts ...PoolManagerOption) PostgresPoolManager {
	pm := &postgresPoolManagerImpl{
		resolver:           resolver,
		maxConns:           100,
		idleTimeout:        30 * time.Minute,
		cleanupInterval:    5 * time.Minute,
		connConfig:         ConnectionConfig{MaxOpenConnections: 25, MaxIdleConnections: 5},
		tenantConns:        make(map[string]*poolEntry),
		sharedConns:        make(map[string]*poolEntry),
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

// NewPostgresPoolManagerWithConfig creates a new PostgresPoolManager with explicit configuration.
// This is the recommended constructor for production use.
func NewPostgresPoolManagerWithConfig(cfg PostgresPoolManagerConfig) (PostgresPoolManager, error) {
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

	if cfg.ConnectionConfig.MaxOpenConnections <= 0 {
		cfg.ConnectionConfig.MaxOpenConnections = 25
	}

	if cfg.ConnectionConfig.MaxIdleConnections <= 0 {
		cfg.ConnectionConfig.MaxIdleConnections = 5
	}

	pm := &postgresPoolManagerImpl{
		defaultConn:        cfg.DefaultConnection,
		resolver:           cfg.Resolver,
		logger:             cfg.Logger,
		maxConns:           cfg.MaxConnections,
		idleTimeout:        cfg.IdleTimeout,
		cleanupInterval:    cfg.CleanupInterval,
		connConfig:         cfg.ConnectionConfig,
		tenantConns:        make(map[string]*poolEntry),
		sharedConns:        make(map[string]*poolEntry),
		tenantToSharedConn: make(map[string]string),
		cleanupStop:        make(chan struct{}),
		cleanupDone:        make(chan struct{}),
	}

	// Start background cleanup goroutine
	go pm.cleanupIdleConns()

	return pm, nil
}

// GetConnection returns a database connection for the specified tenant and application.
func (pm *postgresPoolManagerImpl) GetConnection(ctx context.Context, tenantID, applicationName string) (dbresolver.DB, error) {
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
	if !ok || dbServices.PostgreSQL == nil {
		return nil, fmt.Errorf("database config not found for application %s", applicationName)
	}

	pgConfig := dbServices.PostgreSQL

	// Route based on isolation mode
	switch config.IsolationMode {
	case "database":
		return pm.getConnectionDatabaseMode(ctx, tenantID, applicationName, pgConfig)
	case "schema":
		return pm.getConnectionSchemaMode(ctx, tenantID, applicationName, pgConfig)
	default:
		return nil, fmt.Errorf("unsupported isolation mode: %s", config.IsolationMode)
	}
}

// getConnectionDatabaseMode returns a connection from a dedicated PostgresConnection per tenant.
func (pm *postgresPoolManagerImpl) getConnectionDatabaseMode(_ context.Context, tenantID, appName string, pgConfig *PostgreSQLConfig) (dbresolver.DB, error) {
	poolKey := pm.makePoolKey(tenantID, appName)

	// Check if connection exists
	pm.mu.RLock()
	entry, exists := pm.tenantConns[poolKey]
	pm.mu.RUnlock()

	if exists && entry.conn != nil && entry.conn.Connected {
		entry.updateLastUsed()
		return entry.conn.GetDB()
	}

	// Create new connection
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, exists = pm.tenantConns[poolKey]; exists && entry != nil && entry.conn != nil && entry.conn.Connected {
		entry.updateLastUsed()
		return entry.conn.GetDB()
	}

	// Check max connections limit
	if len(pm.tenantConns)+len(pm.sharedConns) >= pm.maxConns {
		// Evict least recently used connection
		if err := pm.evictLRUConn(); err != nil {
			return nil, fmt.Errorf("max connections reached and failed to evict: %w", err)
		}
	}

	// Build connection strings
	primaryDSN := pm.buildDSN(pgConfig)
	// For tenant connections, we use the same DSN for primary and replica
	// unless the tenant config specifies separate replica settings
	replicaDSN := primaryDSN

	// Create new PostgresConnection
	pgConn := &libPostgres.PostgresConnection{
		ConnectionStringPrimary: primaryDSN,
		ConnectionStringReplica: replicaDSN,
		PrimaryDBName:           pgConfig.Database,
		ReplicaDBName:           pgConfig.Database,
		MaxOpenConnections:      pm.connConfig.MaxOpenConnections,
		MaxIdleConnections:      pm.connConfig.MaxIdleConnections,
		MigrationsPath:          "", // No migrations for tenant connections
	}

	// Set logger if available
	if pm.logger != nil {
		pgConn.Logger = pm.logger
	}

	// Connect (without migrations for tenant databases)
	if err := pm.connectWithoutMigrations(pgConn); err != nil {
		return nil, fmt.Errorf("failed to connect to tenant database: %w", err)
	}

	// Store connection
	now := time.Now()
	pm.tenantConns[poolKey] = &poolEntry{
		conn:          pgConn,
		tenantID:      tenantID,
		appName:       appName,
		isolationMode: "database",
		createdAt:     now,
		lastUsedAt:    now,
	}

	if pm.logger != nil {
		pm.logger.Infof("Created new tenant connection for %s (database mode)", poolKey)
	}

	return pgConn.GetDB()
}

// connectWithoutMigrations connects to the database without running migrations.
// This is used for tenant connections where migrations are managed by the Tenant Service.
func (pm *postgresPoolManagerImpl) connectWithoutMigrations(pgConn *libPostgres.PostgresConnection) error {
	// We need to connect without calling the full Connect() method
	// which attempts to run migrations. Instead, we create the connection directly.
	// The PostgresConnection.Connect() includes migration logic, so we replicate
	// just the connection part here.

	// For tenant connections, we'll use the standard Connect but with an empty migrations path
	// The Connect method handles missing migrations gracefully
	err := pgConn.Connect()
	if err != nil {
		return err
	}

	// Double-check that the connection was actually established
	// The Connect method may return nil even if connection failed (uses Fatal logging)
	if pgConn.ConnectionDB == nil {
		return fmt.Errorf("connection failed: database connection is nil after Connect()")
	}

	return nil
}

// getConnectionSchemaMode returns the default connection for schema mode.
// The caller is responsible for executing SET search_path.
func (pm *postgresPoolManagerImpl) getConnectionSchemaMode(_ context.Context, tenantID, appName string, pgConfig *PostgreSQLConfig) (dbresolver.DB, error) {
	poolKey := pm.makePoolKey(tenantID, appName)
	dsn := pm.buildDSN(pgConfig)

	// Check if we already have mapping to shared connection
	pm.mu.RLock()
	sharedDSN, mapped := pm.tenantToSharedConn[poolKey]

	var entry *poolEntry
	if mapped {
		entry = pm.sharedConns[sharedDSN]
	}

	pm.mu.RUnlock()

	if entry != nil && entry.conn != nil && entry.conn.Connected {
		entry.updateLastUsed()
		return entry.conn.GetDB()
	}

	// Create or get shared connection
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Check if shared connection exists for this DSN
	var exists bool

	if entry, exists = pm.sharedConns[dsn]; exists && entry != nil && entry.conn != nil && entry.conn.Connected {
		// Map this tenant to the shared connection
		pm.tenantToSharedConn[poolKey] = dsn

		entry.updateLastUsed()

		return entry.conn.GetDB()
	}

	// Check max connections limit
	if len(pm.tenantConns)+len(pm.sharedConns) >= pm.maxConns {
		if err := pm.evictLRUConn(); err != nil {
			return nil, fmt.Errorf("max connections reached and failed to evict: %w", err)
		}
	}

	// Create new shared PostgresConnection
	pgConn := &libPostgres.PostgresConnection{
		ConnectionStringPrimary: dsn,
		ConnectionStringReplica: dsn,
		PrimaryDBName:           pgConfig.Database,
		ReplicaDBName:           pgConfig.Database,
		MaxOpenConnections:      pm.connConfig.MaxOpenConnections,
		MaxIdleConnections:      pm.connConfig.MaxIdleConnections,
		MigrationsPath:          "", // No migrations for schema mode connections
	}

	// Set logger if available
	if pm.logger != nil {
		pgConn.Logger = pm.logger
	}

	// Connect (without migrations)
	if err := pm.connectWithoutMigrations(pgConn); err != nil {
		return nil, fmt.Errorf("failed to connect to shared database: %w", err)
	}

	// Store shared connection
	now := time.Now()
	pm.sharedConns[dsn] = &poolEntry{
		conn:          pgConn,
		tenantID:      "", // Shared connections don't belong to a single tenant
		appName:       appName,
		isolationMode: "schema",
		createdAt:     now,
		lastUsedAt:    now,
	}

	// Map tenant to shared connection
	pm.tenantToSharedConn[poolKey] = dsn

	if pm.logger != nil {
		pm.logger.Infof("Created new shared connection for application %s (schema mode)", appName)
	}

	return pgConn.GetDB()
}

// GetSchemaConnection returns a connection with search_path set for the tenant's schema.
// Note: The search_path is set at the session level. For proper schema isolation,
// consider using a connection-per-request pattern or transaction-level SET LOCAL.
func (pm *postgresPoolManagerImpl) GetSchemaConnection(ctx context.Context, tenantID, applicationName string) (dbresolver.DB, error) {
	// Get the connection first
	db, err := pm.GetConnection(ctx, tenantID, applicationName)
	if err != nil {
		return nil, err
	}

	// Determine the schema name
	schemaName := pm.getSchemaName(tenantID)

	// Set the search_path
	// Note: This sets it for the current session. For true isolation,
	// you should use SET LOCAL within a transaction.
	_, err = db.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s, public", schemaName))
	if err != nil {
		return nil, fmt.Errorf("failed to set search_path: %w", err)
	}

	return db, nil
}

// GetDefaultConnection returns the default (single-tenant) connection.
func (pm *postgresPoolManagerImpl) GetDefaultConnection() (dbresolver.DB, error) {
	if pm.defaultConn == nil {
		return nil, fmt.Errorf("default connection not configured")
	}

	return pm.defaultConn.GetDB()
}

// isSharedConnInUse checks if a shared DSN is still in use by any tenant.
// Must be called with pm.mu held.
func (pm *postgresPoolManagerImpl) isSharedConnInUse(sharedDSN string) bool {
	for _, dsn := range pm.tenantToSharedConn {
		if dsn == sharedDSN {
			return true
		}
	}

	return false
}

// disconnectPoolEntry closes the connection in the pool entry if it exists.
func (pm *postgresPoolManagerImpl) disconnectPoolEntry(entry *poolEntry) error {
	if entry.conn != nil && entry.conn.ConnectionDB != nil {
		return (*entry.conn.ConnectionDB).Close()
	}

	return nil
}

// closeTenantPoolConn closes a tenant pool connection (database mode).
// Must be called with pm.mu held.
func (pm *postgresPoolManagerImpl) closeTenantPoolConn(poolKey string, entry *poolEntry) error {
	if err := pm.disconnectPoolEntry(entry); err != nil {
		return fmt.Errorf("failed to close connection: %w", err)
	}

	delete(pm.tenantConns, poolKey)

	if pm.logger != nil {
		pm.logger.Infof("Closed tenant connection for %s", poolKey)
	}

	return nil
}

// closeSharedPoolConnIfUnused closes a shared connection if no tenants are using it.
// Must be called with pm.mu held.
func (pm *postgresPoolManagerImpl) closeSharedPoolConnIfUnused(sharedDSN string) error {
	if pm.isSharedConnInUse(sharedDSN) {
		return nil
	}

	entry, exists := pm.sharedConns[sharedDSN]
	if !exists {
		return nil
	}

	if err := pm.disconnectPoolEntry(entry); err != nil {
		return fmt.Errorf("failed to close shared connection: %w", err)
	}

	delete(pm.sharedConns, sharedDSN)

	if pm.logger != nil {
		pm.logger.Infof("Closed shared connection %s", pm.sanitizeDSNForLog(sharedDSN))
	}

	return nil
}

// ClosePool closes the connection pool for a specific tenant and application.
func (pm *postgresPoolManagerImpl) ClosePool(tenantID, applicationName string) error {
	poolKey := pm.makePoolKey(tenantID, applicationName)

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Check tenant connections (database mode)
	if entry, exists := pm.tenantConns[poolKey]; exists {
		return pm.closeTenantPoolConn(poolKey, entry)
	}

	// Check if mapped to shared connection (schema mode)
	if sharedDSN, mapped := pm.tenantToSharedConn[poolKey]; mapped {
		delete(pm.tenantToSharedConn, poolKey)

		if err := pm.closeSharedPoolConnIfUnused(sharedDSN); err != nil {
			return err
		}

		return nil
	}

	return fmt.Errorf("pool not found for tenant %s and application %s", tenantID, applicationName)
}

// closeTenantConnsForPrefix closes all tenant connections matching the prefix.
// Must be called with pm.mu held.
func (pm *postgresPoolManagerImpl) closeTenantConnsForPrefix(prefix string) []string {
	var errs []string

	for key, entry := range pm.tenantConns {
		if !strings.HasPrefix(key, prefix) {
			continue
		}

		if err := pm.disconnectPoolEntry(entry); err != nil {
			errs = append(errs, fmt.Sprintf("failed to close connection %s: %v", key, err))
		}

		delete(pm.tenantConns, key)
	}

	return errs
}

// closeSharedConnsForPrefix closes shared connections for mappings matching the prefix.
// Must be called with pm.mu held.
func (pm *postgresPoolManagerImpl) closeSharedConnsForPrefix(prefix string) []string {
	var errs []string

	for key, sharedDSN := range pm.tenantToSharedConn {
		if !strings.HasPrefix(key, prefix) {
			continue
		}

		delete(pm.tenantToSharedConn, key)

		if pm.isSharedConnInUse(sharedDSN) {
			continue
		}

		entry, exists := pm.sharedConns[sharedDSN]
		if !exists {
			continue
		}

		if err := pm.disconnectPoolEntry(entry); err != nil {
			errs = append(errs, fmt.Sprintf("failed to close shared connection: %v", err))
		}

		delete(pm.sharedConns, sharedDSN)
	}

	return errs
}

// CloseTenant closes all connection pools associated with a tenant.
func (pm *postgresPoolManagerImpl) CloseTenant(tenantID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	prefix := tenantID + ":"

	var errs []string

	errs = append(errs, pm.closeTenantConnsForPrefix(prefix)...)
	errs = append(errs, pm.closeSharedConnsForPrefix(prefix)...)

	if pm.logger != nil {
		pm.logger.Infof("Closed all connections for tenant %s", tenantID)
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing tenant connections: %s", strings.Join(errs, "; "))
	}

	return nil
}

// CloseAll closes all managed connection pools and stops background cleanup.
func (pm *postgresPoolManagerImpl) CloseAll() error {
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

	// Close all tenant connections
	for key, entry := range pm.tenantConns {
		if entry.conn != nil && entry.conn.ConnectionDB != nil {
			if err := (*entry.conn.ConnectionDB).Close(); err != nil {
				errs = append(errs, fmt.Sprintf("failed to close connection %s: %v", key, err))
			}
		}
	}

	pm.tenantConns = make(map[string]*poolEntry)

	// Close all shared connections
	for key, entry := range pm.sharedConns {
		if entry.conn != nil && entry.conn.ConnectionDB != nil {
			if err := (*entry.conn.ConnectionDB).Close(); err != nil {
				errs = append(errs, fmt.Sprintf("failed to close shared connection %s: %v", key, err))
			}
		}
	}

	pm.sharedConns = make(map[string]*poolEntry)
	pm.tenantToSharedConn = make(map[string]string)

	if pm.logger != nil {
		pm.logger.Info("Closed all PostgreSQL connections")
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing connections: %s", strings.Join(errs, "; "))
	}

	return nil
}

// Stats returns statistics for all managed pools.
func (pm *postgresPoolManagerImpl) Stats() map[string]PoolStats {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	stats := make(map[string]PoolStats)

	// Stats for tenant connections (database mode)
	for key, entry := range pm.tenantConns {
		openConns := 0
		idleConns := 0

		if entry.conn != nil && entry.conn.ConnectionDB != nil {
			// The dbresolver.DB doesn't expose stats directly,
			// but we can track connection count from our settings
			openConns = pm.connConfig.MaxOpenConnections
			idleConns = pm.connConfig.MaxIdleConnections
		}

		stats[key] = PoolStats{
			TenantID:        entry.tenantID,
			ApplicationName: entry.appName,
			IsolationMode:   entry.isolationMode,
			CreatedAt:       entry.createdAt,
			LastUsedAt:      entry.getLastUsed(),
			OpenConnections: openConns,
			IdleConnections: idleConns,
		}
	}

	// Stats for shared connections (schema mode)
	for dsn, entry := range pm.sharedConns {
		openConns := 0
		idleConns := 0

		if entry.conn != nil && entry.conn.ConnectionDB != nil {
			openConns = pm.connConfig.MaxOpenConnections
			idleConns = pm.connConfig.MaxIdleConnections
		}

		// Use sanitized DSN as key for shared connections
		key := "shared:" + pm.sanitizeDSNForKey(dsn)
		stats[key] = PoolStats{
			TenantID:        "", // Shared connection
			ApplicationName: entry.appName,
			IsolationMode:   entry.isolationMode,
			CreatedAt:       entry.createdAt,
			LastUsedAt:      entry.getLastUsed(),
			OpenConnections: openConns,
			IdleConnections: idleConns,
		}
	}

	return stats
}

// cleanupIdleConns runs in the background and closes idle connections.
func (pm *postgresPoolManagerImpl) cleanupIdleConns() {
	defer close(pm.cleanupDone)

	ticker := time.NewTicker(pm.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pm.cleanupStop:
			return
		case <-ticker.C:
			pm.doCleanup()
		}
	}
}

// cleanupIdleTenantConns cleans up idle tenant connections.
// Must be called with pm.mu held.
func (pm *postgresPoolManagerImpl) cleanupIdleTenantConns(threshold time.Time) {
	for key, entry := range pm.tenantConns {
		if entry.getLastUsed().Before(threshold) {
			if entry.conn != nil && entry.conn.ConnectionDB != nil {
				if err := (*entry.conn.ConnectionDB).Close(); err != nil && pm.logger != nil {
					pm.logger.Warnf("Failed to close idle tenant PostgreSQL connection %s: %v", key, err)
				}
			}

			delete(pm.tenantConns, key)

			if pm.logger != nil {
				pm.logger.Infof("Cleaned up idle tenant connection: %s", key)
			}
		}
	}
}

// cleanupIdleSharedConns cleans up idle shared connections that are no longer in use.
// Must be called with pm.mu held.
func (pm *postgresPoolManagerImpl) cleanupIdleSharedConns(threshold time.Time) {
	for dsn, entry := range pm.sharedConns {
		if !entry.getLastUsed().Before(threshold) {
			continue
		}

		if pm.isSharedConnInUse(dsn) {
			continue
		}

		if entry.conn != nil && entry.conn.ConnectionDB != nil {
			if err := (*entry.conn.ConnectionDB).Close(); err != nil && pm.logger != nil {
				pm.logger.Warnf("Failed to close idle shared PostgreSQL connection %s: %v", pm.sanitizeDSNForLog(dsn), err)
			}
		}

		delete(pm.sharedConns, dsn)

		if pm.logger != nil {
			pm.logger.Infof("Cleaned up idle shared connection: %s", pm.sanitizeDSNForLog(dsn))
		}
	}
}

// doCleanup performs the actual cleanup of idle connections.
func (pm *postgresPoolManagerImpl) doCleanup() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	threshold := time.Now().Add(-pm.idleTimeout)

	pm.cleanupIdleTenantConns(threshold)
	pm.cleanupIdleSharedConns(threshold)
}

// evictLRUConn evicts the least recently used connection to make room for a new one.
// Must be called with pm.mu held.
func (pm *postgresPoolManagerImpl) evictLRUConn() error {
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
	for dsn, entry := range pm.sharedConns {
		lastUsed := entry.getLastUsed()
		if oldestKey == "" || lastUsed.Before(oldestTime) {
			oldestKey = dsn
			oldestTime = lastUsed
			isShared = true
		}
	}

	if oldestKey == "" {
		return fmt.Errorf("no connections to evict")
	}

	// Evict the LRU connection
	if isShared {
		entry := pm.sharedConns[oldestKey]
		if entry.conn != nil && entry.conn.ConnectionDB != nil {
			_ = (*entry.conn.ConnectionDB).Close()
		}

		delete(pm.sharedConns, oldestKey)

		// Remove all tenant mappings to this DSN
		for key, dsn := range pm.tenantToSharedConn {
			if dsn == oldestKey {
				delete(pm.tenantToSharedConn, key)
			}
		}
	} else {
		entry := pm.tenantConns[oldestKey]
		if entry.conn != nil && entry.conn.ConnectionDB != nil {
			_ = (*entry.conn.ConnectionDB).Close()
		}

		delete(pm.tenantConns, oldestKey)
	}

	if pm.logger != nil {
		pm.logger.Infof("Evicted LRU connection: %s", oldestKey)
	}

	return nil
}

// buildDSN constructs a PostgreSQL connection URI from configuration.
// Uses URL-encoded username and password to handle special characters safely.
func (pm *postgresPoolManagerImpl) buildDSN(cfg *PostgreSQLConfig) string {
	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}

	// URL-encode username and password to handle special characters
	encodedUser := url.QueryEscape(cfg.Username)
	encodedPass := url.QueryEscape(cfg.Password)

	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		encodedUser,
		encodedPass,
		cfg.Host,
		cfg.Port,
		cfg.Database,
		sslMode,
	)
}

// makePoolKey creates a unique key for a tenant/application combination.
func (pm *postgresPoolManagerImpl) makePoolKey(tenantID, appName string) string {
	return tenantID + ":" + appName
}

// sanitizeDSNForKey removes sensitive information from DSN for use as a map key identifier.
// Supports both URI format (postgres://user:pass@host:port/db) and key=value format.
func (pm *postgresPoolManagerImpl) sanitizeDSNForKey(dsn string) string {
	// Handle URI format: postgres://user:pass@host:port/db?sslmode=...
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "invalid_dsn"
		}

		return fmt.Sprintf("%s_%s_%s", u.Hostname(), u.Port(), strings.TrimPrefix(u.Path, "/"))
	}

	// Handle legacy key=value format
	parts := strings.Fields(dsn)

	var sanitized []string

	for _, part := range parts {
		if strings.HasPrefix(part, "host=") ||
			strings.HasPrefix(part, "port=") ||
			strings.HasPrefix(part, "dbname=") {
			sanitized = append(sanitized, part)
		}
	}

	return strings.Join(sanitized, "_")
}

// sanitizeDSNForLog removes sensitive information from DSN for logging.
// Supports both URI format (postgres://user:pass@host:port/db) and key=value format.
func (pm *postgresPoolManagerImpl) sanitizeDSNForLog(dsn string) string {
	// Handle URI format: postgres://user:pass@host:port/db?sslmode=...
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "invalid_dsn"
		}

		// Rebuild URI without password
		sanitized := fmt.Sprintf("%s://%s:***@%s%s",
			u.Scheme,
			u.User.Username(),
			u.Host,
			u.Path,
		)

		if u.RawQuery != "" {
			sanitized += "?" + u.RawQuery
		}

		return sanitized
	}

	// Handle legacy key=value format
	parts := strings.Fields(dsn)

	var sanitized []string

	for _, part := range parts {
		if strings.HasPrefix(part, "password=") {
			sanitized = append(sanitized, "password=***")
		} else {
			sanitized = append(sanitized, part)
		}
	}

	return strings.Join(sanitized, " ")
}

// getSchemaName returns the schema name for a tenant.
func (pm *postgresPoolManagerImpl) getSchemaName(tenantID string) string {
	// Sanitize tenant ID for use as schema name
	// Replace any non-alphanumeric characters with underscore
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}

		return '_'
	}, tenantID)

	return "tenant_" + sanitized
}
