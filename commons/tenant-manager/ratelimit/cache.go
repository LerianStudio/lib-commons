// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package tmratelimit provides a dedicated in-memory cache and HTTP client for
// fetching and storing per-tenant rate limit settings from the tenant-manager
// service. It is designed to be used by rate-limiting middleware in consumer
// services to resolve tenant-specific rate limit tiers.
package tmratelimit

import (
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

// DefaultRateLimitCacheTTL is the default time-to-live for rate limit cache entries.
// Rate limit settings change infrequently, so a 5-minute TTL balances freshness
// with reduced load on the tenant-manager service.
const DefaultRateLimitCacheTTL = 5 * time.Minute

// cacheEntry holds a cached rate limit settings value alongside its expiration time.
// The settings field is intentionally typed as core.RateLimitSettings (which is a map type)
// so that a nil value can be stored to distinguish "tenant has no rate limits" (cache hit,
// nil settings) from "not yet fetched" (cache miss).
type cacheEntry struct {
	settings  core.RateLimitSettings
	expiresAt time.Time
}

// isExpired reports whether the cache entry has passed its expiration time.
func (e *cacheEntry) isExpired() bool {
	return time.Now().After(e.expiresAt)
}

// RateLimitCache is a thread-safe, process-local cache for per-tenant rate limit settings.
// It uses lazy eviction: expired entries are removed on access via Get, not by a background
// goroutine. This avoids goroutine lifecycle complexity while keeping memory bounded by
// access patterns.
type RateLimitCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
}

// CacheOption is a functional option for configuring a RateLimitCache.
type CacheOption func(*RateLimitCache)

// WithCacheTTL sets the time-to-live for cache entries.
// If ttl is zero or negative, DefaultRateLimitCacheTTL is used instead.
func WithCacheTTL(ttl time.Duration) CacheOption {
	return func(c *RateLimitCache) {
		if ttl > 0 {
			c.ttl = ttl
		}
	}
}

// NewRateLimitCache creates an empty RateLimitCache ready for use.
// Without options, the cache uses DefaultRateLimitCacheTTL.
func NewRateLimitCache(opts ...CacheOption) *RateLimitCache {
	c := &RateLimitCache{
		entries: make(map[string]*cacheEntry),
		ttl:     DefaultRateLimitCacheTTL,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Get retrieves the cached rate limit settings for a tenant.
//
// Return values:
//   - (settings, true)  — cache hit; settings may be nil if the tenant has no rate limits.
//   - (nil, false)      — cache miss; the entry does not exist or has expired.
//
// Expired entries are lazily evicted: when Get detects an expired entry under a read lock,
// it promotes to a write lock, double-checks expiry (to avoid deleting a fresher entry set
// by another goroutine), and deletes the stale entry.
func (c *RateLimitCache) Get(tenantID string) (core.RateLimitSettings, bool) {
	c.mu.RLock()
	entry, ok := c.entries[tenantID]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if entry.isExpired() {
		// Lazy eviction: promote to write lock and delete the expired entry.
		c.mu.Lock()
		// Re-check under write lock to avoid deleting a fresher entry set by another goroutine.
		if current, stillExists := c.entries[tenantID]; stillExists && current.isExpired() {
			delete(c.entries, tenantID)
		}
		c.mu.Unlock()

		return nil, false
	}

	return entry.settings, true
}

// Set stores rate limit settings for a tenant in the cache.
// The settings parameter may be nil to record that the tenant has no rate limits configured;
// this distinguishes "no config" (cache hit with nil settings) from "not fetched" (cache miss).
// If an entry already exists for the tenant ID, it is overwritten with a fresh TTL.
func (c *RateLimitCache) Set(tenantID string, settings core.RateLimitSettings) {
	entry := &cacheEntry{
		settings:  settings,
		expiresAt: time.Now().Add(c.ttl),
	}

	c.mu.Lock()
	c.entries[tenantID] = entry
	c.mu.Unlock()
}

// Delete removes a tenant entry from the cache. It is a no-op if the key does not exist.
func (c *RateLimitCache) Delete(tenantID string) {
	c.mu.Lock()
	delete(c.entries, tenantID)
	c.mu.Unlock()
}

// Len returns the number of entries in the cache, including expired ones.
func (c *RateLimitCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}
