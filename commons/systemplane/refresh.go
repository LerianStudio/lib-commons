// Changefeed-driven refresh for the systemplane Client.
//
// When a NOTIFY / change-stream event arrives at onEvent, it is debounced on
// the (namespace, key, tenantID) tuple and — once the debounce window closes —
// dispatched to refreshFromStoreRouted in this file.
//
// The router branches on the sentinel: store.SentinelGlobal ("_global") → legacy
// global refresh (re-reads via store.Get, updates cache, fires OnChange);
// any other value → tenant refresh (re-reads via store.GetTenantValue, updates
// tenantCache, fires OnTenantChange). This is the locked D5 invariant from
// the TRD (§4.3): OnChange and OnTenantChange are mutually exclusive dispatch
// targets based on the single tenant_id field on the refreshed row.
//
// Keeping all refresh code in one file (rather than split across client.go
// and tenant_onchange.go) makes it easier to reason about the dispatch
// invariants as a single unit and keeps client.go focused on lifecycle.
package systemplane

import (
	"context"
	"encoding/json"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// refreshFromStoreRouted is the entry point for debounced changefeed
// refreshes. It dispatches to the global or tenant refresh path based on the
// event's tenant_id.
//
// The tenantID argument always carries the backend-reported value:
// store.SentinelGlobal ("_global") for shared rows, the actual tenant ID otherwise.
// Callers (onEvent in client.go) do not interpret it — they pass through.
func (c *Client) refreshFromStoreRouted(ns, key, tenantID string) {
	if tenantID == store.SentinelGlobal {
		c.refreshGlobalFromStore(ns, key)

		return
	}

	c.refreshTenantFromStore(ns, key, tenantID)
}

// refreshGlobalFromStore re-reads a global row from the backend, updates the
// legacy cache, and fires OnChange subscribers. This is the pre-tenant-scoping
// refresh path preserved verbatim — the behavior contract documented in
// PRD AC8 is that OnChange fires only for tenant_id='_global' events.
func (c *Client) refreshGlobalFromStore(ns, key string) {
	nk := nskey{Namespace: ns, Key: key}

	// 1. Look up registration.
	c.registryMu.RLock()
	def, registered := c.registry[nk]
	c.registryMu.RUnlock()

	if !registered {
		c.logWarn(context.Background(), "changefeed event for unregistered key, skipping",
			log.String("namespace", ns),
			log.String("key", key),
		)

		return
	}

	// 2. Fetch from store with a bounded timeout.
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()

	entry, found, err := c.store.Get(ctx, ns, key)
	if err != nil {
		c.logWarn(ctx, "refresh from store failed",
			log.String("namespace", ns),
			log.String("key", key),
			log.Err(err),
		)

		return
	}

	// 3. Resolve the new value: persisted or default.
	newValue := def.defaultValue

	if found {
		var decoded any
		if err := json.Unmarshal(entry.Value, &decoded); err != nil {
			c.logWarn(ctx, "failed to unmarshal refreshed value, keeping current",
				log.String("namespace", ns),
				log.String("key", key),
				log.Err(err),
			)

			return
		}

		newValue = decoded
	}

	// 4. Update cache.
	c.cacheMu.Lock()
	c.cache[nk] = newValue
	c.cacheMu.Unlock()

	// 5. Fire OnChange subscribers only. Tenant subscribers are untouched
	// because a global row changed, not a per-tenant override (PRD AC8).
	c.fireSubscribers(nk, newValue)
}

// refreshTenantFromStore re-reads a tenant override row from the backend.
// On success (row present), updates tenantCache[tenantID][nk] and fires
// OnTenantChange with the new value. On not-found (row deleted), drops the
// cache entry and fires OnTenantChange with the registered default — the
// tenant has reverted to the global/default value per PRD AC9.
//
// If the key was not registered via RegisterTenantScoped, the event is
// logged and skipped: the dispatcher cannot route a tenant refresh to
// OnTenantChange subscribers for a key that was never declared
// tenant-scoped, and firing OnChange would violate AC8.
func (c *Client) refreshTenantFromStore(ns, key, tenantID string) {
	nk := nskey{Namespace: ns, Key: key}

	// 1. Look up registration. The key must be both registered AND
	// tenant-scoped for this refresh to do anything meaningful. Unknown or
	// globals-only keys are logged and skipped.
	c.registryMu.RLock()
	def, registered := c.registry[nk]
	_, tenantScoped := c.tenantScopedRegistry[nk]
	c.registryMu.RUnlock()

	if !registered {
		c.logWarn(context.Background(), "tenant changefeed event for unregistered key, skipping",
			log.String("namespace", ns),
			log.String("key", key),
			log.String("tenant_id", tenantID),
		)

		return
	}

	if !tenantScoped {
		c.logWarn(context.Background(), "tenant changefeed event for key not registered via RegisterTenantScoped, skipping",
			log.String("namespace", ns),
			log.String("key", key),
			log.String("tenant_id", tenantID),
		)

		return
	}

	// 2. Fetch from store with a bounded timeout. Synthesize a ctx that
	// carries the tenantID so observability middleware reading
	// core.GetTenantIDContext sees the right value on spans and logs
	// emitted under this refresh. The store.GetTenantValue call takes
	// tenantID positionally — the ctx binding is purely for telemetry
	// attribution downstream.
	//
	// bgCtx carries tenantID for downstream OTEL spans; detached from the
	// caller's ctx so refresh doesn't abort on short-lived request
	// contexts. The changefeed echo can arrive long after the request
	// that triggered it returned, so tying the refresh lifetime to a
	// request ctx would drop async echoes silently.
	bgCtx := core.ContextWithTenantID(context.Background(), tenantID)

	ctx, cancel := context.WithTimeout(bgCtx, refreshTimeout)
	defer cancel()

	entry, found, err := c.store.GetTenantValue(ctx, tenantID, ns, key)
	if err != nil {
		c.logWarn(ctx, "tenant refresh from store failed",
			log.String("namespace", ns),
			log.String("key", key),
			log.String("tenant_id", tenantID),
			log.Err(err),
		)

		return
	}

	// 3. Resolve the new value. Not-found means the tenant override was
	// deleted (DELETE event); we drop the cache and report the default so
	// subscribers see the effective post-delete value (PRD AC9 / TRD §4.4).
	if !found {
		c.cacheMu.Lock()
		c.tenantCache.delete(tenantID, nk)
		c.cacheMu.Unlock()

		c.fireTenantSubscribers(nk, tenantID, def.defaultValue)

		return
	}

	var decoded any
	if err := json.Unmarshal(entry.Value, &decoded); err != nil {
		c.logWarn(ctx, "failed to unmarshal refreshed tenant value, keeping current",
			log.String("namespace", ns),
			log.String("key", key),
			log.String("tenant_id", tenantID),
			log.Err(err),
		)

		return
	}

	// 4. Update tenant cache.
	c.cacheMu.Lock()
	c.tenantCache.set(tenantID, nk, decoded)
	c.cacheMu.Unlock()

	// 5. Fire OnTenantChange subscribers only. The legacy OnChange list is
	// untouched — this is the AC8 invariant.
	c.fireTenantSubscribers(nk, tenantID, decoded)
}

// hydrateTenantCache is implemented in tenant_hydration.go. It is invoked
// by Start() in eager mode to populate tenantCache from the backing store.
