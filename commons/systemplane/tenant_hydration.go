// Tenant cache eager-mode hydration helper.
//
// Extracted from refresh.go to keep that file focused on the runtime
// changefeed refresh path (refreshFromStoreRouted / refreshGlobalFromStore /
// refreshTenantFromStore). Hydration runs exactly once per Client lifetime,
// at Start in eager mode, and its concerns (list + decode + populate under
// a single cache lock window) don't overlap with the per-event refresh
// machinery.
//
// Behavior is preserved verbatim: Start calls hydrateTenantCache(ctx) and
// whatever this function does is what it used to do.
package systemplane

import (
	"context"
	"encoding/json"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// hydrationPageSize is the keyset page size hydrateTenantCache uses when
// streaming tenant overrides from the store. Chosen to cap peak hydration
// memory at ~O(1000 × value-size) instead of the unbounded O(total-rows) the
// pre-pagination implementation held before batching (C2). Rows still land in
// tenantCache under a single cache-lock window at the end, but the per-page
// decode keeps the store-side result set bounded.
const hydrationPageSize = 1000

// hydrateTenantCache loads every tenant-scoped row from the backing store
// into tenantCache. Called by Start() in eager mode only. Failures here are
// non-fatal because the lazy-mode fallback (miss-populate) already handles
// the case where tenantCache is empty; a hydration failure simply degrades
// to lazy-like behavior on subsequent GetForTenant calls.
//
// Rows for unregistered keys or keys not registered via RegisterTenantScoped
// are logged and skipped — they signal drift between the running binary and
// the underlying DB but are not fatal.
//
// Pagination: hydrate pulls rows in keyset pages of `hydrationPageSize` rows
// ordered by (namespace, key, tenant_id). End-of-stream is a short page. This
// bounds peak hydration memory (C2) while letting Start complete in a single
// eager pass.
//
// Locking contract: this function MUST NOT hold registryMu across
// json.Unmarshal or any other per-row work. At 10k+ entries, a held
// registry lock during per-row unmarshal would stall concurrent Register
// calls for 100-500ms. Register is supposed to be pre-Start, but the
// invariant is cheap to preserve and forestalls future misuse. We snapshot
// the tenant-scoped registration set under a short registryMu.RLock, then
// iterate + unmarshal outside the lock, populating tenantCache under a
// single cacheMu.Lock window instead of N lock/unlock cycles.
func (c *Client) hydrateTenantCache(ctx context.Context) {
	// Snapshot only the registry state the decode loop needs. Holding the
	// lock just for the snapshot — not the unmarshal — keeps concurrent
	// Register calls unblocked for O(10k) entries.
	type regState struct {
		registered   bool
		tenantScoped bool
	}

	c.registryMu.RLock()

	regSnap := make(map[nskey]regState, len(c.registry))
	for nk := range c.registry {
		_, tenantScoped := c.tenantScopedRegistry[nk]
		regSnap[nk] = regState{registered: true, tenantScoped: tenantScoped}
	}

	c.registryMu.RUnlock()

	// Decode and filter outside the registry lock. Successful decodes are
	// collected into a batch so the cache is populated under a single
	// cacheMu.Lock window at the end.
	type hydrateItem struct {
		tenantID string
		nk       nskey
		value    any
	}

	var (
		batch                                   []hydrateItem
		afterNamespace, afterKey, afterTenantID string
	)

	for {
		// ListTenantOverrides is the server-side-filtered variant that omits
		// the _global rows already seeded by the earlier global hydrate pass.
		// On clusters dominated by globals this roughly halves hydration
		// transfer (see TRD §4.5); on balanced clusters it still eliminates
		// the per-row discard branch the old code ran in Go.
		page, err := c.store.ListTenantOverrides(ctx, afterNamespace, afterKey, afterTenantID, hydrationPageSize)
		if err != nil {
			c.logWarn(ctx, "tenant hydration failed, falling back to miss-populate",
				log.Err(err),
			)

			return
		}

		for _, entry := range page {
			nk := nskey{Namespace: entry.Namespace, Key: entry.Key}

			state, registered := regSnap[nk]
			if !registered {
				c.logWarn(ctx, "tenant row for unregistered key, skipping",
					log.String("namespace", entry.Namespace),
					log.String("key", entry.Key),
					log.String("tenant_id", entry.TenantID),
				)

				continue
			}

			if !state.tenantScoped {
				c.logWarn(ctx, "tenant row for key not registered via RegisterTenantScoped, skipping",
					log.String("namespace", entry.Namespace),
					log.String("key", entry.Key),
					log.String("tenant_id", entry.TenantID),
				)

				continue
			}

			var decoded any
			if err := json.Unmarshal(entry.Value, &decoded); err != nil {
				c.logWarn(ctx, "failed to unmarshal tenant value during hydration, skipping",
					log.String("namespace", entry.Namespace),
					log.String("key", entry.Key),
					log.String("tenant_id", entry.TenantID),
					log.Err(err),
				)

				continue
			}

			batch = append(batch, hydrateItem{tenantID: entry.TenantID, nk: nk, value: decoded})
		}

		// Short page → end of stream.
		if len(page) < hydrationPageSize {
			break
		}

		last := page[len(page)-1]
		afterNamespace, afterKey, afterTenantID = last.Namespace, last.Key, last.TenantID
	}

	if len(batch) == 0 {
		return
	}

	// Single lock window for the whole batch — avoids N lock/unlock cycles.
	c.cacheMu.Lock()
	for _, it := range batch {
		c.tenantCache.set(it.tenantID, it.nk, it.value)
	}
	c.cacheMu.Unlock()
}
