// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package cache

import (
	"context"
	"sync"
	"time"
)

// cleanupInterval is the interval at which the background goroutine evicts
// expired entries to prevent unbounded memory growth.
const cleanupInterval = 5 * time.Minute

// cacheEntry holds a cached value together with its absolute expiration time.
type cacheEntry struct {
	value     string
	expiresAt time.Time
}

// isExpired reports whether the entry has passed its expiration time.
// An entry with a zero expiresAt never expires.
func (e cacheEntry) isExpired() bool {
	if e.expiresAt.IsZero() {
		return false
	}

	return time.Now().After(e.expiresAt)
}

// InMemoryCache is a thread-safe, process-local cache with per-key TTL.
// It uses lazy expiration on Get (expired entries are deleted on access) and
// a background goroutine that periodically sweeps all expired entries.
//
// Call Close to stop the background cleanup goroutine when the cache is no
// longer needed. Failing to call Close will leak the goroutine.
type InMemoryCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	done    chan struct{}
}

// NewInMemoryCache creates a new InMemoryCache and starts a background
// goroutine that evicts expired entries every 5 minutes.
func NewInMemoryCache() *InMemoryCache {
	c := &InMemoryCache{
		entries: make(map[string]cacheEntry),
		done:    make(chan struct{}),
	}

	go c.cleanupLoop()

	return c
}

// Get retrieves a cached value by key.
// If the key exists but has expired, it is deleted (lazy expiration) and
// ErrCacheMiss is returned.
func (c *InMemoryCache) Get(_ context.Context, key string) (string, error) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return "", ErrCacheMiss
	}

	if entry.isExpired() {
		// Lazy eviction: promote to write lock and delete
		c.mu.Lock()
		// Re-check under write lock to avoid deleting a fresher entry
		if current, stillExists := c.entries[key]; stillExists && current.isExpired() {
			delete(c.entries, key)
		}
		c.mu.Unlock()

		return "", ErrCacheMiss
	}

	return entry.value, nil
}

// Set stores a value with the given TTL.
// A TTL of zero or negative means the entry never expires.
func (c *InMemoryCache) Set(_ context.Context, key string, value string, ttl time.Duration) error {
	entry := cacheEntry{
		value: value,
	}

	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()

	return nil
}

// Del removes a key from the cache. Returns nil if the key does not exist.
func (c *InMemoryCache) Del(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()

	return nil
}

// Close stops the background cleanup goroutine. After Close returns, no more
// cleanup sweeps will run. Close is safe to call multiple times.
func (c *InMemoryCache) Close() error {
	select {
	case <-c.done:
		// Already closed
	default:
		close(c.done)
	}

	return nil
}

// cleanupLoop runs in a background goroutine and periodically evicts expired
// entries to prevent unbounded memory growth.
func (c *InMemoryCache) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.evictExpired()
		}
	}
}

// evictExpired removes all expired entries from the cache.
func (c *InMemoryCache) evictExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, entry := range c.entries {
		if entry.isExpired() {
			delete(c.entries, key)
		}
	}
}
