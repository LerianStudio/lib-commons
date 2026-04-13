// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package tmratelimit

import (
	"context"
	"errors"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/net/http/ratelimit"
	"github.com/LerianStudio/lib-commons/v4/commons/runtime"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

// Compile-time interface check.
var _ ratelimit.TierResolver = (*CacheTierResolver)(nil)

// ResolverOption configures a CacheTierResolver via functional options.
type ResolverOption func(*CacheTierResolver)

// WithResolverLoader sets the loader used for async cache population on miss.
// When set, a cache miss triggers a fire-and-forget background load so the
// next request for the same tenant can use cached settings.
func WithResolverLoader(l *RateLimitLoader) ResolverOption {
	return func(r *CacheTierResolver) {
		r.loader = l
	}
}

// WithResolverLogger provides a structured logger for the resolver.
// When not provided, a no-op logger is used.
func WithResolverLogger(l log.Logger) ResolverOption {
	return func(r *CacheTierResolver) {
		if l != nil {
			r.logger = l
		}
	}
}

// CacheTierResolver reads rate limit config from RateLimitCache and converts
// core.TierLimit to ratelimit.Tier. It implements ratelimit.TierResolver.
//
// Resolution flow for Resolve:
//  1. Empty tenantID -> (zero, false)
//  2. Cache miss + loader configured -> fire-and-forget async load, return (zero, false)
//  3. settings.ForTier(tierName) -> if not found, return (zero, false)
//  4. Convert TierLimit to Tier and return (tier, true)
type CacheTierResolver struct {
	cache     *RateLimitCache
	fallbacks map[string]ratelimit.Tier
	loader    *RateLimitLoader
	logger    log.Logger
}

// NewCacheTierResolver creates a CacheTierResolver.
//
// Parameters:
//   - cache: rate limit settings cache (must not be nil)
//   - fallbacks: map of tier name to fallback Tier; nil is safe (means no fallbacks)
//   - opts: functional options (e.g., WithResolverLoader, WithResolverLogger)
//
// Returns an error if cache is nil.
func NewCacheTierResolver(
	cache *RateLimitCache,
	fallbacks map[string]ratelimit.Tier,
	opts ...ResolverOption,
) (*CacheTierResolver, error) {
	if cache == nil {
		return nil, errors.New("tmratelimit: cache must not be nil")
	}

	r := &CacheTierResolver{
		cache:     cache,
		fallbacks: fallbacks,
		logger:    log.NewNop(),
	}

	for _, opt := range opts {
		opt(r)
	}

	return r, nil
}

// Resolve returns the rate limit Tier for a tenant and tier name.
//
// Returns (tier, true) if the tenant has explicit configuration for that tier.
// Returns (zero, false) otherwise -- the caller should use Fallback.
//
// On a cache miss with a configured loader, a background goroutine is launched
// to populate the cache. The current request falls back; the next request for
// the same tenant will find the cached value.
func (r *CacheTierResolver) Resolve(tenantID, tierName string) (ratelimit.Tier, bool) {
	if tenantID == "" {
		return ratelimit.Tier{}, false
	}

	settings, cached := r.cache.Get(tenantID)
	if !cached {
		// Fire-and-forget async load so subsequent requests hit the cache.
		if r.loader != nil {
			r.asyncLoad(tenantID)
		}

		return ratelimit.Tier{}, false
	}

	limit, ok := settings.ForTier(tierName)
	if !ok {
		return ratelimit.Tier{}, false
	}

	return convertToTier(tierName, limit), true
}

// Fallback returns the registered fallback Tier for a tier name.
// Returns a zero-value Tier if no fallback is registered for that tier.
func (r *CacheTierResolver) Fallback(tierName string) ratelimit.Tier {
	if r.fallbacks == nil {
		return ratelimit.Tier{}
	}

	tier, ok := r.fallbacks[tierName]
	if !ok {
		return ratelimit.Tier{}
	}

	return tier
}

// asyncLoad launches a background goroutine to load rate limit settings for a
// tenant into the cache. The loader handles its own error logging; errors are
// not propagated to the caller.
func (r *CacheTierResolver) asyncLoad(tenantID string) {
	ctx := context.Background()

	runtime.SafeGoWithContext(
		ctx,
		r.logger,
		"tmratelimit.resolver.async_load",
		runtime.KeepRunning,
		func(ctx context.Context) {
			if err := r.loader.Load(ctx, tenantID); err != nil {
				r.logger.Log(ctx, log.LevelWarn, "async rate limit load failed",
					log.String("tenant_id", tenantID),
					log.Err(err),
				)
			}
		},
	)
}

// convertToTier converts a core.TierLimit to a ratelimit.Tier.
// The tier name is used as the Tier.Name field.
func convertToTier(tierName string, limit *core.TierLimit) ratelimit.Tier {
	return ratelimit.Tier{
		Name:   tierName,
		Max:    limit.Max,
		Window: time.Duration(limit.Window) * time.Second,
	}
}
