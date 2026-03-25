// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// handleTenantCreated is a no-op: new tenants are lazy-loaded on first request.
func (d *EventDispatcher) handleTenantCreated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.created received tenant=%s: no-op (lazy-load on first request)", evt.TenantID)
	return nil
}

// handleTenantActivated touches the TTL for a cached tenant, keeping it fresh.
func (d *EventDispatcher) handleTenantActivated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	ttl := d.resolveCacheTTL()

	touched := d.cache.Touch(evt.TenantID, ttl)
	if touched {
		logger.InfofCtx(ctx, "tenant.activated: refreshed TTL for tenant=%s", evt.TenantID)
	} else {
		logger.Debugf("tenant.activated: tenant=%s not in cache, skipping TTL refresh", evt.TenantID)
	}

	return nil
}

// handleTenantSuspended removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleTenantSuspended(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.suspended: evicting tenant=%s", evt.TenantID)
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleTenantDeleted removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleTenantDeleted(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.deleted: evicting tenant=%s", evt.TenantID)
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleTenantUpdated touches the TTL for a cached tenant.
func (d *EventDispatcher) handleTenantUpdated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	ttl := d.resolveCacheTTL()

	touched := d.cache.Touch(evt.TenantID, ttl)
	if touched {
		logger.InfofCtx(ctx, "tenant.updated: refreshed TTL for tenant=%s", evt.TenantID)
	} else {
		logger.Debugf("tenant.updated: tenant=%s not in cache, skipping TTL refresh", evt.TenantID)
	}

	return nil
}

// handleServiceAssociated adds the tenant to cache with a minimal config built
// from the event payload and invokes the onTenantAdded callback.
// Actual DB connections are created lazily when the first request arrives.
func (d *EventDispatcher) handleServiceAssociated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload ServiceAssociatedPayload
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

	// Populate Databases from secret_paths
	config.Databases = buildDatabasesFromSecretPaths(payload.SecretPaths)

	// Populate Messaging from payload
	if payload.MessagingConfig != nil && payload.MessagingConfig.RabbitMQSecretPath != "" {
		config.Messaging = &core.MessagingConfig{
			RabbitMQ: &core.RabbitMQConfig{
				// Secret path stored in Host field; actual credentials resolved lazily
				Host: payload.MessagingConfig.RabbitMQSecretPath,
			},
		}
	}

	ttl := d.resolveCacheTTL()
	d.cache.Set(evt.TenantID, config, ttl)

	// Notify consumer to start goroutine
	if d.onTenantAdded != nil {
		d.onTenantAdded(ctx, evt.TenantID)
	}

	return nil
}

// handleServiceDisassociated removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleServiceDisassociated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.disassociated: evicting tenant=%s", evt.TenantID)
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleServiceSuspended removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleServiceSuspended(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.suspended: evicting tenant=%s", evt.TenantID)
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleServicePurged removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleServicePurged(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.purged: evicting tenant=%s", evt.TenantID)
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleServiceReactivated re-adds the tenant to cache and invokes the onTenantAdded callback.
// A random jitter (0-5s) is applied before rebuilding to prevent thundering herd.
func (d *EventDispatcher) handleServiceReactivated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload ServiceReactivatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleServiceReactivated: unmarshal payload: %w", err)
	}

	logger.InfofCtx(ctx, "tenant.service.reactivated: re-adding tenant=%s with jitter", evt.TenantID)

	d.applyJitter(ctx)

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

	// Populate Databases from secret_paths
	config.Databases = buildDatabasesFromSecretPaths(payload.SecretPaths)

	ttl := d.resolveCacheTTL()
	d.cache.Set(evt.TenantID, config, ttl)

	// Notify consumer to start goroutine
	if d.onTenantAdded != nil {
		d.onTenantAdded(ctx, evt.TenantID)
	}

	return nil
}

// handleCredentialsRotated closes existing pools, applies jitter, and eagerly
// reloads tenant config via /connections to reconnect with new credentials.
// Unlike other events that rely on lazy-load, credential rotation requires
// immediate reconnection to avoid failed queries between the event and the
// next inbound request.
func (d *EventDispatcher) handleCredentialsRotated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.credentials.rotated: closing pools for tenant=%s", evt.TenantID)

	// Close existing pools and remove from cache
	d.removeTenant(ctx, evt.TenantID, logger)

	// Apply jitter to prevent thundering herd when multiple pods react simultaneously
	d.applyJitter(ctx)

	// Eagerly reload tenant config from tenant-manager /connections endpoint.
	// This fetches new credentials from Secrets Manager and rebuilds pools
	// immediately, avoiding a window of failed queries.
	if _, err := d.loader.LoadTenant(ctx, evt.TenantID); err != nil {
		logger.WarnfCtx(ctx, "tenant.credentials.rotated: eager reload failed for tenant=%s (will retry on next request): %v", evt.TenantID, err)
		return nil // non-fatal: next request will trigger lazy-load as fallback
	}

	// LoadTenant cached the config; notify consumer to start goroutine.
	if d.onTenantAdded != nil {
		d.onTenantAdded(ctx, evt.TenantID)
	}

	logger.InfofCtx(ctx, "tenant.credentials.rotated: tenant=%s reconnected with new credentials", evt.TenantID)

	return nil
}

// handleConnectionsUpdated applies new pool settings from the event payload.
// If the postgres manager is set, it delegates to ApplyConnectionSettings.
func (d *EventDispatcher) handleConnectionsUpdated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload ConnectionsUpdatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleConnectionsUpdated: unmarshal payload: %w", err)
	}

	logger.InfofCtx(ctx, "tenant.connections.updated: tenant=%s module=%s max_open=%d max_idle=%d",
		evt.TenantID, payload.Module, payload.MaxOpenConns, payload.MaxIdleConns)

	if d.postgres != nil {
		config := buildConfigFromConnectionsPayload(evt.TenantID, payload)
		d.postgres.ApplyConnectionSettings(evt.TenantID, config)
	}

	return nil
}
