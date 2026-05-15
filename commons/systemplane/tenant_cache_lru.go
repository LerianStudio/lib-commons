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
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
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
//
// logger is used only to surface the defensive fallback branch below; a nil
// logger is tolerated (callers like unit tests pass nil or log.NewNop()).
// In practice the fallback is unreachable — lru.New only errors on size <=
// 0, which the guard above already rejects — but when it DOES fire we want
// a loud signal rather than a silent mode switch, because an unbounded
// eager cache in a long-running service will leak memory if tenant
// overrides churn.
func newTenantCacheLRU(maxEntries int, logger log.Logger) tenantCache {
	if maxEntries <= 0 {
		return newTenantCacheEager()
	}

	cache, err := lru.New[lruKey, any](maxEntries)
	if err != nil || cache == nil {
		if !nilcheck.Interface(logger) {
			logger.Log(context.Background(), log.LevelWarn,
				"systemplane: bounded LRU tenant cache init failed; falling back to unbounded eager cache",
				log.Int("max_entries", maxEntries),
				log.Err(err),
			)
		}

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
