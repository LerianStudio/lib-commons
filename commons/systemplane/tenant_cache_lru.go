// Bounded LRU implementation of tenantCache for lazy load mode.
//
// Backed by hashicorp/golang-lru/v2, whose Cache[K, V] maintains atomic LRU
// order under its own internal RWMutex. This means the library's Get (which
// promotes the hit to MRU) is safe under a caller-held cacheMu.RLock — the
// promotion is synchronized by the library's own lock, not by the outer
// cacheMu. This is the key to unblocking concurrent reads on the lazy hit
// path (see tenant_scoped.go GetForTenant).
package systemplane

import (
	lru "github.com/hashicorp/golang-lru/v2"
)

// lruKey is the composite map key for the LRU's fast-lookup index.
// (tenantID, namespace, key) uniquely identifies a cache entry. A struct
// key avoids the per-event string concat allocation the old encoded key
// required and keeps the composite unambiguous (no delimiter collisions).
type lruKey struct {
	tenantID  string
	namespace string
	key       string
}

// tenantCacheLRU is a bounded LRU backed by hashicorp/golang-lru/v2. Unlike
// the tenantCacheEager map-backed cache, the library's Cache type carries
// its own internal RWMutex, so get() is safe under a caller-held cacheMu
// RLock — promotion to MRU happens atomically inside the library.
//
// The outer cacheMu.Lock() on set/delete is still required to coordinate
// with the legacy cache map and subscriber/refresh paths that take cacheMu
// for cross-structure writes; see tenant_cache.go for the full contract.
type tenantCacheLRU struct {
	cache *lru.Cache[lruKey, any]
}

// newTenantCacheLRU returns a bounded LRU cache. If maxEntries <= 0, an
// unbounded eager cache is returned instead — treating a non-positive bound
// as a disabled feature rather than a configuration error matches the
// existing option conventions (see WithDebounce in options.go).
func newTenantCacheLRU(maxEntries int) tenantCache {
	if maxEntries <= 0 {
		return newTenantCacheEager()
	}

	// lru.New only errors on size <= 0, which we already guarded above.
	// The error is unreachable in practice; we fall back to eager defensively.
	cache, err := lru.New[lruKey, any](maxEntries)
	if err != nil || cache == nil {
		return newTenantCacheEager()
	}

	return &tenantCacheLRU{cache: cache}
}

func (c *tenantCacheLRU) get(tenantID string, nk nskey) (any, bool) {
	return c.cache.Get(lruKey{tenantID: tenantID, namespace: nk.Namespace, key: nk.Key})
}

func (c *tenantCacheLRU) set(tenantID string, nk nskey, value any) {
	c.cache.Add(lruKey{tenantID: tenantID, namespace: nk.Namespace, key: nk.Key}, value)
}

func (c *tenantCacheLRU) delete(tenantID string, nk nskey) {
	c.cache.Remove(lruKey{tenantID: tenantID, namespace: nk.Namespace, key: nk.Key})
}

func (c *tenantCacheLRU) mode() tenantLoadMode {
	return tenantLoadLazy
}
