package consumer

import (
	"context"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

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
