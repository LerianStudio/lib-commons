package rabbitmq

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/eviction"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	amqp "github.com/rabbitmq/amqp091-go"
)

// defaultConnectionsCheckInterval is the default interval between periodic
// connection settings revalidation checks. When a cached connection is
// returned by GetConnection and this interval has elapsed since the last check,
// fresh config is fetched from the Tenant Manager asynchronously.
const defaultConnectionsCheckInterval = 30 * time.Second

// settingsRevalidationTimeout is the maximum duration for the HTTP call
// to the Tenant Manager during async settings revalidation.
const settingsRevalidationTimeout = 5 * time.Second

// Manager manages RabbitMQ connections per tenant.
// Each tenant has a dedicated vhost, user, and credentials stored in Tenant Manager.
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
	connections    map[string]*amqp.Connection
	cachedURIs     map[string]string // tenantID -> last known connection URI (for change detection)
	closed         bool
	maxConnections int                  // soft limit for pool size (0 = unlimited)
	idleTimeout    time.Duration        // how long before a connection is eligible for eviction
	lastAccessed   map[string]time.Time // LRU tracking per tenant
	useTLS         bool                 // use amqps:// scheme instead of amqp://

	lastConnectionsCheck     map[string]time.Time // tracks per-tenant last settings revalidation time
	connectionsCheckInterval time.Duration        // configurable interval between settings revalidation checks

	// revalidateWG tracks in-flight revalidatePoolSettings goroutines so Close()
	// can wait for them to finish before returning. Without this, goroutines
	// spawned by GetConnection may access Manager state after Close() returns.
	revalidateWG sync.WaitGroup
}

// Option configures a Manager.
type Option func(*Manager)

// WithModule sets the module name for the RabbitMQ manager.
func WithModule(module string) Option {
	return func(p *Manager) {
		p.module = module
	}
}

// WithLogger sets the logger for the RabbitMQ manager.
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

// WithConnectionsCheckInterval sets the interval between periodic connection settings
// revalidation checks. When GetConnection returns a cached connection and this interval
// has elapsed since the last check for that tenant, fresh config is fetched from the
// Tenant Manager asynchronously to detect suspended tenants and config changes.
//
// If d <= 0, revalidation is DISABLED (connectionsCheckInterval is set to 0).
// When disabled, no async revalidation checks are performed on cache hits.
// Default: 30 seconds (defaultConnectionsCheckInterval).
func WithConnectionsCheckInterval(d time.Duration) Option {
	return func(p *Manager) {
		p.connectionsCheckInterval = max(d, 0)
	}
}

// WithTLS enables TLS connections (amqps:// scheme) instead of the default
// plaintext amqp://. Use this for production deployments where RabbitMQ is
// configured with TLS certificates.
func WithTLS() Option {
	return func(p *Manager) {
		p.useTLS = true
	}
}

// NewManager creates a new RabbitMQ connection manager.
// Parameters:
//   - c: The Tenant Manager client for fetching tenant configurations
//   - service: The service name (e.g., "ledger")
//   - opts: Optional configuration options
func NewManager(c *client.Client, service string, opts ...Option) *Manager {
	p := &Manager{
		client:                c,
		service:               service,
		logger:                logcompat.New(nil),
		connections:           make(map[string]*amqp.Connection),
		cachedURIs:            make(map[string]string),
		lastAccessed:          make(map[string]time.Time),
		lastConnectionsCheck:     make(map[string]time.Time),
		connectionsCheckInterval: defaultConnectionsCheckInterval,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// GetConnection returns a RabbitMQ connection for the tenant.
// Creates a new connection if one doesn't exist or the existing one is closed.
func (p *Manager) GetConnection(ctx context.Context, tenantID string) (*amqp.Connection, error) {
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

	if conn, ok := p.connections[tenantID]; ok && !conn.IsClosed() {
		p.mu.RUnlock()

		// Update LRU tracking on cache hit and check if settings revalidation is due
		now := time.Now()

		p.mu.Lock()
		// Re-read connection from map (may have been evicted and closed between locks)
		if refreshedConn, still := p.connections[tenantID]; still && !refreshedConn.IsClosed() {
			p.lastAccessed[tenantID] = now

			// Only revalidate if connectionsCheckInterval > 0 (means revalidation is enabled)
			shouldRevalidate := p.client != nil && p.connectionsCheckInterval > 0 && time.Since(p.lastConnectionsCheck[tenantID]) > p.connectionsCheckInterval
			if shouldRevalidate {
				// Update timestamp BEFORE spawning goroutine to prevent multiple
				// concurrent revalidation checks for the same tenant.
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

			return refreshedConn, nil
		}

		p.mu.Unlock()

		// Connection was evicted between RUnlock and Lock; create a new one
		_ = conn // original reference is now potentially stale; discard it

		return p.createConnection(ctx, tenantID)
	}

	p.mu.RUnlock()

	return p.createConnection(ctx, tenantID)
}

// createConnection fetches config from Tenant Manager and creates a RabbitMQ connection.
//
// Network I/O (GetTenantConfig, amqp.Dial) is performed outside the mutex to
// avoid blocking other goroutines on slow network calls. The pattern is:
//  1. Under lock: double-check cache, check closed state
//  2. Outside lock: fetch config and dial
//  3. Re-acquire lock: evict LRU, cache new connection (with race-loss handling)
func (p *Manager) createConnection(ctx context.Context, tenantID string) (*amqp.Connection, error) {
	if p.client == nil {
		return nil, errors.New("tenant manager client is required for multi-tenant connections")
	}

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "rabbitmq.create_connection")
	defer span.End()

	if p.logger != nil {
		logger = p.logger
	}

	// Step 1: Under lock — double-check if connection exists or manager is closed.
	p.mu.Lock()

	if conn, ok := p.connections[tenantID]; ok && !conn.IsClosed() {
		p.mu.Unlock()
		return conn, nil
	}

	if p.closed {
		p.mu.Unlock()
		return nil, core.ErrManagerClosed
	}

	p.mu.Unlock()

	// Step 2: Outside lock — perform network I/O (HTTP call + TCP dial).
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		logger.Errorf("failed to get tenant config: %v", err)
		libOpentelemetry.HandleSpanError(span, "failed to get tenant config", err)

		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	rabbitConfig := config.GetRabbitMQConfig()
	if rabbitConfig == nil {
		logger.Errorf("RabbitMQ not configured for tenant: %s", tenantID)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "RabbitMQ not configured", core.ErrServiceNotConfigured)

		return nil, core.ErrServiceNotConfigured
	}

	// Resolve TLS: per-tenant config takes precedence over global WithTLS() setting.
	useTLS := p.resolveTLS(rabbitConfig)
	uri := buildRabbitMQURI(rabbitConfig, useTLS)

	logger.Infof("connecting to RabbitMQ vhost: tenant=%s, vhost=%s, tls=%v", tenantID, rabbitConfig.VHost, useTLS)

	conn, err := p.dialRabbitMQ(uri, useTLS, rabbitConfig.TLSCAFile)
	if err != nil {
		logger.Errorf("failed to connect to RabbitMQ: %v", err)
		libOpentelemetry.HandleSpanError(span, "failed to connect to RabbitMQ", err)

		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	// Step 3: Re-acquire lock — evict LRU, cache connection (with race-loss check).
	p.mu.Lock()

	// If manager was closed while we were dialing, discard the new connection.
	if p.closed {
		p.mu.Unlock()

		if closeErr := conn.Close(); closeErr != nil {
			logger.Errorf("failed to close RabbitMQ connection on closed manager: %v", closeErr)
		}

		return nil, core.ErrManagerClosed
	}

	// If another goroutine cached a connection for this tenant while we were
	// dialing, use the cached one and discard ours.
	if cached, ok := p.connections[tenantID]; ok && !cached.IsClosed() {
		p.lastAccessed[tenantID] = time.Now()
		p.mu.Unlock()

		if closeErr := conn.Close(); closeErr != nil {
			logger.Errorf("failed to close excess RabbitMQ connection for tenant %s: %v", tenantID, closeErr)
		}

		return cached, nil
	}

	// Evict least recently used connection if pool is full
	p.evictLRU(logger.Base())

	// Cache our new connection and its URI for config change detection.
	// Include TLSCAFile in the cached key so CA file changes trigger reconnection.
	cachedKey := uri
	if rabbitConfig.TLSCAFile != "" {
		cachedKey += "|ca=" + rabbitConfig.TLSCAFile
	}

	p.connections[tenantID] = conn
	p.cachedURIs[tenantID] = cachedKey
	p.lastAccessed[tenantID] = time.Now()

	p.mu.Unlock()

	logger.Infof("RabbitMQ connection created: tenant=%s, vhost=%s", tenantID, rabbitConfig.VHost)

	return conn, nil
}

// evictLRU removes the least recently used idle connection when the pool reaches the
// soft limit. Only connections that have been idle longer than the idle timeout are
// eligible for eviction. If all connections are active (used within the idle timeout),
// the pool is allowed to grow beyond the soft limit.
// Caller MUST hold p.mu write lock.
func (p *Manager) evictLRU(logger log.Logger) {
	candidateID, shouldEvict := eviction.FindLRUEvictionCandidate(
		len(p.connections), p.maxConnections, p.lastAccessed, p.idleTimeout, logger,
	)
	if !shouldEvict {
		return
	}

	// Manager-specific cleanup: close the AMQP connection and remove from maps.
	if conn, ok := p.connections[candidateID]; ok {
		if conn != nil && !conn.IsClosed() {
			if err := conn.Close(); err != nil && logger != nil {
				logger.Log(context.Background(), log.LevelWarn, "failed to close evicted rabbitmq connection",
					log.String("tenant_id", candidateID),
					log.Err(err),
				)
			}
		}

		delete(p.connections, candidateID)
		delete(p.cachedURIs, candidateID)
		delete(p.lastAccessed, candidateID)
		delete(p.lastConnectionsCheck, candidateID)
	}
}

// GetChannel returns a RabbitMQ channel for the tenant.
// Creates a new connection if one doesn't exist.
//
// Channel ownership: The caller is responsible for closing the returned channel
// when it is no longer needed. Failing to close channels will leak resources
// on both the client and the RabbitMQ server.
func (p *Manager) GetChannel(ctx context.Context, tenantID string) (*amqp.Channel, error) {
	conn, err := p.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	channel, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	return channel, nil
}

// Close closes all RabbitMQ connections.
// It waits for any in-flight revalidatePoolSettings goroutines to finish
// before returning, preventing goroutine leaks and use-after-close races.
func (p *Manager) Close(_ context.Context) error {
	// Phase 1: Under lock, mark closed and close all connections.
	p.mu.Lock()

	p.closed = true

	var errs []error

	for tenantID, conn := range p.connections {
		if conn != nil && !conn.IsClosed() {
			if err := conn.Close(); err != nil {
				errs = append(errs, err)
			}
		}

		delete(p.connections, tenantID)
		delete(p.cachedURIs, tenantID)
		delete(p.lastAccessed, tenantID)
		delete(p.lastConnectionsCheck, tenantID)
	}

	p.mu.Unlock()

	// Phase 2: Wait for in-flight revalidatePoolSettings goroutines OUTSIDE the lock.
	// revalidatePoolSettings acquires p.mu internally (via CloseConnection),
	// so waiting with the lock held would deadlock.
	p.revalidateWG.Wait()

	return errors.Join(errs...)
}

// CloseConnection closes the RabbitMQ connection for a specific tenant.
func (p *Manager) CloseConnection(_ context.Context, tenantID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	conn, ok := p.connections[tenantID]
	if !ok {
		return nil
	}

	var err error
	if conn != nil && !conn.IsClosed() {
		err = conn.Close()
	}

	delete(p.connections, tenantID)
	delete(p.cachedURIs, tenantID)
	delete(p.lastAccessed, tenantID)
	delete(p.lastConnectionsCheck, tenantID)

	return err
}

// ApplyConnectionSettings is a no-op for RabbitMQ connections.
// RabbitMQ does not support dynamic connection pool settings like databases do.
// This method exists to satisfy a common manager interface.
func (p *Manager) ApplyConnectionSettings(_ string, _ *core.TenantConfig) {
	// no-op: RabbitMQ connections do not have adjustable pool settings.
}

// revalidatePoolSettings fetches fresh config from the Tenant Manager and detects
// whether the tenant has been suspended or purged. It also detects connection-level
// config changes (host, port, vhost, credentials, TLS settings) and triggers a
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
		if core.IsTenantSuspendedError(err) {
			if p.logger != nil {
				p.logger.Warnf("tenant %s service suspended, evicting cached connection", tenantID)
			}

			_ = p.CloseConnection(context.Background(), tenantID)

			return
		}

		if p.logger != nil {
			p.logger.Warnf("failed to revalidate connection settings for tenant %s: %v", tenantID, err)
		}

		return
	}

	// Detect connection-level config changes and trigger graceful reconnection.
	p.detectAndReconnectRabbitMQ(tenantID, config)
}

// detectAndReconnectRabbitMQ compares the fresh RabbitMQ config against the cached
// connection URI and triggers a graceful reconnection if connection-level fields
// have changed (host, port, vhost, username, password, TLS settings).
//
// The reconnection is graceful: the new connection is established first, and the
// old one is replaced and closed only after the new one is ready. If the new
// connection fails, the old one is kept to avoid breaking existing tenants.
func (p *Manager) detectAndReconnectRabbitMQ(tenantID string, config *core.TenantConfig) {
	rabbitConfig := config.GetRabbitMQConfig()
	if rabbitConfig == nil {
		return
	}

	useTLS := p.resolveTLS(rabbitConfig)
	freshURI := buildRabbitMQURI(rabbitConfig, useTLS)

	// Read the cached URI under read lock.
	p.mu.RLock()
	cachedURI, hasCachedURI := p.cachedURIs[tenantID]

	if !hasCachedURI {
		p.mu.RUnlock()
		return
	}

	p.mu.RUnlock()

	// The URI covers host, port, vhost, credentials, and TLS scheme. The TLS CA
	// file path is not part of the URI, so we compare it separately by appending
	// a sentinel to the cached key. This way, a CA file change triggers reconnection.
	freshKey := freshURI
	if rabbitConfig.TLSCAFile != "" {
		freshKey += "|ca=" + rabbitConfig.TLSCAFile
	}

	if cachedURI == freshKey {
		return // no connection-level change
	}

	// Config changed — attempt graceful reconnection.
	if p.logger != nil {
		p.logger.Infof("tenant %s RabbitMQ config changed, reconnecting", tenantID)
	}

	newConn, err := p.dialRabbitMQ(freshURI, useTLS, rabbitConfig.TLSCAFile)
	if err != nil {
		if p.logger != nil {
			p.logger.Warnf("config change: failed to connect to new RabbitMQ for tenant %s, keeping old connection: %v", tenantID, err)
		}

		return
	}

	// Replace the cached connection under write lock and close the old one.
	if !p.canStoreRabbitMQConnection(tenantID, newConn) {
		return
	}

	p.swapRabbitMQConnection(tenantID, newConn, freshKey)
}

// canStoreRabbitMQConnection acquires the write lock and checks whether the
// manager is still open and the tenant still exists. If not, it discards
// newConn and returns false.
// Caller must NOT hold p.mu.
func (p *Manager) canStoreRabbitMQConnection(tenantID string, newConn *amqp.Connection) bool {
	p.mu.Lock()

	if p.closed {
		p.mu.Unlock()
		p.closeRabbitMQConn(newConn, "config change: failed to close new RabbitMQ connection for tenant %s after manager closed", tenantID)

		return false
	}

	if _, stillExists := p.connections[tenantID]; !stillExists {
		p.mu.Unlock()
		p.closeRabbitMQConn(newConn, "config change: failed to close new RabbitMQ connection for tenant %s after eviction", tenantID)

		return false
	}

	p.mu.Unlock()

	return true
}

// swapRabbitMQConnection replaces the cached connection with newConn under
// write lock and closes the old connection after releasing the lock.
// Caller must NOT hold p.mu.
func (p *Manager) swapRabbitMQConnection(tenantID string, newConn *amqp.Connection, freshKey string) {
	p.mu.Lock()

	oldConn := p.connections[tenantID]
	p.connections[tenantID] = newConn
	p.cachedURIs[tenantID] = freshKey
	p.lastAccessed[tenantID] = time.Now()

	p.mu.Unlock()

	// Close the old connection after releasing the lock.
	p.closeRabbitMQConn(oldConn, "config change: failed to close old RabbitMQ connection for tenant %s", tenantID)

	if p.logger != nil {
		p.logger.Infof("tenant %s RabbitMQ connection replaced with updated config", tenantID)
	}
}

// closeRabbitMQConn closes an AMQP connection and logs a warning on failure.
func (p *Manager) closeRabbitMQConn(conn *amqp.Connection, msgFmt string, tenantID string) {
	if conn == nil || conn.IsClosed() {
		return
	}

	if closeErr := conn.Close(); closeErr != nil && p.logger != nil {
		p.logger.Warnf(msgFmt+": %v", tenantID, closeErr)
	}
}

// Stats returns connection statistics.
//
// ActiveConnections counts connections that are not closed.
// Unlike Postgres/Mongo which use recency-based idle timeout to determine
// whether a connection is "active", RabbitMQ checks actual connection liveness
// because AMQP connections are long-lived and do not have a meaningful
// "last accessed" recency signal for activity classification.
func (p *Manager) Stats() Stats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	tenantIDs := make([]string, 0, len(p.connections))
	activeConnections := 0

	for id, conn := range p.connections {
		tenantIDs = append(tenantIDs, id)

		if conn != nil && !conn.IsClosed() {
			activeConnections++
		}
	}

	return Stats{
		TotalConnections:  len(p.connections),
		MaxConnections:    p.maxConnections,
		ActiveConnections: activeConnections,
		TenantIDs:         tenantIDs,
		Closed:            p.closed,
	}
}

// Stats contains statistics for the RabbitMQ manager.
type Stats struct {
	TotalConnections  int      `json:"totalConnections"`
	MaxConnections    int      `json:"maxConnections"`
	ActiveConnections int      `json:"activeConnections"`
	TenantIDs         []string `json:"tenantIds"`
	Closed            bool     `json:"closed"`
}

// resolveTLS determines whether TLS should be used for a tenant connection.
// Per-tenant TLS configuration (RabbitMQConfig.TLS) takes precedence over the
// global WithTLS() setting. When the per-tenant value is nil (not configured),
// the global useTLS flag is used as a fallback.
func (p *Manager) resolveTLS(cfg *core.RabbitMQConfig) bool {
	if cfg.TLS != nil {
		return *cfg.TLS
	}

	return p.useTLS
}

// dialRabbitMQ connects to RabbitMQ, using TLS when enabled.
// When a custom CA file is specified, it is loaded into the TLS config's RootCAs
// to allow verification against private certificate authorities.
func (p *Manager) dialRabbitMQ(uri string, useTLS bool, tlsCAFile string) (*amqp.Connection, error) {
	if !useTLS || tlsCAFile == "" {
		return amqp.Dial(uri)
	}

	// Load custom CA certificate for TLS verification.
	caCert, err := os.ReadFile(tlsCAFile) // #nosec G304 -- path from tenant config
	if err != nil {
		return nil, fmt.Errorf("failed to read TLS CA file %q: %w", tlsCAFile, err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate from %q", tlsCAFile)
	}

	tlsCfg := &tls.Config{
		RootCAs:    certPool,
		MinVersion: tls.VersionTLS12,
	}

	return amqp.DialTLS(uri, tlsCfg)
}

// buildRabbitMQURI builds RabbitMQ connection URI from config.
// Credentials and vhost are percent-encoded to handle special characters (e.g., @, :, /).
// Uses QueryEscape with '+' replaced by '%20' because QueryEscape encodes spaces as '+'
// which is only valid in query strings, not in userinfo or path segments of a URI.
// When useTLS is true, the amqps:// scheme is used instead of amqp://.
func buildRabbitMQURI(cfg *core.RabbitMQConfig, useTLS bool) string {
	escapedUsername := strings.ReplaceAll(url.QueryEscape(cfg.Username), "+", "%20")
	escapedPassword := strings.ReplaceAll(url.QueryEscape(cfg.Password), "+", "%20")
	escapedVHost := strings.ReplaceAll(url.QueryEscape(cfg.VHost), "+", "%20")

	scheme := "amqp"
	if useTLS {
		scheme = "amqps"
	}

	return fmt.Sprintf("%s://%s:%s@%s:%d/%s",
		scheme, escapedUsername, escapedPassword,
		cfg.Host, cfg.Port, escapedVHost)
}

// IsMultiTenant returns true if the manager is configured with a Tenant Manager client.
func (p *Manager) IsMultiTenant() bool {
	return p.client != nil
}
