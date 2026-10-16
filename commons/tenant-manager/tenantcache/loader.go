// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package tenantcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	obsbridge "github.com/LerianStudio/lib-commons/v7/commons/obs/obsbridge"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/logcompat"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"golang.org/x/sync/singleflight"
)

// TenantLoader fetches tenant configurations from the tenant-manager API and
// caches them in a TenantCache. Concurrent loads of one tenant share a single
// API call (singleflight, with a double-check of the cache).
//
// TenantLoader is a standalone component: any layer (middleware, event listener,
// consumer) can use it. It does NOT manage consumer goroutines or knownTenants
// maps -- those are caller responsibilities.
type TenantLoader struct {
	pmClient         *client.Client
	cache            *TenantCache
	service          string
	cacheTTL         time.Duration
	logger           *logcompat.Logger
	loads            singleflight.Group // one in-flight load per tenant
	onTenantLoaded   func(ctx context.Context, tenantID string)
	onTenantLoadedMu sync.RWMutex
}

// NewTenantLoader creates a TenantLoader.
//
// Parameters:
//   - pmClient: tenant-manager HTTP API client (must not be nil)
//   - cache: process-local tenant config cache (must not be nil)
//   - service: service name for tenant-manager lookups
//   - cacheTTL: TTL for cached entries; if <= 0, DefaultTenantCacheTTL is used
//   - logger: structured logger; nil is safe (falls back to no-op)
func NewTenantLoader(
	pmClient *client.Client,
	cache *TenantCache,
	service string,
	cacheTTL time.Duration,
	logger obs.Logger,
) *TenantLoader {
	if cacheTTL <= 0 {
		cacheTTL = DefaultTenantCacheTTL
	}

	return &TenantLoader{
		pmClient: pmClient,
		cache:    cache,
		service:  service,
		cacheTTL: cacheTTL,
		logger:   logcompat.New(logger),
	}
}

// SetOnTenantLoaded registers a callback that is invoked after a tenant is
// successfully lazy-loaded from the tenant-manager API and cached. The callback
// is NOT called on cache hits -- only on new loads. Passing nil clears the
// callback. This is safe to call before any LoadTenant call.
func (l *TenantLoader) SetOnTenantLoaded(fn func(ctx context.Context, tenantID string)) {
	l.onTenantLoadedMu.Lock()
	l.onTenantLoaded = fn
	l.onTenantLoadedMu.Unlock()
}

// InvalidateClientCache evicts the tenant's entry from the tenant-manager
// client (tier-2) config cache, delegating to pmClient.InvalidateConfig.
//
// It is nil-safe: a nil receiver returns nil without performing any work, and
// when the loader has no pmClient configured it also returns nil. The
// underlying client is a no-op when no cache is wired, so this is always safe
// to call.
func (l *TenantLoader) InvalidateClientCache(ctx context.Context, tenantID, service string) error {
	if l == nil {
		return nil
	}

	_, tracer, _, _ := obsbridge.TrackingFromContext(ctx) //nolint:dogsled // standard tracking extraction

	ctx, span := tracer.Start(ctx, "tenantcache.tenant_loader.invalidate_client_cache")
	defer span.End()

	if l.pmClient == nil {
		return nil
	}

	if err := l.pmClient.InvalidateConfig(ctx, tenantID, service); err != nil {
		wrappedErr := fmt.Errorf("tenantcache: failed to invalidate client cache for tenant %s: %w", tenantID, err)
		libOpentelemetry.HandleSpanError(span, "failed to invalidate client cache", wrappedErr)

		return wrappedErr
	}

	return nil
}

// LoadTenant fetches and caches a tenant's configuration.
//
// Behaviour:
//  1. Concurrent callers for one tenant share one load: one fetch, one result,
//     and at most one circuit breaker failure for all of them.
//  2. The shared load is detached from every caller's cancellation, so a
//     caller hanging up does not fail the others; each caller still stops
//     waiting when its own context ends. The client's own timeout bounds it.
//  3. The load double-checks the cache, calls pmClient.GetTenantConfig on a
//     miss and caches the result with the configured TTL.
//
// Error classification:
//   - TenantSuspendedError / TenantPurgedError: returned as-is (not cached)
//   - ErrTenantNotFound: returned as-is (not cached)
//   - Other errors: wrapped with context
func (l *TenantLoader) LoadTenant(ctx context.Context, tenantID string) (*core.TenantConfig, error) {
	_, tracer, _, _ := obsbridge.TrackingFromContext(ctx) //nolint:dogsled // standard tracking extraction

	ctx, span := tracer.Start(ctx, "tenantcache.tenant_loader.load_tenant")
	defer span.End()

	loads := l.loads.DoChan(tenantID, func() (any, error) {
		return l.fetchAndCache(context.WithoutCancel(ctx), tenantID)
	})

	var res singleflight.Result

	select {
	case res = <-loads:
	case <-ctx.Done():
		res.Err = ctx.Err()
	}

	if err := res.Err; err != nil {
		if core.IsTenantSuspendedError(err) || core.IsTenantPurgedError(err) {
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant suspended or purged", err)
			return nil, err
		}

		if errors.Is(err, core.ErrTenantNotFound) {
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant not found", err)
			return nil, err
		}

		wrappedErr := fmt.Errorf("tenantcache: failed to load tenant %s: %w", tenantID, err)
		libOpentelemetry.HandleSpanError(span, "failed to fetch tenant config", wrappedErr)

		return nil, wrappedErr
	}

	config, _ := res.Val.(*core.TenantConfig)

	return config, nil
}

// fetchAndCache is the body of one shared load.
func (l *TenantLoader) fetchAndCache(ctx context.Context, tenantID string) (*core.TenantConfig, error) {
	// Double-check: a load may have finished between the caller's miss and this one.
	if entry, cached := l.cache.Get(tenantID); cached && entry != nil {
		return entry.Config, nil
	}

	config, err := l.pmClient.GetTenantConfig(ctx, tenantID, l.service)
	if err != nil {
		return nil, err
	}

	l.cache.Set(tenantID, config, l.cacheTTL)

	l.logger.InfofCtx(ctx, "lazy-loaded tenant %s for service %s", tenantID, l.service)

	l.onTenantLoadedMu.RLock()
	cb := l.onTenantLoaded
	l.onTenantLoadedMu.RUnlock()

	if cb != nil {
		cb(ctx, tenantID)
	}

	return config, nil
}
