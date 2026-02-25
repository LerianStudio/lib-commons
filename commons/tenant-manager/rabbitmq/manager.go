package rabbitmq

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
	libOpentelemetry "github.com/LerianStudio/lib-commons/v3/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/core"
	amqp "github.com/rabbitmq/amqp091-go"
)

// defaultIdleTimeout is the default duration before a tenant connection becomes
// eligible for eviction when the pool exceeds the soft limit.
const defaultIdleTimeout = 5 * time.Minute

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
	logger  log.Logger

	mu             sync.RWMutex
	connections    map[string]*amqp.Connection
	closed         bool
	maxConnections int                  // soft limit for pool size (0 = unlimited)
	idleTimeout    time.Duration        // how long before a connection is eligible for eviction
	lastAccessed   map[string]time.Time // LRU tracking per tenant
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

// NewManager creates a new RabbitMQ connection manager.
// Parameters:
//   - c: The Tenant Manager client for fetching tenant configurations
//   - service: The service name (e.g., "ledger")
//   - opts: Optional configuration options
func NewManager(c *client.Client, service string, opts ...Option) *Manager {
	p := &Manager{
		client:       c,
		service:      service,
		connections:  make(map[string]*amqp.Connection),
		lastAccessed: make(map[string]time.Time),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// GetConnection returns a RabbitMQ connection for the tenant.
// Creates a new connection if one doesn't exist or the existing one is closed.
func (p *Manager) GetConnection(ctx context.Context, tenantID string) (*amqp.Connection, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	p.mu.RLock()

	if p.closed {
		p.mu.RUnlock()
		return nil, core.ErrManagerClosed
	}

	if conn, ok := p.connections[tenantID]; ok && !conn.IsClosed() {
		p.mu.RUnlock()

		// Update LRU tracking on cache hit
		p.mu.Lock()
		// Re-check connection still exists (may have been evicted between locks)
		if _, still := p.connections[tenantID]; still {
			p.lastAccessed[tenantID] = time.Now()
		}

		p.mu.Unlock()

		return conn, nil
	}

	p.mu.RUnlock()

	return p.createConnection(ctx, tenantID)
}

// createConnection fetches config from Tenant Manager and creates a RabbitMQ connection.
func (p *Manager) createConnection(ctx context.Context, tenantID string) (*amqp.Connection, error) {
	if p.client == nil {
		return nil, fmt.Errorf("tenant manager client is required for multi-tenant connections")
	}

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "rabbitmq.create_connection")
	defer span.End()

	if p.logger != nil {
		logger = p.logger
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring lock
	if conn, ok := p.connections[tenantID]; ok && !conn.IsClosed() {
		return conn, nil
	}

	if p.closed {
		return nil, core.ErrManagerClosed
	}

	// Fetch tenant config from Tenant Manager
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		logger.Errorf("failed to get tenant config: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to get tenant config", err)

		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	// Get RabbitMQ config
	rabbitConfig := config.GetRabbitMQConfig()
	if rabbitConfig == nil {
		logger.Errorf("RabbitMQ not configured for tenant: %s", tenantID)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "RabbitMQ not configured", nil)

		return nil, core.ErrServiceNotConfigured
	}

	// Build connection URI with tenant's vhost
	uri := buildRabbitMQURI(rabbitConfig)

	logger.Infof("connecting to RabbitMQ vhost: tenant=%s, vhost=%s", tenantID, rabbitConfig.VHost)

	// Create connection
	conn, err := amqp.Dial(uri)
	if err != nil {
		logger.Errorf("failed to connect to RabbitMQ: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to connect to RabbitMQ", err)

		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	// Evict least recently used connection if pool is full
	p.evictLRU(logger)

	// Cache connection
	p.connections[tenantID] = conn
	p.lastAccessed[tenantID] = time.Now()

	logger.Infof("RabbitMQ connection created: tenant=%s, vhost=%s", tenantID, rabbitConfig.VHost)

	return conn, nil
}

// evictLRU removes the least recently used idle connection when the pool reaches the
// soft limit. Only connections that have been idle longer than the idle timeout are
// eligible for eviction. If all connections are active (used within the idle timeout),
// the pool is allowed to grow beyond the soft limit.
// Caller MUST hold p.mu write lock.
func (p *Manager) evictLRU(logger log.Logger) {
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
		if conn != nil && !conn.IsClosed() {
			_ = conn.Close()
		}

		delete(p.connections, oldestID)
		delete(p.lastAccessed, oldestID)

		if logger != nil {
			logger.Infof("LRU evicted idle rabbitmq connection for tenant %s (idle for %s)", oldestID, now.Sub(oldestTime))
		}
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
func (p *Manager) Close(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	var errs []error

	for tenantID, conn := range p.connections {
		if conn != nil && !conn.IsClosed() {
			if err := conn.Close(); err != nil {
				errs = append(errs, err)
			}
		}

		delete(p.connections, tenantID)
		delete(p.lastAccessed, tenantID)
	}

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
	delete(p.lastAccessed, tenantID)

	return err
}

// ApplyConnectionSettings is a no-op for RabbitMQ connections.
// RabbitMQ does not support dynamic connection pool settings like databases do.
// This method exists to satisfy a common manager interface.
func (p *Manager) ApplyConnectionSettings(_ string, _ *core.TenantConfig) {
	// no-op: RabbitMQ connections do not have adjustable pool settings.
}

// Stats returns connection statistics.
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

// buildRabbitMQURI builds RabbitMQ connection URI from config.
// Credentials and vhost are percent-encoded to handle special characters (e.g., @, :, /).
// Uses QueryEscape with '+' replaced by '%20' because QueryEscape encodes spaces as '+'
// which is only valid in query strings, not in userinfo or path segments of a URI.
func buildRabbitMQURI(cfg *core.RabbitMQConfig) string {
	escapedUsername := strings.ReplaceAll(url.QueryEscape(cfg.Username), "+", "%20")
	escapedPassword := strings.ReplaceAll(url.QueryEscape(cfg.Password), "+", "%20")
	escapedVHost := strings.ReplaceAll(url.QueryEscape(cfg.VHost), "+", "%20")

	return fmt.Sprintf("amqp://%s:%s@%s:%d/%s",
		escapedUsername, escapedPassword,
		cfg.Host, cfg.Port, escapedVHost)
}

// IsMultiTenant returns true if the manager is configured with a Tenant Manager client.
func (p *Manager) IsMultiTenant() bool {
	return p.client != nil
}
