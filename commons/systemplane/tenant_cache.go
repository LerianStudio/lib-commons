// Tenant value cache with eager and lazy (bounded LRU) implementations.
//
// The tenantCache holds tenant-specific override values keyed by (tenantID, ns,
// key). Two implementations exist:
//
//   - tenantCacheEager: a bare map[string]map[nskey]any. Zero-allocation hot
//     path. Used when Start() hydrates every row from the store up front.
//     Requires caller-held cacheMu (RLock for get; Lock for set/delete) because
//     the bare map itself is not concurrency-safe.
//
//   - tenantCacheLRU: a bounded LRU backed by hashicorp/golang-lru/v2. Used
//     when the consumer opts into lazy loading via WithLazyTenantLoad(max).
//     Entries are populated on first read (a cache miss triggers
//     store.GetTenantValue) and the least-recently-used entry is evicted when
//     the total population exceeds max. The library handles its own internal
//     concurrency — so get() is safe to call under cacheMu.RLock even though
//     it promotes the hit to MRU.
//
// Concurrency contract (READ THIS BEFORE ADDING A CALL SITE):
//
//   - get(): read-only from the caller's perspective. No outer mutation
//     leaks — even the LRU's MRU promotion happens inside the library's own
//     lock. Safe under cacheMu.RLock for BOTH implementations. The lazy
//     GetForTenant path takes advantage of this (tenant_scoped.go) so
//     concurrent hits do not serialize through a write lock.
//
//   - set() / delete(): REQUIRE caller-held cacheMu.Lock. The eager map is not
//     concurrency-safe, and more importantly, the outer write lock coordinates
//     writes across the tenantCache, the legacy cache[nk], and the subscriber
//     and refresh paths that also take cacheMu (see refresh.go,
//     set.go, tenant_scoped.go). The tenantCache implementations themselves do
//     NOT enforce this — it is a contract the callers must honor.
//
// Mutex hierarchy: the tenantCache takes no locks on its own. See client.go
// for the global lock ordering (startMu → registryMu → cacheMu → subsMu →
// tenantSubsMu); tenantCache sits under cacheMu.
package systemplane

// tenantLoadMode selects eager vs lazy tenant cache population at Start().
type tenantLoadMode int

const (
	// tenantLoadEager hydrates every tenant value from the store at Start and
	// keeps every row cached in memory. Zero DB round-trips on GetForTenant.
	// Default mode. Best fit when the total tenant × key population is bounded
	// and small (see PRD §8: ~1,200 entries for 100 tenants × 12 keys).
	tenantLoadEager tenantLoadMode = iota

	// tenantLoadLazy skips tenant hydration at Start and loads values on first
	// read with a bounded LRU. Trades a ~5-10ms first-touch DB round-trip for
	// bounded memory consumption. Selected via WithLazyTenantLoad(max).
	tenantLoadLazy
)

// tenantCache is the abstraction over both eager (unbounded map) and lazy
// (bounded LRU) tenant value caches. See the package doc above for the
// cacheMu-holding requirements per method; implementations do NOT enforce
// concurrency on their own (eager case) or rely on library-internal
// synchronization (LRU case via hashicorp/golang-lru/v2).
type tenantCache interface {
	// get returns the cached value and a hit flag. Safe under cacheMu.RLock
	// for both implementations: the eager map is read-only from this method's
	// perspective, and the LRU's promotion-to-MRU happens under the library's
	// own internal lock (not the outer cacheMu).
	get(tenantID string, nk nskey) (any, bool)

	// set stores the value, creating nested maps as needed. In the lazy
	// implementation, if the cache is at capacity before insertion, the
	// least-recently-used entry is evicted to make room.
	set(tenantID string, nk nskey, value any)

	// delete removes a single (tenantID, nk) entry if present. Absent entries
	// are a silent no-op.
	delete(tenantID string, nk nskey)

	// mode returns the configured load mode. Callers use this to branch
	// between "hydrate at Start" (eager) and "fetch on miss" (lazy) semantics.
	mode() tenantLoadMode
}

// tenantCacheEager is a map-backed cache with no size bound. Used in eager
// mode (the default). Safe only under caller-held cacheMu.
type tenantCacheEager struct {
	entries map[string]map[nskey]any
}

// newTenantCacheEager returns a fresh eager cache with no size bound.
func newTenantCacheEager() tenantCache {
	return &tenantCacheEager{entries: make(map[string]map[nskey]any)}
}

func (c *tenantCacheEager) get(tenantID string, nk nskey) (any, bool) {
	inner, ok := c.entries[tenantID]
	if !ok {
		return nil, false
	}

	v, ok := inner[nk]

	return v, ok
}

func (c *tenantCacheEager) set(tenantID string, nk nskey, value any) {
	inner, ok := c.entries[tenantID]
	if !ok {
		inner = make(map[nskey]any)
		c.entries[tenantID] = inner
	}

	inner[nk] = value
}

func (c *tenantCacheEager) delete(tenantID string, nk nskey) {
	inner, ok := c.entries[tenantID]
	if !ok {
		return
	}

	delete(inner, nk)

	if len(inner) == 0 {
		delete(c.entries, tenantID)
	}
}

func (c *tenantCacheEager) mode() tenantLoadMode {
	return tenantLoadEager
}
