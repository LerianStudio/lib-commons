// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

// loadTenant lazy-loads a tenant's configuration from the tenant-manager API.
// On success it stores the entry in the local cache with TTL, marks the tenant
// as known, and starts a consumer goroutine via ensureConsumerStarted.
//
// A per-tenant mutex (lazyLoadLocks) prevents concurrent lazy-loads for the same
// tenant. If the tenant is already cached (another goroutine loaded it while we
// waited for the lock), loadTenant returns nil immediately.
//
// lazyLoadLocks is separate from consumerLocks to avoid deadlock: loadTenant
// holds a lazyLoadLock and then calls ensureConsumerStarted, which acquires a
// consumerLock for the same tenantID.
//
// Errors from GetTenantConfig are classified:
//   - TenantSuspendedError / TenantPurgedError: returned as-is (don't cache)
//   - ErrTenantNotFound: returned as-is (don't cache)
//   - Other errors: wrapped with context for debugging
func (c *MultiTenantConsumer) loadTenant(ctx context.Context, tenantID string) error {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled // standard tracking extraction

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.load_tenant")
	defer span.End()

	// Per-tenant mutex to prevent concurrent lazy-loads.
	// Uses lazyLoadLocks (not consumerLocks) to avoid deadlock with ensureConsumerStarted.
	lockVal, _ := c.lazyLoadLocks.LoadOrStore(tenantID, &sync.Mutex{})

	tenantMu, ok := lockVal.(*sync.Mutex)
	if !ok {
		err := fmt.Errorf("loadTenant: unexpected lock type for tenant %s", tenantID)
		libOpentelemetry.HandleSpanError(span, "unexpected lock type", err)

		return err
	}

	tenantMu.Lock()
	defer tenantMu.Unlock()

	// Double-check: maybe another goroutine loaded it while we waited
	if entry, cached := c.cache.Get(tenantID); cached && entry != nil {
		return nil
	}

	// Fetch from tenant-manager
	config, err := c.pmClient.GetTenantConfig(ctx, tenantID, c.config.Service)
	if err != nil {
		if core.IsTenantSuspendedError(err) || core.IsTenantPurgedError(err) {
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant suspended or purged", err)
			return err
		}

		if errors.Is(err, core.ErrTenantNotFound) {
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant not found", err)
			return err
		}

		wrappedErr := fmt.Errorf("loadTenant: failed to fetch config for tenant %s: %w", tenantID, err)
		libOpentelemetry.HandleSpanError(span, "failed to fetch tenant config", wrappedErr)

		return wrappedErr
	}

	// Cache the tenant config with TTL
	ttl := c.config.TenantCacheTTL
	if ttl <= 0 {
		ttl = DefaultTenantCacheTTL
	}

	c.cache.Set(tenantID, config, ttl)

	// Mark as known and start consumer
	c.mu.Lock()
	c.knownTenants[tenantID] = true
	c.mu.Unlock()

	c.ensureConsumerStarted(ctx, tenantID)

	c.logger.InfofCtx(ctx, "lazy-loaded tenant %s for service %s", tenantID, c.config.Service)

	return nil
}
