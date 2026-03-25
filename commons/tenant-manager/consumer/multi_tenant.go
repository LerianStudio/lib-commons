// Package consumer provides multi-tenant message queue consumption management.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/rabbitmq"
)

// HandlerFunc is a function that processes messages from a queue.
// The context contains the tenant ID via core.SetTenantIDInContext.
type HandlerFunc func(ctx context.Context, delivery amqp.Delivery) error

// MultiTenantConfig holds configuration for the MultiTenantConsumer.
type MultiTenantConfig struct {
	// SyncInterval is the interval between tenant list synchronizations.
	// Default: 30 seconds
	SyncInterval time.Duration

	// WorkersPerQueue is reserved for future use. It is currently not implemented
	// and has no effect on consumer behavior. Each queue runs a single consumer goroutine.
	// Setting this field is a no-op; it is retained only for backward compatibility.
	//
	// Deprecated: This field is not yet implemented. Setting it has no effect.
	WorkersPerQueue int

	// PrefetchCount is the QoS prefetch count per channel.
	// Default: 10
	PrefetchCount int

	// MultiTenantURL is the HTTP endpoint of the tenant-manager API (required).
	// The API is the single source of truth for tenant discovery.
	// Format: http://tenant-manager:4003
	MultiTenantURL string

	// ServiceAPIKey is the API key sent as X-API-Key header on HTTP requests to the
	// Tenant Manager. Required. Typically sourced from the
	// MULTI_TENANT_SERVICE_API_KEY environment variable.
	ServiceAPIKey string

	// Service is the service name to filter tenants by.
	// This is passed to tenant-manager when fetching tenant list.
	Service string

	// Environment is the deployment environment (e.g., "staging", "production").
	// Retained for backward compatibility but no longer used for Redis key
	// construction. Tenant discovery uses the tenant-manager API exclusively.
	Environment string

	// DiscoveryTimeout is the maximum time allowed for the initial tenant discovery
	// (fetching tenant IDs at startup). If zero, 500ms is used. Increase this for
	// high-latency or loaded environments where the tenant-manager API
	// may respond slowly; discovery is best-effort and the sync loop will retry.
	// Default: 500ms
	DiscoveryTimeout time.Duration

	// AllowInsecureHTTP permits the use of http:// (plaintext) URLs for the
	// MultiTenantURL. By default, only https:// is accepted by the underlying
	// client. Set this to true for in-cluster Kubernetes service URLs that use
	// plain HTTP (e.g., http://tenant-manager.namespace.svc.cluster.local:4003).
	// Default: false
	AllowInsecureHTTP bool

	// CacheTTL is the TTL for the internal tenant config cache used by the consumer's
	// HTTP client. When zero, the client's default (1 hour) is used.
	// Recommended: match the MULTI_TENANT_CACHE_TTL_SEC env var from the consuming service.
	CacheTTL time.Duration

	// Deprecated: EagerStart is ignored. Consumers are always started eagerly.
	// This field is retained only for backward compatibility with existing configs;
	// setting it has no effect. It will be removed in a future major version.
	EagerStart bool
}

// DefaultMultiTenantConfig returns a MultiTenantConfig with sensible defaults.
func DefaultMultiTenantConfig() MultiTenantConfig {
	return MultiTenantConfig{
		SyncInterval:     30 * time.Second,
		PrefetchCount:    10,
		DiscoveryTimeout: 500 * time.Millisecond,
	}
}

// Option configures a MultiTenantConsumer.
type Option func(*MultiTenantConsumer)

// WithPostgresManager sets the postgres Manager on the consumer.
// When set, database connections for removed tenants are automatically closed
// during tenant synchronization.
func WithPostgresManager(p *tmpostgres.Manager) Option {
	return func(c *MultiTenantConsumer) { c.postgres = p }
}

// WithMongoManager sets the mongo Manager on the consumer.
// When set, MongoDB connections for removed tenants are automatically closed
// during tenant synchronization.
func WithMongoManager(m *tmmongo.Manager) Option {
	return func(c *MultiTenantConsumer) { c.mongo = m }
}

// MultiTenantConsumer manages message consumption across multiple tenant vhosts.
// It dynamically discovers tenants via the tenant-manager API and spawns consumer goroutines.
// Run() discovers tenants and eagerly starts consumers for all known tenants.
// New tenants discovered during background sync are also started immediately.
type MultiTenantConsumer struct {
	rabbitmq     *tmrabbitmq.Manager
	pmClient     *client.Client // Tenant Manager HTTP API client (primary source of truth)
	handlers     map[string]HandlerFunc
	tenants      map[string]context.CancelFunc // Active tenant goroutines
	knownTenants map[string]bool               // Discovered tenants (populated by discovery and sync)
	config       MultiTenantConfig
	mu           sync.RWMutex
	logger       *logcompat.Logger
	closed       bool

	// postgres manages PostgreSQL connections per tenant.
	// When set, connections are closed automatically when a tenant is removed.
	postgres *tmpostgres.Manager

	// mongo manages MongoDB connections per tenant.
	// When set, connections are closed automatically when a tenant is removed.
	mongo *tmmongo.Manager

	// consumerLocks provides per-tenant mutexes for double-check locking in ensureConsumerStarted.
	// Key: tenantID, Value: *sync.Mutex
	consumerLocks sync.Map

	// retryState holds per-tenant retry counters for connection failure resilience.
	// Key: tenantID, Value: *retryStateEntry
	retryState sync.Map

	// parentCtx is the context passed to Run(), stored for use by ensureConsumerStarted.
	parentCtx context.Context

	// syncLoopCancel cancels the context used by the sync loop goroutine.
	// Stored in Run() and called in Close() to ensure the sync loop stops
	// even when the original context (e.g., context.Background()) is never cancelled.
	syncLoopCancel context.CancelFunc
}

// NewMultiTenantConsumerWithError creates a new MultiTenantConsumer.
// Parameters:
//   - rabbitmq: RabbitMQ connection manager for tenant vhosts (must not be nil)
//   - config: Consumer configuration (MultiTenantURL and Service are required)
//   - logger: Logger for operational logging
//   - opts: Optional configuration options (e.g., WithPostgresManager, WithMongoManager)
//
// The tenant-manager HTTP API is the single source of truth for tenant discovery.
// MultiTenantURL and Service must be set in config.
// Returns an error if rabbitmq is nil or if MultiTenantURL/Service are not configured.
func NewMultiTenantConsumerWithError(
	rabbitmq *tmrabbitmq.Manager,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) (*MultiTenantConsumer, error) {
	if rabbitmq == nil {
		return nil, errors.New("consumer.NewMultiTenantConsumerWithError: rabbitmq must not be nil")
	}

	if config.MultiTenantURL == "" {
		return nil, errors.New("consumer.NewMultiTenantConsumerWithError: MultiTenantURL must not be empty (tenant-manager API is required)")
	}

	if config.Service == "" {
		return nil, errors.New("consumer.NewMultiTenantConsumerWithError: Service must not be empty")
	}

	if config.CacheTTL < 0 {
		return nil, fmt.Errorf("consumer.NewMultiTenantConsumerWithError: CacheTTL must be non-negative, got %v", config.CacheTTL)
	}

	// Guard against nil logger to prevent panics downstream
	if logger == nil {
		logger = libLog.NewNop()
	}

	// Apply defaults
	if config.SyncInterval <= 0 {
		config.SyncInterval = 30 * time.Second
	}

	if config.PrefetchCount == 0 {
		config.PrefetchCount = 10
	}

	consumer := &MultiTenantConsumer{
		rabbitmq:     rabbitmq,
		handlers:     make(map[string]HandlerFunc),
		tenants:      make(map[string]context.CancelFunc),
		knownTenants: make(map[string]bool),
		config:       config,
		logger:       logcompat.New(logger),
	}

	// Apply optional configurations
	for _, opt := range opts {
		opt(consumer)
	}

	// Create Tenant Manager HTTP API client (required — single source of truth)
	clientOpts := []client.ClientOption{
		client.WithServiceAPIKey(config.ServiceAPIKey),
	}

	if config.AllowInsecureHTTP {
		clientOpts = append(clientOpts, client.WithAllowInsecureHTTP())
	}

	if config.CacheTTL > 0 {
		clientOpts = append(clientOpts, client.WithCacheTTL(config.CacheTTL))
	}

	pmClient, err := client.NewClient(config.MultiTenantURL, consumer.logger.Base(), clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("consumer.NewMultiTenantConsumerWithError: invalid MultiTenantURL: %w", err)
	}

	consumer.pmClient = pmClient

	if config.WorkersPerQueue > 0 {
		consumer.logger.Base().Log(context.Background(), libLog.LevelWarn,
			"WorkersPerQueue is deprecated and has no effect; the field is reserved for future use",
			libLog.Int("workers_per_queue", config.WorkersPerQueue))
	}

	return consumer, nil
}

// Register adds a queue handler for all tenant vhosts.
// The handler will be invoked for messages from the specified queue in each tenant's vhost.
//
// Handlers should be registered before calling Run(). Handlers registered after Run()
// has been called will only take effect for tenants whose consumers are spawned after
// the registration; already-running tenant consumers will NOT pick up the new handler.
//
// Returns an error if handler is nil.
func (c *MultiTenantConsumer) Register(queueName string, handler HandlerFunc) error {
	if handler == nil {
		return fmt.Errorf("consumer.Register: queue %q: %w", queueName, core.ErrNilHandlerFunc)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.handlers[queueName] = handler
	c.logger.Infof("registered handler for queue: %s", queueName)

	return nil
}

// Run starts the multi-tenant consumer.
// It discovers tenants (non-blocking, soft failure), eagerly starts consumers
// for all discovered tenants, and starts background polling for new tenants.
// Returns nil even on discovery failure (soft failure).
func (c *MultiTenantConsumer) Run(ctx context.Context) error {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	// Fall back to constructor logger when context has no logger attached
	// (e.g., context.Background()). This prevents silent log loss.
	if c.logger != nil {
		logger = c.logger
	}

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.run")
	defer span.End()

	// Store parent context for use by ensureConsumerStarted.
	// Protected by c.mu because ensureConsumerStarted reads it concurrently.
	c.mu.Lock()
	c.parentCtx = ctx
	c.mu.Unlock()

	// Discover tenants without blocking (soft failure - does not start consumers)
	c.discoverTenants(ctx)

	// Capture count under lock to avoid concurrent read race
	c.mu.RLock()
	knownCount := len(c.knownTenants)
	c.mu.RUnlock()

	logger.InfofCtx(ctx, "starting multi-tenant consumer, connection_mode=eager, known_tenants=%d",
		knownCount)

	// Eager start: start consumers for all discovered tenants immediately
	if knownCount > 0 {
		c.eagerStartKnownTenants(ctx)
	}

	// Background polling - ASYNC
	// Create a derived context so Close() can stop the sync loop even when
	// the caller passes a never-cancelled context (e.g., context.Background()).
	syncCtx, syncCancel := context.WithCancel(ctx) //#nosec G118 -- cancel is stored in c.syncLoopCancel and called by Close()
	c.syncLoopCancel = syncCancel

	go c.syncActiveTenants(syncCtx)

	return nil
}

// Close stops all consumer goroutines and marks the consumer as closed.
// It also closes the pmClient to prevent goroutine leaks from its
// InMemoryCache cleanup loop.
func (c *MultiTenantConsumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true

	// Cancel the sync loop context first, so the background polling goroutine
	// stops before we tear down individual tenant consumers.
	if c.syncLoopCancel != nil {
		c.syncLoopCancel()
	}

	// Cancel all tenant contexts
	for tenantID, cancel := range c.tenants {
		c.logger.Infof("stopping consumer for tenant: %s", tenantID)
		cancel()
	}

	// Clear the maps

	c.tenants = make(map[string]context.CancelFunc)
	c.knownTenants = make(map[string]bool)

	// Close pmClient to release its InMemoryCache cleanup goroutine.
	if c.pmClient != nil {
		if err := c.pmClient.Close(); err != nil {
			c.logger.Warnf("failed to close tenant manager client: %v", err)
		}
	}

	c.logger.Info("multi-tenant consumer closed")

	return nil
}

// Stats holds statistics for the consumer.
type Stats struct {
	ActiveTenants    int      `json:"activeTenants"`
	TenantIDs        []string `json:"tenantIds"`
	RegisteredQueues []string `json:"registeredQueues"`
	Closed           bool     `json:"closed"`
	KnownTenants     int      `json:"knownTenants"`
	KnownTenantIDs   []string `json:"knownTenantIds"`
	PendingTenants   int      `json:"pendingTenants"`
	PendingTenantIDs []string `json:"pendingTenantIds"`
	DegradedTenants  []string `json:"degradedTenants"`
}

// Prometheus-compatible metric name constants for multi-tenant consumer observability.
// These constants provide a standardized naming scheme for metrics instrumentation.
const (
	// MetricTenantConnectionsTotal tracks the total number of tenant connections established.
	MetricTenantConnectionsTotal = "tenant_connections_total"
	// MetricTenantConnectionErrors tracks connection errors by tenant.
	MetricTenantConnectionErrors = "tenant_connection_errors_total"
	// MetricTenantConsumersActive tracks the number of currently active tenant consumers.
	MetricTenantConsumersActive = "tenant_consumers_active"
	// MetricTenantMessageProcessed tracks the total number of messages processed per tenant.
	MetricTenantMessageProcessed = "tenant_messages_processed_total"
)
