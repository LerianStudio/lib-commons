// Tenant-aware change notification dispatch for the systemplane Client.
//
// OnTenantChange registers a callback that fires whenever a tenant-specific
// override for (namespace, key) is written or deleted through the backend.
// Callbacks receive the tenant ID so subscribers can distinguish which
// tenant's override changed — a single subscription observes every tenant.
//
// Relationship with OnChange (invariant AC8): OnChange fires exclusively for
// store.SentinelGlobal events. OnTenantChange fires exclusively for tenant events.
// The router in refresh.go enforces this split; see refreshFromStoreRouted.
// A tenant-scoped key's global row changing fires OnChange, not
// OnTenantChange — the split is on the row's tenant_id, not on whether the
// key was registered as tenant-scoped.
package systemplane

import (
	"context"
	"sync"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
)

// OnTenantChange registers a tenant-aware callback invoked whenever the
// specified tenant-scoped key's value changes for any tenant through the
// backend changefeed. The returned function unsubscribes the callback and is
// safe to call multiple times.
//
// The callback signature carries the tenantID so a single subscriber can
// distinguish which tenant's override changed:
//
//	unsub := c.OnTenantChange("global", "fees.fail_closed_default",
//	    func(ns, key, tenantID string, newValue any) {
//	        // ...
//	    })
//	defer unsub()
//
// Callbacks are invoked sequentially with panic recovery via
// runtime.RecoverAndLog; a panicking callback does not prevent subsequent
// subscribers from being notified. The newValue argument is the post-change
// value (the decoded tenant override when present, or the registered default
// when the override was deleted — see refresh.go / TRD §4.4).
//
// On a nil receiver, nil fn, or a key not registered via
// RegisterTenantScoped, OnTenantChange returns a no-op unsubscribe function
// (silent tolerance, mirroring OnChange for unknown keys at onchange.go:22).
// Registering a legacy (non-tenant-scoped) key here also returns a no-op —
// OnChange is the correct surface for those keys, not OnTenantChange.
func (c *Client) OnTenantChange(namespace, key string, fn func(ns, key, tenantID string, newValue any)) (unsubscribe func()) {
	noop := func() {}

	if c == nil || fn == nil {
		return noop
	}

	nk := nskey{Namespace: namespace, Key: key}

	// Silent tolerance for unregistered or globals-only keys. Mirrors
	// OnChange at onchange.go:32-43 except we additionally require the key
	// to be tenant-scoped — OnTenantChange is not a valid surface for a
	// key declared via Register.
	c.registryMu.RLock()
	_, registered := c.registry[nk]
	_, tenantScoped := c.tenantScopedRegistry[nk]
	c.registryMu.RUnlock()

	if !registered || !tenantScoped {
		c.logger.Log(context.Background(), log.LevelDebug, "OnTenantChange called for unregistered or non-tenant-scoped key, returning no-op",
			log.String("namespace", namespace),
			log.String("key", key),
			log.Bool("registered", registered),
			log.Bool("tenant_scoped", tenantScoped),
		)

		return noop
	}

	id := c.nextTenantSubID.Add(1)

	sub := tenantSubscription{
		id: id,
		fn: fn,
	}

	c.tenantSubsMu.Lock()
	c.tenantSubscribers[nk] = append(c.tenantSubscribers[nk], sub)
	c.tenantSubsMu.Unlock()

	// sync.Once guarantees exactly-once execution so callers can safely
	// invoke the unsubscribe multiple times.
	var once sync.Once

	return func() {
		once.Do(func() {
			c.tenantSubsMu.Lock()
			defer c.tenantSubsMu.Unlock()

			subs := c.tenantSubscribers[nk]
			for i, s := range subs {
				if s.id == id {
					c.tenantSubscribers[nk] = append(subs[:i], subs[i+1:]...)
					return
				}
			}
		})
	}
}

// fireTenantSubscribers invokes every OnTenantChange callback registered for
// nk, passing the post-change tenant value. Mirrors fireSubscribers in
// client.go: snapshot the subscriber slice under RLock, release, then invoke
// each callback under an individual panic shield.
//
// CRITICAL invariant: the slice is COPIED before iteration so a callback
// that unsubscribes itself (by calling the returned unsubscribe func) does
// not mutate the slice we are iterating. Without the copy, that sequence
// would either deadlock (on tenantSubsMu) or corrupt the iteration index.
func (c *Client) fireTenantSubscribers(nk nskey, tenantID string, newValue any) {
	c.tenantSubsMu.RLock()
	subs := make([]tenantSubscription, len(c.tenantSubscribers[nk]))
	copy(subs, c.tenantSubscribers[nk])
	c.tenantSubsMu.RUnlock()

	for _, sub := range subs {
		fn := sub.fn

		func() {
			defer runtime.RecoverAndLog(c.logger, "systemplane.ontenantchange")

			fn(nk.Namespace, nk.Key, tenantID, newValue)
		}()
	}
}
