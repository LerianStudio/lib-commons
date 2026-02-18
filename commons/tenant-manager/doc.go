// Package tenantmanager provides multi-tenant support for Lerian Studio services.
//
// This package offers utilities for managing tenant context, validation,
// and error handling in multi-tenant applications. It provides:
//   - Tenant context key for request-scoped tenant identification
//   - Standard tenant-related errors for consistent error handling
//   - Tenant isolation utilities to prevent cross-tenant data access
//   - Connection management for PostgreSQL, MongoDB, and RabbitMQ
//   - Multi-tenant message consumer with lazy (on-demand) connection mode
//
// # Multi-Tenant Consumer (Lazy Mode)
//
// The [MultiTenantConsumer] manages RabbitMQ message consumption across multiple
// tenant vhosts. It operates in lazy mode by default:
//
//   - Run() discovers tenants but does NOT start consumers (non-blocking, <1s startup)
//   - Consumers are spawned on-demand via [MultiTenantConsumer.EnsureConsumerStarted]
//   - Background sync loop periodically refreshes the known tenant list
//   - Per-tenant connection failure resilience with exponential backoff (5s, 10s, 20s, 40s)
//   - Tenants are marked as degraded after 3 consecutive connection failures
//
// Basic usage:
//
//	consumer := tenantmanager.NewMultiTenantConsumer(rabbitmqMgr, redisClient, config, logger)
//	consumer.Register("my-queue", myHandler)
//	consumer.Run(ctx)
//	// Later, when a message arrives for tenant-123:
//	consumer.EnsureConsumerStarted(ctx, "tenant-123")
//
// # Connection Failure Resilience
//
// The consumer implements exponential backoff per tenant:
//   - Initial delay: 5 seconds
//   - Backoff factor: 2x (5s -> 10s -> 20s -> 40s)
//   - Maximum delay: 40 seconds
//   - Degraded state: marked after 3 consecutive failures
//   - Retry state resets on successful connection
//
// # Observability
//
// The consumer provides:
//   - OpenTelemetry spans for all operations (layer.domain.operation naming)
//   - Structured log events with tenant_id context
//   - Enhanced [MultiTenantConsumer.Stats] API with ConnectionMode, KnownTenants,
//     PendingTenants, and DegradedTenants
//   - Prometheus-compatible metric name constants (MetricTenantConnectionsTotal, etc.)
package tenantmanager

const (
	// PackageName is the name of this package, used for logging and identification.
	PackageName = "tenants"
)

// Note: Tenant context keys are defined in context.go as typed ContextKey constants.
// Use TenantIDContextKey for storing/retrieving tenant ID from context.
