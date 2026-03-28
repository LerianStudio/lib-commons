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

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// TenantLoader fetches tenant configurations from the tenant-manager API and
// caches them in a TenantCache. It uses per-tenant mutexes to prevent concurrent
// API calls for the same tenant (double-check cache pattern).
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
	loadLocks        sync.Map // per-tenant mutexes (key: tenantID, value: *sync.Mutex)
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
	logger libLog.Logger,
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

// LoadTenant fetches and caches a tenant's configuration.
//
// Behaviour:
//  1. Acquires a per-tenant mutex (prevents concurrent loads for same tenant).
//  2. Double-checks the cache after acquiring the lock.
//  3. Calls pmClient.GetTenantConfig if not cached.
//  4. Caches the result with the configured TTL.
//  5. Returns the config or an error.
//
// Error classification:
//   - TenantSuspendedError / TenantPurgedError: returned as-is (not cached)
//   - ErrTenantNotFound: returned as-is (not cached)
//   - Other errors: wrapped with context
func (l *TenantLoader) LoadTenant(ctx context.Context, tenantID string) (*core.TenantConfig, error) {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled // standard tracking extraction

	ctx, span := tracer.Start(ctx, "tenantcache.tenant_loader.load_tenant")
	defer span.End()

	// Per-tenant mutex to prevent concurrent API calls for the same tenant.
	lockVal, _ := l.loadLocks.LoadOrStore(tenantID, &sync.Mutex{})

	tenantMu, ok := lockVal.(*sync.Mutex)
	if !ok {
		err := fmt.Errorf("tenantcache: unexpected lock type for tenant %s", tenantID)
		libOpentelemetry.HandleSpanError(span, "unexpected lock type", err)

		return nil, err
	}

	tenantMu.Lock()
	defer tenantMu.Unlock()

	// Double-check: maybe another goroutine loaded it while we waited.
	if entry, cached := l.cache.Get(tenantID); cached && entry != nil {
		return entry.Config, nil
	}

	// Fetch from tenant-manager API.
	config, err := l.pmClient.GetTenantConfig(ctx, tenantID, l.service)
	if err != nil {
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

	// Cache the config with TTL.
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
