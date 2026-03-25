// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// handleTenantCreated is a no-op: new tenants are lazy-loaded on first request.
func (c *MultiTenantConsumer) handleTenantCreated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.created received tenant=%s: no-op (lazy-load on first request)", evt.TenantID)
	return nil
}

// handleTenantActivated touches the TTL for a cached tenant, keeping it fresh.
func (c *MultiTenantConsumer) handleTenantActivated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	ttl := c.resolveCacheTTL()

	touched := c.cache.Touch(evt.TenantID, ttl)
	if touched {
		logger.InfofCtx(ctx, "tenant.activated: refreshed TTL for tenant=%s", evt.TenantID)
	} else {
		logger.Debugf("tenant.activated: tenant=%s not in cache, skipping TTL refresh", evt.TenantID)
	}

	return nil
}

// handleTenantSuspended removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleTenantSuspended(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.suspended: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleTenantDeleted removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleTenantDeleted(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.deleted: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleTenantUpdated touches the TTL for a cached tenant.
func (c *MultiTenantConsumer) handleTenantUpdated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	ttl := c.resolveCacheTTL()

	touched := c.cache.Touch(evt.TenantID, ttl)
	if touched {
		logger.InfofCtx(ctx, "tenant.updated: refreshed TTL for tenant=%s", evt.TenantID)
	} else {
		logger.Debugf("tenant.updated: tenant=%s not in cache, skipping TTL refresh", evt.TenantID)
	}

	return nil
}

// handleServiceAssociated adds the tenant to cache with a minimal config built
// from the event payload, marks it as known, and starts a consumer goroutine.
// Actual DB connections are created lazily when the first request arrives.
func (c *MultiTenantConsumer) handleServiceAssociated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload event.ServiceAssociatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleServiceAssociated: unmarshal payload: %w", err)
	}

	logger.InfofCtx(ctx, "tenant.service.associated: adding tenant=%s service=%s", evt.TenantID, payload.ServiceName)

	// Build TenantConfig from payload with connection settings, databases, and messaging
	config := &core.TenantConfig{
		ID:            evt.TenantID,
		TenantSlug:    evt.TenantSlug,
		Service:       payload.ServiceName,
		IsolationMode: payload.IsolationMode,
	}

	// Populate ConnectionSettings from payload
	if payload.ConnectionSettings != nil {
		config.ConnectionSettings = &core.ConnectionSettings{
			MaxOpenConns:     payload.ConnectionSettings.MaxOpenConns,
			MaxIdleConns:     payload.ConnectionSettings.MaxIdleConns,
			StatementTimeout: payload.ConnectionSettings.StatementTimeout,
		}
	}

	// Populate Databases from secret_paths (modules with their secret paths).
	// secret_paths carry paths to Secrets Manager, not actual credentials.
	// The connection manager resolves credentials lazily via pmClient.GetTenantConfig().
	// We store the module entry so the config is not nil when checked.
	if len(payload.SecretPaths) > 0 {
		config.Databases = make(map[string]core.DatabaseConfig, len(payload.SecretPaths))

		for module, paths := range payload.SecretPaths {
			dbConfig := core.DatabaseConfig{}

			if pgRW, ok := paths["postgresql_rw"]; ok {
				dbConfig.PostgreSQL = &core.PostgreSQLConfig{
					Host: pgRW, // placeholder: secret path, resolved later
				}
			}

			if pgRO, ok := paths["postgresql_ro"]; ok {
				dbConfig.PostgreSQLReplica = &core.PostgreSQLConfig{
					Host: pgRO, // placeholder: secret path, resolved later
				}
			}

			if _, ok := paths["mongodb"]; ok {
				dbConfig.MongoDB = &core.MongoDBConfig{}
			}

			config.Databases[module] = dbConfig
		}
	}

	// Populate Messaging from payload
	if payload.MessagingConfig != nil && payload.MessagingConfig.RabbitMQSecretPath != "" {
		config.Messaging = &core.MessagingConfig{
			RabbitMQ: &core.RabbitMQConfig{
				// Secret path stored in Host field; actual credentials resolved lazily
				Host: payload.MessagingConfig.RabbitMQSecretPath,
			},
		}
	}

	ttl := c.resolveCacheTTL()
	c.cache.Set(evt.TenantID, config, ttl)

	c.mu.Lock()
	c.knownTenants[evt.TenantID] = true
	c.mu.Unlock()

	c.ensureConsumerStarted(ctx, evt.TenantID)

	return nil
}

// handleServiceDisassociated removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleServiceDisassociated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.disassociated: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleServiceSuspended removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleServiceSuspended(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.suspended: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleServicePurged removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleServicePurged(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.purged: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleServiceReactivated re-adds the tenant to cache and starts a consumer.
// A random jitter (0-5s) is applied before rebuilding to prevent thundering herd.
func (c *MultiTenantConsumer) handleServiceReactivated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload event.ServiceReactivatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleServiceReactivated: unmarshal payload: %w", err)
	}

	logger.InfofCtx(ctx, "tenant.service.reactivated: re-adding tenant=%s with jitter", evt.TenantID)

	c.applyJitter(ctx)

	// Build TenantConfig from payload with connection settings and databases
	config := &core.TenantConfig{
		ID:         evt.TenantID,
		TenantSlug: evt.TenantSlug,
		Service:    payload.ServiceName,
	}

	// Populate ConnectionSettings from payload
	if payload.ConnectionSettings != nil {
		config.ConnectionSettings = &core.ConnectionSettings{
			MaxOpenConns:     payload.ConnectionSettings.MaxOpenConns,
			MaxIdleConns:     payload.ConnectionSettings.MaxIdleConns,
			StatementTimeout: payload.ConnectionSettings.StatementTimeout,
		}
	}

	// Populate Databases from secret_paths (same pattern as handleServiceAssociated)
	if len(payload.SecretPaths) > 0 {
		config.Databases = make(map[string]core.DatabaseConfig, len(payload.SecretPaths))

		for module, paths := range payload.SecretPaths {
			dbConfig := core.DatabaseConfig{}

			if pgRW, ok := paths["postgresql_rw"]; ok {
				dbConfig.PostgreSQL = &core.PostgreSQLConfig{
					Host: pgRW,
				}
			}

			if pgRO, ok := paths["postgresql_ro"]; ok {
				dbConfig.PostgreSQLReplica = &core.PostgreSQLConfig{
					Host: pgRO,
				}
			}

			if _, ok := paths["mongodb"]; ok {
				dbConfig.MongoDB = &core.MongoDBConfig{}
			}

			config.Databases[module] = dbConfig
		}
	}

	ttl := c.resolveCacheTTL()
	c.cache.Set(evt.TenantID, config, ttl)

	c.mu.Lock()
	c.knownTenants[evt.TenantID] = true
	c.mu.Unlock()

	c.ensureConsumerStarted(ctx, evt.TenantID)

	return nil
}

// handleCredentialsRotated closes existing pools, applies jitter, and eagerly
// reloads tenant config via /connections to reconnect with new credentials.
// Unlike other events that rely on lazy-load, credential rotation requires
// immediate reconnection to avoid failed queries between the event and the
// next inbound request.
func (c *MultiTenantConsumer) handleCredentialsRotated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.credentials.rotated: closing pools for tenant=%s", evt.TenantID)

	// Close existing pools and remove from cache
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	// Apply jitter to prevent thundering herd when multiple pods react simultaneously
	c.applyJitter(ctx)

	// Eagerly reload tenant config from tenant-manager /connections endpoint.
	// This fetches new credentials from Secrets Manager and rebuilds pools
	// immediately, avoiding a window of failed queries.
	if err := c.loadTenant(ctx, evt.TenantID); err != nil {
		logger.WarnfCtx(ctx, "tenant.credentials.rotated: eager reload failed for tenant=%s (will retry on next request): %v", evt.TenantID, err)
		return nil // non-fatal: next request will trigger lazy-load as fallback
	}

	logger.InfofCtx(ctx, "tenant.credentials.rotated: tenant=%s reconnected with new credentials", evt.TenantID)

	return nil
}

// handleConnectionsUpdated applies new pool settings from the event payload.
// If the postgres manager is set, it delegates to ApplyConnectionSettings.
func (c *MultiTenantConsumer) handleConnectionsUpdated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload event.ConnectionsUpdatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleConnectionsUpdated: unmarshal payload: %w", err)
	}

	logger.InfofCtx(ctx, "tenant.connections.updated: tenant=%s module=%s max_open=%d max_idle=%d",
		evt.TenantID, payload.Module, payload.MaxOpenConns, payload.MaxIdleConns)

	if c.postgres != nil {
		// Build a TenantConfig with the updated settings for ApplyConnectionSettings
		config := buildConfigFromConnectionsPayload(evt.TenantID, payload)
		c.postgres.ApplyConnectionSettings(evt.TenantID, config)
	}

	return nil
}

// buildConfigFromConnectionsPayload creates a minimal TenantConfig with connection
// settings from a ConnectionsUpdatedPayload, suitable for ApplyConnectionSettings.
func buildConfigFromConnectionsPayload(tenantID string, payload event.ConnectionsUpdatedPayload) *core.TenantConfig {
	return &core.TenantConfig{
		ID: tenantID,
		ConnectionSettings: &core.ConnectionSettings{
			MaxOpenConns:     payload.MaxOpenConns,
			MaxIdleConns:     payload.MaxIdleConns,
			StatementTimeout: payload.StatementTimeout,
		},
	}
}

// resolveCacheTTL returns the configured tenant cache TTL or the default.
func (c *MultiTenantConsumer) resolveCacheTTL() time.Duration {
	if c.config.TenantCacheTTL > 0 {
		return c.config.TenantCacheTTL
	}

	return DefaultTenantCacheTTL
}
