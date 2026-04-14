// Package mongo provides multi-tenant MongoDB connection management.
// It fetches tenant-specific database credentials from Tenant Manager service
// and manages connections per tenant using LRU eviction with idle timeout.
package mongo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	mongolib "github.com/LerianStudio/lib-commons/v5/commons/mongo"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/eviction"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/logcompat"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/otel/trace"
)

// mongoPingTimeout is the maximum duration for MongoDB connection health check pings.
const mongoPingTimeout = 3 * time.Second

// defaultConnectionsCheckInterval is the default interval between periodic
// connection pool settings revalidation checks. When a cached connection is
// returned by GetConnection and this interval has elapsed since the last check,
// fresh config is fetched from the Tenant Manager asynchronously.
const defaultConnectionsCheckInterval = 30 * time.Second

// settingsRevalidationTimeout is the maximum duration for the HTTP call
// to the Tenant Manager during async settings revalidation.
const settingsRevalidationTimeout = 5 * time.Second

// DefaultMaxConnections is the default max connections for MongoDB.
const DefaultMaxConnections uint64 = 100

// defaultIdleTimeout is the default duration before a tenant connection becomes
// eligible for eviction. Connections accessed within this window are considered
// active and will not be evicted, allowing the pool to grow beyond maxConnections.
// Defined centrally in the eviction package; aliased here for local convenience.
var defaultIdleTimeout = eviction.DefaultIdleTimeout

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
	logger  *logcompat.Logger

	mu             sync.RWMutex
	connections    map[string]*MongoConnection
	databaseNames  map[string]string // tenantID -> database name (cached from createConnection)
	closed         bool
	maxConnections int                  // soft limit for pool size (0 = unlimited)
	idleTimeout    time.Duration        // how long before a connection is eligible for eviction
	lastAccessed   map[string]time.Time // LRU tracking per tenant

	lastConnectionsCheck     map[string]time.Time // tracks per-tenant last settings revalidation time
	connectionsCheckInterval time.Duration        // configurable interval between settings revalidation checks

	// revalidateWG tracks in-flight revalidatePoolSettings goroutines so Close()
	// can wait for them to finish before returning. Without this, goroutines
	// spawned by GetConnection may access Manager state after Close() returns.
	revalidateWG sync.WaitGroup
}

type MongoConnection struct {
	// Adapter type used by tenant-manager package; keep fields aligned with
	// tenant-manager migration contract and upstream lib-commons adapter semantics.
	ConnectionStringSource string
	Database               string
	Logger                 log.Logger
	MaxPoolSize            uint64
	DB                     *mongo.Client

	// tlsConfig, when non-nil, is applied to the mongo client options via
	// SetTLSConfig. This is used when separate TLS certificate and key files
	// are provided (tls.LoadX509KeyPair), since the MongoDB URI parameter
	// tlsCertificateKeyFile only accepts a single combined PEM file.
	tlsConfig *tls.Config

	client *mongolib.Client
}

func (c *MongoConnection) Connect(ctx context.Context) error {
	if c == nil {
		return errors.New("mongo connection is nil")
	}

	// When a custom TLS config is required (e.g., separate cert+key files loaded
	// via tls.LoadX509KeyPair), connect directly via the mongo driver so we can
	// call SetTLSConfig on the client options. The mongolib.NewClient path does
	// not expose this capability.
	if c.tlsConfig != nil {
		return c.connectWithTLS(ctx)
	}

	mongoTenantClient, err := mongolib.NewClient(ctx, mongolib.Config{
		URI:         c.ConnectionStringSource,
		Database:    c.Database,
		MaxPoolSize: c.MaxPoolSize,
		Logger:      c.Logger,
	})
	if err != nil {
		return err
	}

	mongoClient, err := mongoTenantClient.Client(ctx)
	if err != nil {
		return err
	}

	c.client = mongoTenantClient
	c.DB = mongoClient

	return nil
}

// connectWithTLS creates a MongoDB client using the raw driver, applying the
// custom TLS configuration via SetTLSConfig. This path is used when separate
// certificate and key files are provided (not a combined PEM).
func (c *MongoConnection) connectWithTLS(ctx context.Context) error {
	clientOptions := options.Client().ApplyURI(c.ConnectionStringSource)

	if c.MaxPoolSize > 0 {
		clientOptions.SetMaxPoolSize(c.MaxPoolSize)
	}

	clientOptions.SetTLSConfig(c.tlsConfig)

	mongoClient, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return fmt.Errorf("mongo connect with TLS failed: %w", err)
	}

	c.DB = mongoClient

	return nil
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
		p.logger = logcompat.New(logger)
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

// WithConnectionsCheckInterval sets the interval between periodic connection pool settings
// revalidation checks. When GetConnection returns a cached connection and this interval
// has elapsed since the last check for that tenant, fresh config is fetched from the
// Tenant Manager asynchronously. For MongoDB, the driver does not support runtime pool
// resize, but revalidation detects suspended/purged tenants and evicts their connections.
//
// If d <= 0, revalidation is DISABLED (connectionsCheckInterval is set to 0).
// When disabled, no async revalidation checks are performed on cache hits.
// Default: 30 seconds (defaultConnectionsCheckInterval).
func WithConnectionsCheckInterval(d time.Duration) Option {
	return func(p *Manager) {
		p.connectionsCheckInterval = max(d, 0)
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
		client:                   c,
		service:                  service,
		logger:                   logcompat.New(nil),
		connections:              make(map[string]*MongoConnection),
		databaseNames:            make(map[string]string),
		lastAccessed:             make(map[string]time.Time),
		lastConnectionsCheck:     make(map[string]time.Time),
		connectionsCheckInterval: defaultConnectionsCheckInterval,
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
func (p *Manager) GetConnection(ctx context.Context, tenantID string) (*mongo.Client, error) { //nolint:gocognit // complexity from connection lifecycle (ping, revalidate, evict) is inherent
	if ctx == nil {
		ctx = context.Background()
	}

	if tenantID == "" {
		return nil, errors.New("tenant ID is required")
	}

	p.mu.RLock()

	if p.closed {
		p.mu.RUnlock()
		return nil, core.ErrManagerClosed
	}

	if conn, ok := p.connections[tenantID]; ok {
		p.mu.RUnlock()

		// Validate cached connection is still healthy (e.g., credentials may have changed).
		// Ping is slow I/O, so we intentionally run it outside any lock.
		if conn.DB != nil {
			pingCtx, cancel := context.WithTimeout(ctx, mongoPingTimeout)
			pingErr := conn.DB.Ping(pingCtx, nil)

			cancel()

			if pingErr != nil {
				if p.logger != nil {
					p.logger.WarnCtx(ctx, fmt.Sprintf("cached mongo connection unhealthy for tenant %s, reconnecting: %v", tenantID, pingErr))
				}

				if closeErr := p.CloseConnection(ctx, tenantID); closeErr != nil && p.logger != nil {
					p.logger.WarnCtx(ctx, fmt.Sprintf("failed to close stale mongo connection for tenant %s: %v", tenantID, closeErr))
				}

				// Connection was unhealthy and has been evicted; create fresh.
				return p.createConnection(ctx, tenantID)
			}

			// Ping succeeded. Re-acquire write lock to update LRU tracking,
			// but re-check that the connection was not evicted while we were
			// pinging (another goroutine may have called CloseConnection,
			// Close, or evictLRU in the meantime).
			now := time.Now()

			p.mu.Lock()
			if current, stillExists := p.connections[tenantID]; stillExists && current == conn {
				p.lastAccessed[tenantID] = now

				shouldRevalidate := p.client != nil && p.connectionsCheckInterval > 0 && time.Since(p.lastConnectionsCheck[tenantID]) > p.connectionsCheckInterval
				if shouldRevalidate {
					p.lastConnectionsCheck[tenantID] = now
					p.revalidateWG.Add(1)
				}

				p.mu.Unlock()

				if shouldRevalidate {
					go func() { //#nosec G118 -- intentional: revalidatePoolSettings creates its own timeout context; must not use request-scoped context as this outlives the request
						defer p.revalidateWG.Done()

						p.revalidatePoolSettings(tenantID)
					}()
				}

				return conn.DB, nil
			}

			p.mu.Unlock()

			// Connection was evicted while we were pinging; fall through
			// to createConnection which will fetch fresh credentials.
			return p.createConnection(ctx, tenantID)
		}

		// conn.DB is nil -- cached entry is unusable, create a new connection.
		return p.createConnection(ctx, tenantID)
	}

	p.mu.RUnlock()

	return p.createConnection(ctx, tenantID)
}

// revalidatePoolSettings fetches fresh config from the Tenant Manager and detects
// whether the tenant has been suspended or purged. It also detects connection-level
// config changes (host, port, database, credentials, TLS settings) and triggers a
// graceful reconnection when they differ from the cached connection.
// This runs asynchronously (in a goroutine) and must never block GetConnection.
// If the fetch fails, a warning is logged but the connection remains usable.
func (p *Manager) revalidatePoolSettings(tenantID string) {
	// Guard: recover from any panic to avoid crashing the process.
	// This goroutine runs asynchronously and must never bring down the service.
	defer func() {
		if r := recover(); r != nil {
			if p.logger != nil {
				p.logger.Warnf("recovered from panic during settings revalidation for tenant %s: %v", tenantID, r)
			}
		}
	}()

	revalidateCtx, cancel := context.WithTimeout(context.Background(), settingsRevalidationTimeout)
	defer cancel()

	config, err := p.client.GetTenantConfig(revalidateCtx, tenantID, p.service, client.WithSkipCache())
	if err != nil {
		// If tenant service was suspended/purged, evict the cached connection immediately.
		// The next request for this tenant will call createConnection, which fetches fresh
		// config from the Tenant Manager and receives the 403 error directly.
		if core.IsTenantSuspendedError(err) {
			if p.logger != nil {
				p.logger.Warnf("tenant %s service suspended, evicting cached connection", tenantID)
			}

			evictCtx, evictCancel := context.WithTimeout(context.Background(), settingsRevalidationTimeout)
			defer evictCancel()

			_ = p.CloseConnection(evictCtx, tenantID)

			return
		}

		if p.logger != nil {
			p.logger.Warnf("failed to revalidate connection settings for tenant %s: %v", tenantID, err)
		}

		return
	}

	// Detect connection-level config changes and trigger graceful reconnection.
	if p.detectAndReconnectMongo(revalidateCtx, tenantID, config) {
		return // reconnection handled; skip no-op ApplyConnectionSettings
	}

	p.ApplyConnectionSettings(tenantID, nil)
}

// detectAndReconnectMongo compares the fresh MongoDB config against the cached
// connection URI and triggers a graceful reconnection if connection-level fields
// have changed (host, port, database, username, password, authSource, TLS settings).
// Returns true if a reconnection was attempted (regardless of success), false if
// no config change was detected.
//
// The reconnection is graceful: the new connection is established first, and the
// old one is replaced and closed only after the new one is ready. If the new
// connection fails, the old one is kept to avoid breaking existing tenants.
func (p *Manager) detectAndReconnectMongo(ctx context.Context, tenantID string, config *core.TenantConfig) bool {
	mongoConfig := config.GetMongoDBConfig(p.service, p.module)
	if mongoConfig == nil {
		return false
	}

	freshURI, err := buildMongoURI(mongoConfig, p.logger)
	if err != nil {
		if p.logger != nil {
			p.logger.Warnf("config change detection: invalid MongoDB URI for tenant %s: %v", tenantID, err)
		}

		return false
	}

	changed := p.hasMongoConfigChanged(tenantID, freshURI, mongoConfig)
	if !changed {
		return false
	}

	// Config changed — attempt graceful reconnection.
	if p.logger != nil {
		p.logger.Infof("tenant %s MongoDB config changed, reconnecting", tenantID)
	}

	return p.reconnectMongo(ctx, tenantID, mongoConfig, freshURI)
}

// hasMongoConfigChanged reads the cached connection under read lock and returns
// true if any connection-level field differs from the fresh config. This covers:
//   - URI (host, port, credentials, authSource, TLS params)
//   - Database name
//   - MaxPoolSize
//   - TLS config presence (separate cert+key files)
func (p *Manager) hasMongoConfigChanged(tenantID, freshURI string, freshConfig *core.MongoDBConfig) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	conn, ok := p.connections[tenantID]
	if !ok || conn == nil {
		return false
	}

	// URI covers host, port, credentials, authSource, and inline TLS params.
	if conn.ConnectionStringSource != freshURI {
		return true
	}

	// Database name may change independently of the URI (e.g., tenant migrated
	// to a different database on the same cluster).
	if cachedDB, hasCachedDB := p.databaseNames[tenantID]; hasCachedDB && cachedDB != freshConfig.Database {
		return true
	}

	// MaxPoolSize change requires a new client (MongoDB driver does not support
	// runtime pool resize). Normalize both to the effective value: 0 means the
	// default was used at creation time.
	freshMaxPool := freshConfig.MaxPoolSize
	if freshMaxPool == 0 {
		freshMaxPool = DefaultMaxConnections
	}

	cachedMaxPool := conn.MaxPoolSize
	if cachedMaxPool == 0 {
		cachedMaxPool = DefaultMaxConnections
	}

	if cachedMaxPool != freshMaxPool {
		return true
	}

	// Detect TLS config changes: separate cert+key files toggled on/off.
	hasFreshSeparateTLS := hasSeparateCertAndKey(freshConfig)
	hasCachedSeparateTLS := conn.tlsConfig != nil

	return hasFreshSeparateTLS != hasCachedSeparateTLS
}

// reconnectMongo builds a new connection from the fresh config and replaces
// the cached one. If the new connection fails, the old one is kept.
// Returns true to indicate a reconnection was attempted.
func (p *Manager) reconnectMongo(ctx context.Context, tenantID string, mongoConfig *core.MongoDBConfig, freshURI string) bool {
	maxConnections := DefaultMaxConnections
	if mongoConfig.MaxPoolSize > 0 {
		maxConnections = mongoConfig.MaxPoolSize
	}

	newConn := &MongoConnection{
		ConnectionStringSource: freshURI,
		Database:               mongoConfig.Database,
		Logger:                 p.logger.Base(),
		MaxPoolSize:            maxConnections,
	}

	// Apply TLS config if separate cert+key files are provided.
	if hasSeparateCertAndKey(mongoConfig) {
		tlsCfg, tlsErr := buildTLSConfigFromFiles(mongoConfig)
		if tlsErr != nil {
			if p.logger != nil {
				p.logger.Warnf("config change: failed to build TLS config for tenant %s, keeping old connection: %v", tenantID, tlsErr)
			}

			return true
		}

		newConn.tlsConfig = tlsCfg
	}

	if err := newConn.Connect(ctx); err != nil {
		if p.logger != nil {
			p.logger.Warnf("config change: failed to connect to new MongoDB for tenant %s, keeping old connection: %v", tenantID, err)
		}

		return true
	}

	// Replace the cached connection under write lock and disconnect the old one.
	if !p.canStoreMongoConnection(ctx, tenantID, newConn) {
		return true
	}

	p.swapMongoConnection(ctx, tenantID, newConn, mongoConfig.Database)

	return true
}

// canStoreMongoConnection acquires the write lock and checks whether the
// manager is still open and the tenant still exists. If not, it discards
// newConn and returns false.
// Caller must NOT hold p.mu.
func (p *Manager) canStoreMongoConnection(ctx context.Context, tenantID string, newConn *MongoConnection) bool {
	p.mu.Lock()

	if p.closed {
		p.mu.Unlock()
		p.disconnectMongo(ctx, newConn, "config change: failed to disconnect new MongoDB for tenant %s after manager closed", tenantID)

		return false
	}

	if _, stillExists := p.connections[tenantID]; !stillExists {
		p.mu.Unlock()
		p.disconnectMongo(ctx, newConn, "config change: failed to disconnect new MongoDB for tenant %s after eviction", tenantID)

		return false
	}

	p.mu.Unlock()

	return true
}

// swapMongoConnection replaces the cached connection with newConn under write
// lock and disconnects the old connection after releasing the lock.
// Caller must NOT hold p.mu.
func (p *Manager) swapMongoConnection(ctx context.Context, tenantID string, newConn *MongoConnection, database string) {
	p.mu.Lock()

	oldConn := p.connections[tenantID]
	p.connections[tenantID] = newConn
	p.databaseNames[tenantID] = database
	p.lastAccessed[tenantID] = time.Now()

	p.mu.Unlock()

	// Disconnect the old connection after releasing the lock.
	if oldConn != nil && oldConn.DB != nil {
		discCtx, discCancel := context.WithTimeout(ctx, mongoPingTimeout)
		if discErr := oldConn.DB.Disconnect(discCtx); discErr != nil && p.logger != nil {
			p.logger.Warnf("config change: failed to disconnect old MongoDB for tenant %s: %v", tenantID, discErr)
		}

		discCancel()
	}

	if p.logger != nil {
		p.logger.Infof("tenant %s MongoDB connection replaced with updated config", tenantID)
	}
}

// disconnectMongo disconnects a MongoDB connection and logs a warning on failure.
func (p *Manager) disconnectMongo(ctx context.Context, conn *MongoConnection, msgFmt string, tenantID string) {
	discCtx, discCancel := context.WithTimeout(ctx, mongoPingTimeout)
	if discErr := conn.DB.Disconnect(discCtx); discErr != nil && p.logger != nil {
		p.logger.Warnf(msgFmt+": %v", tenantID, discErr)
	}

	discCancel()
}

// createConnection fetches config from Tenant Manager and creates a MongoDB client.
func (p *Manager) createConnection(ctx context.Context, tenantID string) (*mongo.Client, error) {
	if p.client == nil {
		return nil, errors.New("tenant manager client is required for multi-tenant connections")
	}

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "mongo.create_connection")
	defer span.End()

	// Check for a cached connection under the write lock, but perform
	// network I/O (Ping / Disconnect) outside the lock to avoid blocking
	// other goroutines on slow network calls.
	cachedConn, hasCached, err := p.snapshotCachedConnection(tenantID)
	if err != nil {
		return nil, err
	}

	if hasCached {
		if reusedDB, reused := p.tryReuseCachedConnection(ctx, tenantID, cachedConn); reused {
			return reusedDB, nil
		}
	}

	return p.buildAndCacheNewConnection(ctx, tenantID, logger, span)
}

// snapshotCachedConnection reads the cached connection for tenantID under a
// short lock and returns whether the manager is closed.
func (p *Manager) snapshotCachedConnection(tenantID string) (*MongoConnection, bool, error) {
	p.mu.Lock()
	cachedConn, hasCached := p.connections[tenantID]
	closed := p.closed
	p.mu.Unlock()

	if closed {
		return nil, false, core.ErrManagerClosed
	}

	return cachedConn, hasCached, nil
}

// tryReuseCachedConnection validates a previously cached connection by pinging it.
// If the connection is healthy and still in the cache, it updates the LRU timestamp
// and returns it. If unhealthy or evicted, it cleans up and returns reused=false so
// the caller falls through to create a new connection.
func (p *Manager) tryReuseCachedConnection(
	ctx context.Context,
	tenantID string,
	cachedConn *MongoConnection,
) (*mongo.Client, bool) {
	if cachedConn == nil || cachedConn.DB == nil {
		p.removeStaleCacheEntry(tenantID, cachedConn)

		return nil, false
	}

	pingCtx, cancel := context.WithTimeout(ctx, mongoPingTimeout)
	pingErr := cachedConn.DB.Ping(pingCtx, nil)

	cancel()

	if pingErr == nil {
		return p.reuseHealthyConnection(tenantID, cachedConn)
	}

	p.disconnectUnhealthyConnection(ctx, tenantID, cachedConn, pingErr)

	return nil, false
}

// reuseHealthyConnection updates the LRU timestamp for a healthy cached connection.
// Returns (client, true) if the entry still exists in the cache, or (nil, false) if
// it was evicted while we were pinging.
func (p *Manager) reuseHealthyConnection(tenantID string, cachedConn *MongoConnection) (*mongo.Client, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if current, stillExists := p.connections[tenantID]; stillExists && current == cachedConn {
		p.lastAccessed[tenantID] = time.Now()

		return cachedConn.DB, true
	}

	return nil, false
}

// disconnectUnhealthyConnection disconnects a cached connection that failed its
// health check and removes the stale cache entry.
func (p *Manager) disconnectUnhealthyConnection(
	ctx context.Context,
	tenantID string,
	cachedConn *MongoConnection,
	pingErr error,
) {
	if p.logger != nil {
		p.logger.WarnCtx(ctx, fmt.Sprintf("cached mongo connection unhealthy for tenant %s, reconnecting: %v", tenantID, pingErr))
	}

	discCtx, discCancel := context.WithTimeout(ctx, mongoPingTimeout)
	if discErr := cachedConn.DB.Disconnect(discCtx); discErr != nil && p.logger != nil {
		p.logger.WarnCtx(ctx, fmt.Sprintf("failed to disconnect unhealthy mongo connection for tenant %s: %v", tenantID, discErr))
	}

	discCancel()

	p.removeStaleCacheEntry(tenantID, cachedConn)
}

// removeStaleCacheEntry removes a cache entry only if it still points to the
// same connection reference (not replaced by another goroutine).
func (p *Manager) removeStaleCacheEntry(tenantID string, cachedConn *MongoConnection) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if current, ok := p.connections[tenantID]; ok && current == cachedConn {
		delete(p.connections, tenantID)
		delete(p.databaseNames, tenantID)
		delete(p.lastAccessed, tenantID)
		delete(p.lastConnectionsCheck, tenantID)
	}
}

// buildAndCacheNewConnection fetches tenant config, builds a new MongoDB client,
// and caches it.
func (p *Manager) buildAndCacheNewConnection(
	ctx context.Context,
	tenantID string,
	logger *logcompat.Logger,
	span trace.Span,
) (*mongo.Client, error) {
	mongoConfig, err := p.getMongoConfigForTenant(ctx, tenantID, logger, span)
	if err != nil {
		return nil, err
	}

	uri, err := buildMongoURI(mongoConfig, logger)
	if err != nil {
		return nil, err
	}

	maxConnections := DefaultMaxConnections
	if mongoConfig.MaxPoolSize > 0 {
		maxConnections = mongoConfig.MaxPoolSize
	}

	conn := &MongoConnection{
		ConnectionStringSource: uri,
		Database:               mongoConfig.Database,
		Logger:                 p.logger.Base(),
		MaxPoolSize:            maxConnections,
	}

	// When separate TLS certificate and key files are provided, load the
	// X.509 key pair and build a *tls.Config for the connection. The URI
	// does not include tlsCertificateKeyFile in this case (see buildMongoQueryParams).
	if hasSeparateCertAndKey(mongoConfig) {
		tlsCfg, tlsErr := buildTLSConfigFromFiles(mongoConfig)
		if tlsErr != nil {
			logger.ErrorfCtx(ctx, "failed to build TLS config for tenant %s: %v", tenantID, tlsErr)
			libOpentelemetry.HandleSpanError(span, "failed to build TLS config", tlsErr)

			return nil, fmt.Errorf("failed to build TLS config: %w", tlsErr)
		}

		conn.tlsConfig = tlsCfg
	}

	if err := conn.Connect(ctx); err != nil {
		logger.ErrorfCtx(ctx, "failed to connect to MongoDB for tenant %s: %v", tenantID, err)
		libOpentelemetry.HandleSpanError(span, "failed to connect to MongoDB", err)

		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	logger.InfofCtx(ctx, "MongoDB connection created for tenant %s (database: %s)", tenantID, mongoConfig.Database)

	return p.cacheConnection(ctx, tenantID, conn, mongoConfig.Database, logger.Base())
}

func (p *Manager) getMongoConfigForTenant(
	ctx context.Context,
	tenantID string,
	logger *logcompat.Logger,
	span trace.Span,
) (*core.MongoDBConfig, error) {
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		var suspErr *core.TenantSuspendedError
		if errors.As(err, &suspErr) {
			logger.WarnfCtx(ctx, "tenant service is %s: tenantID=%s", suspErr.Status, tenantID)
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant service suspended", err)

			return nil, err
		}

		logger.ErrorfCtx(ctx, "failed to get tenant config: %v", err)
		libOpentelemetry.HandleSpanError(span, "failed to get tenant config", err)

		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	mongoConfig := config.GetMongoDBConfig(p.service, p.module)
	if mongoConfig == nil {
		logger.ErrorfCtx(ctx, "no MongoDB config for tenant %s service %s module %s", tenantID, p.service, p.module)

		return nil, core.ErrServiceNotConfigured
	}

	return mongoConfig, nil
}

func (p *Manager) cacheConnection(
	ctx context.Context,
	tenantID string,
	conn *MongoConnection,
	databaseName string,
	baseLogger log.Logger,
) (*mongo.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		if conn.DB != nil {
			if discErr := conn.DB.Disconnect(ctx); discErr != nil && p.logger != nil {
				p.logger.Base().Log(ctx, log.LevelWarn, "failed to disconnect mongo connection on closed manager",
					log.String("tenant_id", tenantID),
					log.Err(discErr),
				)
			}
		}

		return nil, core.ErrManagerClosed
	}

	if cached, ok := p.connections[tenantID]; ok && cached != nil && cached.DB != nil {
		if conn.DB != nil {
			if discErr := conn.DB.Disconnect(ctx); discErr != nil && p.logger != nil {
				p.logger.Base().Log(ctx, log.LevelWarn, "failed to disconnect excess mongo connection",
					log.String("tenant_id", tenantID),
					log.Err(discErr),
				)
			}
		}

		p.lastAccessed[tenantID] = time.Now()

		return cached.DB, nil
	}

	p.evictLRU(ctx, baseLogger)

	p.connections[tenantID] = conn
	p.databaseNames[tenantID] = databaseName
	p.lastAccessed[tenantID] = time.Now()

	return conn.DB, nil
}

// evictLRU removes the least recently used idle connection when the pool reaches the
// soft limit. Only connections that have been idle longer than the idle timeout are
// eligible for eviction. If all connections are active (used within the idle timeout),
// the pool is allowed to grow beyond the soft limit.
// Caller MUST hold p.mu write lock.
func (p *Manager) evictLRU(ctx context.Context, logger log.Logger) {
	candidateID, shouldEvict := eviction.FindLRUEvictionCandidate(
		len(p.connections), p.maxConnections, p.lastAccessed, p.idleTimeout, logger,
	)
	if !shouldEvict {
		return
	}

	// Manager-specific cleanup: disconnect the MongoDB client and remove from all maps.
	if conn, ok := p.connections[candidateID]; ok {
		if conn.DB != nil {
			if discErr := conn.DB.Disconnect(ctx); discErr != nil {
				if logger != nil {
					logger.Log(ctx, log.LevelWarn,
						"failed to disconnect evicted mongo connection",
						log.String("tenant_id", candidateID),
						log.String("error", discErr.Error()),
					)
				}
			}
		}

		delete(p.connections, candidateID)
		delete(p.databaseNames, candidateID)
		delete(p.lastAccessed, candidateID)
		delete(p.lastConnectionsCheck, candidateID)
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

// GetDatabaseForTenant returns the MongoDB database for a tenant by resolving
// the database name from the cached mapping populated during createConnection.
// This avoids a redundant HTTP call to the Tenant Manager since the database
// name is already known from the initial connection setup.
func (p *Manager) GetDatabaseForTenant(ctx context.Context, tenantID string) (*mongo.Database, error) {
	if tenantID == "" {
		return nil, errors.New("tenant ID is required")
	}

	// GetConnection handles config fetching and caches both the connection
	// and the database name (in p.databaseNames).
	mongoClient, err := p.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Look up the database name cached during createConnection.
	p.mu.RLock()
	dbName, ok := p.databaseNames[tenantID]
	p.mu.RUnlock()

	if ok {
		return mongoClient.Database(dbName), nil
	}

	// Fallback: database name not cached (e.g., connection was pre-populated
	// outside createConnection). Fetch config as a last resort.
	if p.client == nil {
		return nil, errors.New("tenant manager client is required for multi-tenant connections")
	}

	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		// Propagate TenantSuspendedError directly so the middleware can
		// return a specific 403 response instead of a generic 503.
		if core.IsTenantSuspendedError(err) {
			return nil, err
		}

		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	mongoConfig := config.GetMongoDBConfig(p.service, p.module)
	if mongoConfig == nil {
		return nil, core.ErrServiceNotConfigured
	}

	// Cache for future calls
	p.mu.Lock()
	p.databaseNames[tenantID] = mongoConfig.Database
	p.mu.Unlock()

	return mongoClient.Database(mongoConfig.Database), nil
}

// Close closes all MongoDB connections.
// It waits for any in-flight revalidatePoolSettings goroutines to finish
// before returning, preventing goroutine leaks and use-after-close races.
//
// Uses snapshot-then-cleanup to avoid holding the mutex during network I/O
// (Disconnect calls), which could block other goroutines on slow networks.
func (p *Manager) Close(ctx context.Context) error {
	// Phase 1: Under lock — mark closed, snapshot all connections, clear maps.
	p.mu.Lock()
	p.closed = true

	snapshot := make([]*MongoConnection, 0, len(p.connections))
	for _, conn := range p.connections {
		snapshot = append(snapshot, conn)
	}

	// Clear all maps while still under lock.
	clear(p.connections)
	clear(p.databaseNames)
	clear(p.lastAccessed)
	clear(p.lastConnectionsCheck)

	p.mu.Unlock()

	// Phase 2: Outside lock — disconnect each snapshotted connection.
	var errs []error

	for _, conn := range snapshot {
		if conn.DB != nil {
			if err := conn.DB.Disconnect(ctx); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// Phase 3: Wait for in-flight revalidatePoolSettings goroutines OUTSIDE the lock.
	// revalidatePoolSettings acquires p.mu internally (via CloseConnection),
	// so waiting with the lock held would deadlock.
	p.revalidateWG.Wait()

	return errors.Join(errs...)
}

// CloseConnection closes the MongoDB client for a specific tenant.
//
// Uses snapshot-then-cleanup to avoid holding the mutex during Disconnect,
// which performs network I/O and could block other goroutines.
func (p *Manager) CloseConnection(ctx context.Context, tenantID string) error {
	// Step 1: Under lock — remove entry from maps, capture the connection.
	p.mu.Lock()

	conn, ok := p.connections[tenantID]
	if !ok {
		p.mu.Unlock()
		return nil
	}

	delete(p.connections, tenantID)
	delete(p.databaseNames, tenantID)
	delete(p.lastAccessed, tenantID)
	delete(p.lastConnectionsCheck, tenantID)

	p.mu.Unlock()

	// Step 2: Outside lock — disconnect the captured connection.
	if conn.DB != nil {
		return conn.DB.Disconnect(ctx)
	}

	return nil
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
//
// The function uses net/url.URL to construct the URI, which guarantees that
// all components (credentials, host, database, query parameters) are properly
// escaped according to RFC 3986. This prevents injection of URI control
// characters through tenant-supplied configuration values.
func buildMongoURI(cfg *core.MongoDBConfig, logger *logcompat.Logger) (string, error) {
	if cfg.URI != "" {
		return validateAndReturnRawURI(cfg.URI, logger)
	}

	if err := validateMongoHostPort(cfg); err != nil {
		return "", err
	}

	u := buildMongoBaseURL(cfg)
	query := buildMongoQueryParams(cfg)

	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}

	return u.String(), nil
}

// validateAndReturnRawURI validates and returns a raw MongoDB URI when provided directly.
func validateAndReturnRawURI(uri string, logger *logcompat.Logger) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("invalid mongo URI: %w", err)
	}

	if parsed.Scheme != "mongodb" && parsed.Scheme != "mongodb+srv" {
		return "", fmt.Errorf("invalid mongo URI scheme %q", parsed.Scheme)
	}

	if logger != nil {
		logger.Warn("using raw mongodb URI from tenant configuration")
	}

	return uri, nil
}

// validateMongoHostPort validates that host and port are present when no URI is provided.
func validateMongoHostPort(cfg *core.MongoDBConfig) error {
	if cfg.Host == "" {
		return errors.New("mongo host is required when URI is not provided")
	}

	if cfg.Port == 0 {
		return errors.New("mongo port is required when URI is not provided")
	}

	return nil
}

// buildMongoBaseURL constructs the base MongoDB URL with scheme, host, credentials, and database path.
func buildMongoBaseURL(cfg *core.MongoDBConfig) *url.URL {
	u := &url.URL{
		Scheme: "mongodb",
		Host:   cfg.Host + ":" + strconv.Itoa(cfg.Port),
	}

	if cfg.Username != "" && cfg.Password != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}

	if cfg.Database != "" {
		u.Path = "/" + cfg.Database
		u.RawPath = "/" + url.PathEscape(cfg.Database)
	} else {
		u.Path = "/"
	}

	return u
}

// hasSeparateCertAndKey returns true when TLS is enabled and the config provides
// distinct certificate and key files (not a single combined PEM).
func hasSeparateCertAndKey(cfg *core.MongoDBConfig) bool {
	return cfg.TLS && cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" && cfg.TLSCertFile != cfg.TLSKeyFile
}

// buildTLSConfigFromFiles creates a *tls.Config by loading the X.509 key pair
// from separate certificate and private-key files. When a CA file is provided
// it is added to the root CA pool. When TLSSkipVerify is true, both certificate
// chain validation and hostname verification are skipped.
func buildTLSConfigFromFiles(cfg *core.MongoDBConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS certificate key pair: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	if cfg.TLSCAFile != "" {
		caCert, readErr := os.ReadFile(cfg.TLSCAFile)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read CA certificate file: %w", readErr)
		}

		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate from %s", cfg.TLSCAFile)
		}

		tlsCfg.RootCAs = caPool
	}

	if cfg.TLSSkipVerify {
		tlsCfg.InsecureSkipVerify = true //#nosec G402 -- controlled by explicit config flag
	}

	return tlsCfg, nil
}

// buildMongoQueryParams builds the query parameters for the MongoDB URI.
// Defaults authSource to "admin" when database and credentials are present
// but no explicit authSource is configured, preserving backward compatibility
// with deployments where users are created in the "admin" database.
//
// When TLS is enabled in the config, the corresponding query parameters are added:
//   - tls=true enables TLS on the connection
//   - tlsCAFile points to the CA certificate (only added when cert+key are NOT separate files)
//   - tlsCertificateKeyFile points to a combined PEM file (only when a single file is provided)
//   - tlsInsecure=true skips server certificate verification (not for production)
//
// When both TLSCertFile and TLSKeyFile are provided as distinct files, they are
// NOT added to the URI; instead, buildTLSConfigFromFiles is used to load the
// X.509 key pair and the resulting *tls.Config is applied via SetTLSConfig.
func buildMongoQueryParams(cfg *core.MongoDBConfig) url.Values {
	query := url.Values{}

	if cfg.AuthSource != "" {
		query.Set("authSource", cfg.AuthSource)
	} else if cfg.Database != "" && cfg.Username != "" {
		query.Set("authSource", "admin")
	}

	if cfg.DirectConnection {
		query.Set("directConnection", "true")
	}

	if cfg.TLS {
		query.Set("tls", "true")

		// When separate cert+key files are provided, TLS configuration is
		// handled via tls.LoadX509KeyPair + SetTLSConfig (not URI params).
		// CA, client cert, and insecure settings are all set programmatically
		// in that case, so we skip adding them to the URI entirely.
		if hasSeparateCertAndKey(cfg) {
			return query
		}

		if cfg.TLSCAFile != "" {
			query.Set("tlsCAFile", cfg.TLSCAFile)
		}

		if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
			// MongoDB driver uses a single PEM file containing both the client
			// certificate and the private key via the tlsCertificateKeyFile option.
			// When only one is provided, we use it directly since it may be a combined PEM.
			certKeyFile := cfg.TLSCertFile
			if certKeyFile == "" {
				certKeyFile = cfg.TLSKeyFile
			}

			query.Set("tlsCertificateKeyFile", certKeyFile)
		}

		if cfg.TLSSkipVerify {
			query.Set("tlsInsecure", "true")
		}
	}

	return query
}
