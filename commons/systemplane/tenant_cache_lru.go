// Bounded LRU implementation of tenantCache for lazy load mode.
package systemplane

import "container/list"

// lruKey is the composite map key for the LRU's fast-lookup index.
// (tenantID, ns, key) uniquely identifies a cache entry.
type lruKey struct {
	tenantID string
	nk       nskey
}

// lruEntry is the payload stored in each container/list element.
type lruEntry struct {
	key   lruKey
	value any
}

// tenantCacheLRU is a bounded O(1) LRU cache. It wraps a doubly-linked list
// (MRU at Front, LRU at Back) with a map for O(1) key → element lookup. Safe
// only under caller-held cacheMu; see the tenantCache package doc for the
// locking contract.
//
// Why a hand-rolled LRU rather than a library: the tenantCache interface is
// narrow (get/set/delete/iterate), call sites are all internal, and the extra
// dependency buys nothing. container/list is sufficient and well-understood.
type tenantCacheLRU struct {
	maxEntries int
	ll         *list.List // MRU at Front, LRU at Back; elements hold *lruEntry
	index      map[lruKey]*list.Element
}

// newTenantCacheLRU returns a bounded LRU cache. If maxEntries <= 0, an
// unbounded eager cache is returned instead — treating a non-positive bound
// as a disabled feature rather than a configuration error matches the
// existing option conventions (see WithDebounce in options.go).
func newTenantCacheLRU(maxEntries int) tenantCache {
	if maxEntries <= 0 {
		return newTenantCacheEager()
	}

	return &tenantCacheLRU{
		maxEntries: maxEntries,
		ll:         list.New(),
		index:      make(map[lruKey]*list.Element, maxEntries),
	}
}

func (c *tenantCacheLRU) get(tenantID string, nk nskey) (any, bool) {
	elem, ok := c.index[lruKey{tenantID: tenantID, nk: nk}]
	if !ok {
		return nil, false
	}

	// Promote to MRU.
	c.ll.MoveToFront(elem)

	entry, _ := elem.Value.(*lruEntry)

	return entry.value, true
}

func (c *tenantCacheLRU) set(tenantID string, nk nskey, value any) {
	k := lruKey{tenantID: tenantID, nk: nk}

	// Update-in-place: existing entry gets new value and is promoted.
	if elem, ok := c.index[k]; ok {
		entry, _ := elem.Value.(*lruEntry)
		entry.value = value

		c.ll.MoveToFront(elem)

		return
	}

	// New insert. Evict LRU if at capacity before adding so the post-condition
	// len(c.index) <= c.maxEntries always holds.
	if c.ll.Len() >= c.maxEntries {
		c.evictOldest()
	}

	entry := &lruEntry{key: k, value: value}
	elem := c.ll.PushFront(entry)
	c.index[k] = elem
}

func (c *tenantCacheLRU) delete(tenantID string, nk nskey) {
	k := lruKey{tenantID: tenantID, nk: nk}

	elem, ok := c.index[k]
	if !ok {
		return
	}

	c.ll.Remove(elem)
	delete(c.index, k)
}

func (c *tenantCacheLRU) iterate(fn func(tenantID string, nk nskey, value any) bool) {
	// Walk from MRU to LRU. The visit order is deterministic for the snapshot
	// but should NOT be relied on by callers — eager hydration only needs
	// every-entry coverage.
	for e := c.ll.Front(); e != nil; e = e.Next() {
		entry, _ := e.Value.(*lruEntry)
		if !fn(entry.key.tenantID, entry.key.nk, entry.value) {
			return
		}
	}
}

func (c *tenantCacheLRU) mode() tenantLoadMode {
	return tenantLoadLazy
}

// evictOldest removes the least-recently-used entry. Assumes ll.Len() > 0.
func (c *tenantCacheLRU) evictOldest() {
	oldest := c.ll.Back()
	if oldest == nil {
		return
	}

	c.ll.Remove(oldest)

	entry, _ := oldest.Value.(*lruEntry)
	delete(c.index, entry.key)
}
