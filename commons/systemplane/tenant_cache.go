// Tenant value cache with eager and lazy (bounded LRU) implementations.
//
// The tenantCache holds tenant-specific override values keyed by (tenantID, ns,
// key). Two implementations exist:
//
//   - tenantCacheEager: a bare map[string]map[nskey]any. Zero-allocation hot
//     path. Used when Start() hydrates every row from the store up front.
//
//   - tenantCacheLRU: a bounded LRU backed by container/list. Used when the
//     consumer opts into lazy loading via WithLazyTenantLoad(max). Entries are
//     populated on first read (a cache miss triggers store.GetTenantValue) and
//     the least-recently-used entry is evicted when the total population
//     exceeds max.
//
// Concurrency contract: implementations of the tenantCache interface are NOT
// internally synchronized. Every call site is expected to be holding the
// Client's cacheMu (read for get, write for set/delete). This mirrors the
// existing pattern at set.go:82-84 and refreshFromStore at client.go:353-356
// and keeps the mutex hierarchy flat (no nested RWMutex holds). Task 5 wires
// this lock-under-caller discipline on every tenant Client method.
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
// (bounded LRU) tenant value caches. All methods assume the caller holds the
// Client's cacheMu; no internal synchronization is performed.
type tenantCache interface {
	// get returns the cached value and a hit flag. Lazy implementations
	// additionally promote the entry to most-recently-used.
	get(tenantID string, nk nskey) (any, bool)

	// set stores the value, creating nested maps as needed. In the lazy
	// implementation, if the cache is at capacity before insertion, the
	// least-recently-used entry is evicted to make room.
	set(tenantID string, nk nskey, value any)

	// delete removes a single (tenantID, nk) entry if present. Absent entries
	// are a silent no-op.
	delete(tenantID string, nk nskey)

	// iterate walks every (tenantID, nk, value) currently in the cache. The
	// callback returns true to continue iteration, false to stop. Eager caches
	// surface every entry; lazy caches surface every entry currently resident
	// (cold entries are not observable). Used by Start() hydration and by
	// snapshot helpers.
	iterate(fn func(tenantID string, nk nskey, value any) bool)

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

func (c *tenantCacheEager) iterate(fn func(tenantID string, nk nskey, value any) bool) {
	for tenantID, inner := range c.entries {
		for nk, v := range inner {
			if !fn(tenantID, nk, v) {
				return
			}
		}
	}
}

func (c *tenantCacheEager) mode() tenantLoadMode {
	return tenantLoadEager
}
