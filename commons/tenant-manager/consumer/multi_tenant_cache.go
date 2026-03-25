// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

// DefaultTenantCacheTTL is the default time-to-live for tenant entries in the local cache.
// When an entry expires, the next request triggers a fresh lazy-load from tenant-manager.
const DefaultTenantCacheTTL = 12 * time.Hour

// tenantCacheEntry holds a cached tenant configuration alongside its expiration time.
// When the entry expires, the next Get call evicts it (lazy eviction) and returns (nil, false),
// signalling the caller to re-fetch from tenant-manager.
type tenantCacheEntry struct {
	tenantID  string
	config    *core.TenantConfig
	expiresAt time.Time
}

// expired reports whether the cache entry has passed its expiration time.
func (e *tenantCacheEntry) expired() bool {
	return time.Now().After(e.expiresAt)
}

// tenantCache is a thread-safe, process-local cache for tenant configurations.
// It uses lazy eviction: expired entries are removed on access via Get and TenantIDs.
// This cache is internal to the consumer package and does not replace the cache/memory.go
// InMemoryCache, which serves a different purpose (HTTP client caching).
type tenantCache struct {
	mu      sync.RWMutex
	entries map[string]*tenantCacheEntry
}

// newTenantCache creates an empty tenantCache ready for use.
func newTenantCache() *tenantCache {
	return &tenantCache{
		entries: make(map[string]*tenantCacheEntry),
	}
}

// Get retrieves a tenant cache entry by tenant ID.
// If the entry exists but has expired, it is lazily evicted and (nil, false) is returned.
// If the entry does not exist, (nil, false) is returned.
func (c *tenantCache) Get(tenantID string) (*tenantCacheEntry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[tenantID]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if entry.expired() {
		// Lazy eviction: promote to write lock and delete the expired entry.
		c.mu.Lock()
		// Re-check under write lock to avoid deleting a fresher entry set by another goroutine.
		if current, stillExists := c.entries[tenantID]; stillExists && current.expired() {
			delete(c.entries, tenantID)
		}
		c.mu.Unlock()

		return nil, false
	}

	return entry, true
}

// Set stores a tenant configuration in the cache with the given TTL.
// If an entry already exists for the tenant ID, it is overwritten.
func (c *tenantCache) Set(tenantID string, config *core.TenantConfig, ttl time.Duration) {
	entry := &tenantCacheEntry{
		tenantID:  tenantID,
		config:    config,
		expiresAt: time.Now().Add(ttl),
	}

	c.mu.Lock()
	c.entries[tenantID] = entry
	c.mu.Unlock()
}

// Delete removes a tenant entry from the cache. It is a no-op if the key does not exist.
func (c *tenantCache) Delete(tenantID string) {
	c.mu.Lock()
	delete(c.entries, tenantID)
	c.mu.Unlock()
}

// IsExpired reports whether the entry for tenantID is expired.
// Returns (expired, found). If the entry does not exist, returns (false, false).
// Unlike Get, IsExpired does NOT evict the entry.
func (c *tenantCache) IsExpired(tenantID string) (expired bool, found bool) {
	c.mu.RLock()
	entry, ok := c.entries[tenantID]
	c.mu.RUnlock()

	if !ok {
		return false, false
	}

	return entry.expired(), true
}

// Touch resets the TTL for an existing entry without changing its config.
// Returns true if the entry was found and not expired (TTL was reset).
// Returns false if the entry does not exist or is already expired.
func (c *tenantCache) Touch(tenantID string, ttl time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[tenantID]
	if !ok {
		return false
	}

	if entry.expired() {
		// Entry is already expired; do not resurrect it.
		delete(c.entries, tenantID)

		return false
	}

	entry.expiresAt = time.Now().Add(ttl)

	return true
}

// Len returns the number of entries in the cache, including expired ones.
// For a count excluding expired entries, use len(TenantIDs()).
func (c *tenantCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}

// TenantIDs returns the IDs of all non-expired tenants in the cache.
// Expired entries are lazily evicted during this call.
func (c *tenantCache) TenantIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	ids := make([]string, 0, len(c.entries))

	for tenantID, entry := range c.entries {
		if entry.expired() {
			delete(c.entries, tenantID)

			continue
		}

		ids = append(ids, tenantID)
	}

	return ids
}
