package tenantmanager

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v3/commons"
	"github.com/LerianStudio/lib-commons/v3/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v3/commons/opentelemetry"
	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQManager manages RabbitMQ connections per tenant.
// Each tenant has a dedicated vhost, user, and credentials stored in Tenant Manager.
// When maxConnections is set (> 0), the manager uses LRU eviction with an idle
// timeout as a soft limit. Connections idle longer than the timeout are eligible
// for eviction when the pool exceeds maxConnections. If all connections are active
// (used within the idle timeout), the pool grows beyond the soft limit and
// naturally shrinks back as tenants become idle.
type RabbitMQManager struct {
	client  *Client
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

// RabbitMQOption configures a RabbitMQManager.
type RabbitMQOption func(*RabbitMQManager)

// WithRabbitMQModule sets the module name for the RabbitMQ manager.
func WithRabbitMQModule(module string) RabbitMQOption {
	return func(p *RabbitMQManager) {
		p.module = module
	}
}

// WithRabbitMQLogger sets the logger for the RabbitMQ manager.
func WithRabbitMQLogger(logger log.Logger) RabbitMQOption {
	return func(p *RabbitMQManager) {
		p.logger = logger
	}
}

// WithRabbitMQMaxTenantPools sets the soft limit for the number of tenant connections in the pool.
// When the pool reaches this limit and a new tenant needs a connection, only connections
// that have been idle longer than the idle timeout are eligible for eviction. If all
// connections are active (used within the idle timeout), the pool grows beyond this limit.
// A value of 0 (default) means unlimited.
func WithRabbitMQMaxTenantPools(maxSize int) RabbitMQOption {
	return func(p *RabbitMQManager) {
		p.maxConnections = maxSize
	}
}

// WithRabbitMQIdleTimeout sets the duration after which an unused tenant connection becomes
// eligible for eviction. Only connections idle longer than this duration will be evicted
// when the pool exceeds the soft limit (maxConnections). If all connections are active
// (used within the idle timeout), the pool is allowed to grow beyond the soft limit.
// Default: 5 minutes.
func WithRabbitMQIdleTimeout(d time.Duration) RabbitMQOption {
	return func(p *RabbitMQManager) {
		p.idleTimeout = d
	}
}

// Deprecated: Use WithRabbitMQMaxTenantPools instead.
func WithRabbitMQMaxConnections(maxSize int) RabbitMQOption { return WithRabbitMQMaxTenantPools(maxSize) }

// NewRabbitMQManager creates a new RabbitMQ connection manager.
// Parameters:
//   - client: The Tenant Manager client for fetching tenant configurations
//   - service: The service name (e.g., "ledger")
//   - opts: Optional configuration options
func NewRabbitMQManager(client *Client, service string, opts ...RabbitMQOption) *RabbitMQManager {
	p := &RabbitMQManager{
		client:       client,
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
func (p *RabbitMQManager) GetConnection(ctx context.Context, tenantID string) (*amqp.Connection, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	p.mu.RLock()

	if p.closed {
		p.mu.RUnlock()
		return nil, ErrManagerClosed
	}

	if conn, ok := p.connections[tenantID]; ok && !conn.IsClosed() {
		p.mu.RUnlock()

		// Update LRU tracking on cache hit
		p.mu.Lock()
		p.lastAccessed[tenantID] = time.Now()
		p.mu.Unlock()

		return conn, nil
	}

	p.mu.RUnlock()

	return p.createConnection(ctx, tenantID)
}

// createConnection fetches config from Tenant Manager and creates a RabbitMQ connection.
func (p *RabbitMQManager) createConnection(ctx context.Context, tenantID string) (*amqp.Connection, error) {
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
		return nil, ErrManagerClosed
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

		return nil, ErrServiceNotConfigured
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
func (p *RabbitMQManager) evictLRU(logger log.Logger) {
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
func (p *RabbitMQManager) GetChannel(ctx context.Context, tenantID string) (*amqp.Channel, error) {
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
func (p *RabbitMQManager) Close() error {
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
func (p *RabbitMQManager) CloseConnection(tenantID string) error {
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

// Stats returns connection statistics.
func (p *RabbitMQManager) Stats() RabbitMQStats {
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

	return RabbitMQStats{
		TotalConnections:  len(p.connections),
		MaxConnections:    p.maxConnections,
		ActiveConnections: activeConnections,
		TenantIDs:         tenantIDs,
		Closed:            p.closed,
	}
}

// RabbitMQStats contains statistics for the RabbitMQ manager.
type RabbitMQStats struct {
	TotalConnections  int      `json:"totalConnections"`
	MaxConnections    int      `json:"maxConnections"`
	ActiveConnections int      `json:"activeConnections"`
	TenantIDs         []string `json:"tenantIds"`
	Closed            bool     `json:"closed"`
}

// buildRabbitMQURI builds RabbitMQ connection URI from config.
// Credentials are URL-encoded to handle special characters (e.g., @, :, /).
func buildRabbitMQURI(cfg *RabbitMQConfig) string {
	return fmt.Sprintf("amqp://%s:%s@%s:%d/%s",
		url.QueryEscape(cfg.Username), url.QueryEscape(cfg.Password),
		cfg.Host, cfg.Port, cfg.VHost)
}

// IsMultiTenant returns true if the manager is configured with a Tenant Manager client.
func (p *RabbitMQManager) IsMultiTenant() bool {
	return p.client != nil
}
