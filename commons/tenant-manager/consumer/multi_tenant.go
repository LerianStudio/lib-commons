// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

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
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/rabbitmq"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/tenantcache"
)

// HandlerFunc is a function that processes messages from a queue.
// The context contains the tenant ID via core.SetTenantIDInContext.
type HandlerFunc func(ctx context.Context, delivery amqp.Delivery) error

// MultiTenantConfig holds configuration for the MultiTenantConsumer.
type MultiTenantConfig struct {
	// SyncInterval is retained for backward config compatibility only.
	//
	// Deprecated: No longer used. Retained for backward config compatibility only.
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

	// DiscoveryTimeout is retained for backward config compatibility only.
	//
	// Deprecated: No longer used. Retained for backward config compatibility only.
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

	// TenantCacheTTL is the time-to-live for tenant entries in the local cache.
	// When an entry expires, the next request triggers a fresh lazy-load from tenant-manager.
	// Default: 12 hours.
	TenantCacheTTL time.Duration

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
		TenantCacheTTL:   DefaultTenantCacheTTL,
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

// WithRabbitMQ sets the RabbitMQ Manager on the consumer.
// When set, the consumer can register queue handlers and spawn per-tenant
// consumer goroutines. When not set (HTTP-only mode), the consumer still
// manages the tenant cache and events but Register() returns an error and
// ensureConsumerStarted skips goroutine spawning.
func WithRabbitMQ(r *tmrabbitmq.Manager) Option {
	return func(c *MultiTenantConsumer) { c.rabbitmq = r }
}

// WithEventDispatcher injects an externally-created EventDispatcher.
// When set, the consumer uses this dispatcher instead of building one internally.
// The dispatcher handles cache operations, infrastructure teardown, and notifies
// the consumer via callbacks (onTenantAdded/onTenantRemoved).
func WithEventDispatcher(d *event.EventDispatcher) Option {
	return func(c *MultiTenantConsumer) { c.dispatcher = d }
}

// MultiTenantConsumer manages message consumption across multiple tenant vhosts.
// It receives tenant lifecycle events via an externally-managed EventDispatcher
// and starts consumer goroutines lazily on first request or event.
type MultiTenantConsumer struct {
	rabbitmq     *tmrabbitmq.Manager
	pmClient     *client.Client // Tenant Manager HTTP API client (primary source of truth)
	handlers     map[string]HandlerFunc
	tenants      map[string]context.CancelFunc // Active tenant goroutines
	knownTenants map[string]bool               // Discovered tenants (populated by events and lazy-load)
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

	// loader fetches and caches tenant configurations from the tenant-manager API.
	// It manages its own per-tenant mutexes to prevent concurrent API calls.
	loader *tenantcache.TenantLoader

	// retryState holds per-tenant retry counters for connection failure resilience.
	// Key: tenantID, Value: *retryStateEntry
	retryState sync.Map

	// parentCtx is the context passed to Run(), stored for use by ensureConsumerStarted.
	parentCtx context.Context

	// cache is the process-local tenant config cache with TTL-based eviction.
	// Used by the TenantLoader to avoid redundant API calls for recently-loaded tenants.
	cache *tenantcache.TenantCache

	// dispatcher handles lifecycle events (cache, infra teardown, callbacks).
	// Injected via WithEventDispatcher or built internally by the constructor.
	// The consumer hooks into it via callbacks to manage knownTenants and
	// tenant goroutines.
	dispatcher *event.EventDispatcher
}

// NewMultiTenantConsumerWithError creates a new MultiTenantConsumer.
// Parameters:
//   - config: Consumer configuration (MultiTenantURL and Service are required)
//   - logger: Logger for operational logging
//   - opts: Optional configuration options (e.g., WithRabbitMQ, WithPostgresManager,
//     WithMongoManager, WithEventDispatcher)
//
// RabbitMQ is optional. When provided via WithRabbitMQ(), the consumer can register
// queue handlers and spawn per-tenant consumer goroutines. When not set (HTTP-only
// mode), Register() returns an error and ensureConsumerStarted skips goroutine spawning.
//
// The tenant-manager HTTP API is the single source of truth for tenant discovery.
// MultiTenantURL and Service must be set in config.
// Returns an error if MultiTenantURL/Service are not configured.
func NewMultiTenantConsumerWithError(
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) (*MultiTenantConsumer, error) {
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
		handlers:     make(map[string]HandlerFunc),
		tenants:      make(map[string]context.CancelFunc),
		knownTenants: make(map[string]bool),
		config:       config,
		logger:       logcompat.New(logger),
		cache:        tenantcache.NewTenantCache(),
	}

	// Apply optional configurations
	for _, opt := range opts {
		opt(consumer)
	}

	// Create Tenant Manager HTTP API client (required -- single source of truth)
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

	// Create shared TenantLoader for fetch-and-cache operations.
	cacheTTL := config.TenantCacheTTL
	if cacheTTL <= 0 {
		cacheTTL = DefaultTenantCacheTTL
	}

	consumer.loader = tenantcache.NewTenantLoader(pmClient, consumer.cache, config.Service, cacheTTL, consumer.logger.Base())

	// Only build the dispatcher internally if one was not injected via WithEventDispatcher.
	if consumer.dispatcher == nil {
		consumer.dispatcher = consumer.buildEventDispatcher(cacheTTL)
	} else {
		// Wire the injected dispatcher's callbacks to this consumer's state.
		consumer.wireDispatcherCallbacks()
	}

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
// Returns an error if handler is nil or if RabbitMQ manager is not set.
// RabbitMQ is required for queue consumption; use WithRabbitMQ() option when creating
// the consumer.
func (c *MultiTenantConsumer) Register(queueName string, handler HandlerFunc) error {
	if c.rabbitmq == nil {
		return errors.New("consumer.Register: RabbitMQ manager is required for queue consumption; use WithRabbitMQ() option")
	}

	if handler == nil {
		return fmt.Errorf("consumer.Register: queue %q: %w", queueName, core.ErrNilHandlerFunc)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.handlers[queueName] = handler
	c.logger.Infof("registered handler for queue: %s", queueName)

	return nil
}

// Run starts the multi-tenant consumer in event-driven mode.
// The event listener is managed externally; Run() stores the parent context
// and makes the consumer ready to receive events and lazy-load tenants.
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

	logger.InfoCtx(ctx, "multi-tenant consumer ready (event-driven, lazy-load on first request)")

	return nil
}

// Close stops all consumer goroutines and marks the consumer as closed.
// It also closes the pmClient to prevent goroutine leaks from its
// InMemoryCache cleanup loop.
// The event listener is managed externally and is NOT stopped here.
func (c *MultiTenantConsumer) Close() error {
	c.mu.Lock()
	c.closed = true

	// Cancel all tenant contexts
	for tenantID, cancel := range c.tenants {
		c.logger.Infof("stopping consumer for tenant: %s", tenantID)
		cancel()
	}

	// Clear the maps
	c.tenants = make(map[string]context.CancelFunc)
	c.knownTenants = make(map[string]bool)
	c.mu.Unlock()

	// Close pmClient to release its InMemoryCache cleanup goroutine.
	if c.pmClient != nil {
		if err := c.pmClient.Close(); err != nil {
			c.logger.Warnf("failed to close tenant manager client: %v", err)
		}
	}

	c.logger.Info("multi-tenant consumer closed")

	return nil
}

// buildEventDispatcher creates the EventDispatcher with callbacks that manage
// the consumer's internal maps (knownTenants, tenants) and goroutines.
func (c *MultiTenantConsumer) buildEventDispatcher(cacheTTL time.Duration) *event.EventDispatcher {
	opts := []event.DispatcherOption{
		event.WithDispatcherLogger(c.logger.Base()),
		event.WithCacheTTL(cacheTTL),
		event.WithTenantOwnershipChecker(func(tenantID string) bool {
			c.mu.RLock()
			defer c.mu.RUnlock()

			return c.knownTenants[tenantID]
		}),
		event.WithOnTenantAdded(func(ctx context.Context, tenantID string) {
			c.mu.Lock()
			c.knownTenants[tenantID] = true
			c.mu.Unlock()

			c.ensureConsumerStarted(ctx, tenantID)
		}),
		event.WithOnTenantRemoved(func(ctx context.Context, tenantID string) {
			c.mu.Lock()
			if cancel, ok := c.tenants[tenantID]; ok {
				cancel()
				delete(c.tenants, tenantID)
			}

			delete(c.knownTenants, tenantID)
			c.mu.Unlock()
		}),
	}

	if c.postgres != nil {
		opts = append(opts, event.WithPostgres(c.postgres))
	}

	if c.mongo != nil {
		opts = append(opts, event.WithMongo(c.mongo))
	}

	if c.rabbitmq != nil {
		opts = append(opts, event.WithRabbitMQ(c.rabbitmq))
	}

	return event.NewEventDispatcher(c.cache, c.loader, c.config.Service, opts...)
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
