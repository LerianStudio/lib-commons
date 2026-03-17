package consumer

import (
	"context"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// revalidateConnectionSettings fetches current settings from the Tenant Manager
// for each active tenant and applies any changed connection pool settings to
// existing PostgreSQL and MongoDB connections.
//
// For PostgreSQL, SetMaxOpenConns/SetMaxIdleConns are thread-safe and take effect
// immediately for new connections from the pool without recreating the connection.
// For MongoDB, the driver does not support pool resize after creation, so a warning
// is logged and changes take effect on the next connection recreation.
//
// This method is called after syncTenants in each sync iteration. Errors fetching
// config for individual tenants are logged and skipped (will retry next cycle).
// If the Tenant Manager is down, the circuit breaker handles fast-fail.
func (c *MultiTenantConsumer) revalidateConnectionSettings(ctx context.Context) {
	if c.postgres == nil && c.mongo == nil {
		return
	}

	if c.pmClient == nil || c.config.Service == "" {
		return
	}

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.revalidate_connection_settings")
	defer span.End()

	// Snapshot current tenant IDs under lock to avoid holding the lock during HTTP calls.
	c.mu.RLock()

	tenantIDs := make([]string, 0, len(c.tenants))
	for tenantID := range c.tenants {
		tenantIDs = append(tenantIDs, tenantID)
	}

	c.mu.RUnlock()

	if len(tenantIDs) == 0 {
		return
	}

	var revalidated int

	for _, tenantID := range tenantIDs {
		config, err := c.pmClient.GetTenantConfig(ctx, tenantID, c.config.Service)
		if err != nil {
			// If tenant service was suspended/purged, stop consumer and close connections.
			if core.IsTenantSuspendedError(err) {
				c.evictSuspendedTenant(ctx, tenantID, logger)
				continue
			}

			logger.WarnfCtx(ctx, "failed to fetch config for tenant %s during settings revalidation: %v", tenantID, err)

			continue
		}

		if c.postgres != nil {
			c.postgres.ApplyConnectionSettings(tenantID, config)
		}

		if c.mongo != nil {
			c.mongo.ApplyConnectionSettings(tenantID, config)
		}

		revalidated++
	}

	if revalidated > 0 {
		logger.InfofCtx(ctx, "revalidated connection settings for %d/%d active tenants", revalidated, len(tenantIDs))
	}
}

// evictSuspendedTenant stops the consumer and closes all database connections for a
// tenant whose service was suspended or purged by the Tenant Manager. The tenant is
// removed from both tenants and knownTenants maps so it will not be restarted by the
// sync loop. The next request for this tenant will receive the 403 error directly.
func (c *MultiTenantConsumer) evictSuspendedTenant(ctx context.Context, tenantID string, logger *logcompat.Logger) {
	logger.WarnfCtx(ctx, "tenant %s service suspended, stopping consumer and closing connections", tenantID)

	c.mu.Lock()

	if cancel, ok := c.tenants[tenantID]; ok {
		cancel()
		delete(c.tenants, tenantID)
	}

	delete(c.knownTenants, tenantID)
	c.mu.Unlock()

	if c.postgres != nil {
		if err := c.postgres.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close PostgreSQL connection for suspended tenant %s: %v", tenantID, err)
		}
	}

	if c.mongo != nil {
		if err := c.mongo.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close MongoDB connection for suspended tenant %s: %v", tenantID, err)
		}
	}

	if c.rabbitmq != nil {
		if err := c.rabbitmq.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close RabbitMQ connection for suspended tenant %s: %v", tenantID, err)
		}
	}
}
