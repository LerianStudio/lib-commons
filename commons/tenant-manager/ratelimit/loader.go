// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package tmratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
)

// LoaderOption configures a RateLimitLoader via functional options.
type LoaderOption func(*RateLimitLoader)

// WithLoaderLogger provides a structured logger for the loader.
// When not provided, a no-op logger is used.
func WithLoaderLogger(l log.Logger) LoaderOption {
	return func(rl *RateLimitLoader) {
		if l != nil {
			rl.logger = l
		}
	}
}

// RateLimitLoader fetches rate limit settings from the tenant-manager API and
// caches them in a RateLimitCache. It uses per-tenant mutexes to prevent
// concurrent API calls for the same tenant (double-check cache pattern).
type RateLimitLoader struct {
	client    *RateLimitClient
	cache     *RateLimitCache
	logger    log.Logger
	loadLocks sync.Map // key: tenantID, value: *sync.Mutex
}

// NewRateLimitLoader creates a RateLimitLoader.
//
// Parameters:
//   - client: tenant-manager rate limit API client (must not be nil)
//   - cache: process-local rate limit settings cache (must not be nil)
//   - opts: functional options (e.g., WithLoaderLogger)
//
// Returns an error if client or cache is nil.
func NewRateLimitLoader(client *RateLimitClient, cache *RateLimitCache, opts ...LoaderOption) (*RateLimitLoader, error) {
	if client == nil {
		return nil, errors.New("tmratelimit: client must not be nil")
	}

	if cache == nil {
		return nil, errors.New("tmratelimit: cache must not be nil")
	}

	l := &RateLimitLoader{
		client: client,
		cache:  cache,
		logger: log.NewNop(),
	}

	for _, opt := range opts {
		opt(l)
	}

	return l, nil
}

// Load fetches rate limit settings for a tenant and updates the cache.
//
// Behaviour:
//  1. Acquires a per-tenant mutex (prevents concurrent loads for same tenant).
//  2. Double-checks the cache after acquiring the lock.
//  3. Calls client.FetchSettings if not cached.
//  4. Caches the result (including nil settings to avoid repeated 404s).
//
// Error handling:
//   - (nil, nil) from client: cache.Set(tenantID, nil) -- caches "no config".
//   - Error from client: returns the error without caching.
func (l *RateLimitLoader) Load(ctx context.Context, tenantID string) error {
	return l.loadInternal(ctx, tenantID, false)
}

// Refresh unconditionally re-fetches rate limit settings for a tenant from the
// tenant-manager API, bypassing the double-check cache guard. Use this when an
// event signals that the tenant's rate limit configuration has changed and the
// cached value is known to be stale.
func (l *RateLimitLoader) Refresh(ctx context.Context, tenantID string) error {
	return l.loadInternal(ctx, tenantID, true)
}

// loadInternal is the shared implementation for Load and Refresh.
// When forceRefresh is true, the cache entry is deleted before fetching so the
// double-check guard does not short-circuit.
func (l *RateLimitLoader) loadInternal(ctx context.Context, tenantID string, forceRefresh bool) error {
	// Per-tenant mutex to prevent concurrent API calls for the same tenant.
	lockVal, _ := l.loadLocks.LoadOrStore(tenantID, &sync.Mutex{})

	tenantMu, ok := lockVal.(*sync.Mutex)
	if !ok {
		return fmt.Errorf("tmratelimit: unexpected lock type for tenant %s", tenantID)
	}

	tenantMu.Lock()
	defer tenantMu.Unlock()

	// Clean up the per-tenant lock entry after we release the mutex.
	// The lock only serialises concurrent loads; once the load completes the
	// entry can be safely removed. A subsequent Load/Refresh for the same
	// tenant will create a new mutex via LoadOrStore.
	defer l.loadLocks.Delete(tenantID)

	// When refreshing from an event, delete the stale cache entry so the
	// double-check below does not short-circuit.
	if forceRefresh {
		l.cache.Delete(tenantID)
	}

	// Double-check: another goroutine may have loaded while we waited.
	if _, cached := l.cache.Get(tenantID); cached {
		return nil
	}

	// Fetch from tenant-manager API.
	settings, err := l.client.FetchSettings(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("tmratelimit: failed to load rate limits for tenant %s: %w", tenantID, err)
	}

	// Cache the result. A nil settings value is cached intentionally to avoid
	// repeated API calls for tenants that have no rate limit configuration.
	l.cache.Set(tenantID, settings)

	l.logger.Log(ctx, log.LevelInfo, "loaded rate limit settings",
		log.String("tenant_id", tenantID),
	)

	return nil
}
