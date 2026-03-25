// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package tenantcache provides a thread-safe, process-local cache for tenant
// configurations with TTL-based lazy eviction. It is designed to be shared
// across consumer, middleware, and event listener layers.
package tenantcache

import (
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

// DefaultTenantCacheTTL is the default time-to-live for tenant entries in the local cache.
// When an entry expires, the next request triggers a fresh lazy-load from tenant-manager.
const DefaultTenantCacheTTL = 12 * time.Hour

// TenantCacheEntry holds a cached tenant configuration alongside its expiration time.
// When the entry expires, the next Get call evicts it (lazy eviction) and returns (nil, false),
// signalling the caller to re-fetch from tenant-manager.
type TenantCacheEntry struct {
	TenantID  string
	Config    *core.TenantConfig
	ExpiresAt time.Time
}

// IsExpired reports whether the cache entry has passed its expiration time.
func (e *TenantCacheEntry) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

// TenantCache is a thread-safe, process-local cache for tenant configurations.
// It uses lazy eviction: expired entries are removed on access via Get and TenantIDs.
type TenantCache struct {
	mu      sync.RWMutex
	entries map[string]*TenantCacheEntry
}

// NewTenantCache creates an empty TenantCache ready for use.
func NewTenantCache() *TenantCache {
	return &TenantCache{
		entries: make(map[string]*TenantCacheEntry),
	}
}

// Get retrieves a tenant cache entry by tenant ID.
// If the entry exists but has expired, it is lazily evicted and (nil, false) is returned.
// If the entry does not exist, (nil, false) is returned.
func (c *TenantCache) Get(tenantID string) (*TenantCacheEntry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[tenantID]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if entry.IsExpired() {
		// Lazy eviction: promote to write lock and delete the expired entry.
		c.mu.Lock()
		// Re-check under write lock to avoid deleting a fresher entry set by another goroutine.
		if current, stillExists := c.entries[tenantID]; stillExists && current.IsExpired() {
			delete(c.entries, tenantID)
		}
		c.mu.Unlock()

		return nil, false
	}

	return entry, true
}

// Set stores a tenant configuration in the cache with the given TTL.
// If an entry already exists for the tenant ID, it is overwritten.
func (c *TenantCache) Set(tenantID string, config *core.TenantConfig, ttl time.Duration) {
	entry := &TenantCacheEntry{
		TenantID:  tenantID,
		Config:    config,
		ExpiresAt: time.Now().Add(ttl),
	}

	c.mu.Lock()
	c.entries[tenantID] = entry
	c.mu.Unlock()
}

// Delete removes a tenant entry from the cache. It is a no-op if the key does not exist.
func (c *TenantCache) Delete(tenantID string) {
	c.mu.Lock()
	delete(c.entries, tenantID)
	c.mu.Unlock()
}

// IsExpired reports whether the entry for tenantID is expired.
// Returns (expired, found). If the entry does not exist, returns (false, false).
// Unlike Get, IsExpired does NOT evict the entry.
func (c *TenantCache) IsExpired(tenantID string) (expired bool, found bool) {
	c.mu.RLock()
	entry, ok := c.entries[tenantID]
	c.mu.RUnlock()

	if !ok {
		return false, false
	}

	return entry.IsExpired(), true
}

// Touch resets the TTL for an existing entry without changing its config.
// Returns true if the entry was found and not expired (TTL was reset).
// Returns false if the entry does not exist or is already expired.
func (c *TenantCache) Touch(tenantID string, ttl time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[tenantID]
	if !ok {
		return false
	}

	if entry.IsExpired() {
		// Entry is already expired; do not resurrect it.
		delete(c.entries, tenantID)

		return false
	}

	entry.ExpiresAt = time.Now().Add(ttl)

	return true
}

// Len returns the number of entries in the cache, including expired ones.
// For a count excluding expired entries, use len(TenantIDs()).
func (c *TenantCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}

// TenantIDs returns the IDs of all non-expired tenants in the cache.
// Expired entries are lazily evicted during this call.
//
// Note: this method acquires the write lock for the entire iteration to perform
// lazy eviction (deleting expired entries). For caches with many entries, this
// may cause contention with concurrent Get/Set operations. An alternative for
// high-throughput scenarios would be a background janitor goroutine, but the
// current approach avoids the complexity of additional goroutine lifecycle management.
func (c *TenantCache) TenantIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	ids := make([]string, 0, len(c.entries))

	for tenantID, entry := range c.entries {
		if entry.IsExpired() {
			delete(c.entries, tenantID)

			continue
		}

		ids = append(ids, tenantID)
	}

	return ids
}
