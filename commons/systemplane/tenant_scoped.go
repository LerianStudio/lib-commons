// Tenant-scoped registration and read/write/delete/list paths for the
// systemplane Client.
//
// This file covers the five primary tenant methods — RegisterTenantScoped,
// SetForTenant, GetForTenant, DeleteForTenant, ListTenantsForKey — plus the
// shared extractTenantID helper. The tenant-aware subscribe surface
// (OnTenantChange / fireTenantSubscribers) lives in tenant_onchange.go; the
// typed accessor mirrors (GetStringForTenant, GetIntForTenant, etc.) live in
// tenant_scoped_accessors.go. The backend-facing wrappers (span + marshaling
// + error prefix) live in tenant_storage.go.
//
// Dataflow summary (see TRD §4.1-4.5 for the full spec):
//
//	Set:    ctx → validate tenant → registry guard → validator → persist
//	        → write-through tenantCache. Subscribers fire from changefeed echo.
//	Get:    ctx → validate tenant → registry guard → tenantCache → legacy
//	        cache → registered default.
//	Delete: ctx → validate tenant → registry guard → persist delete →
//	        write-through tenantCache delete. Subscribers fire from
//	        changefeed echo with newValue = default.
//	List:   registry guard → backend list. No ctx required (admin-style).
package systemplane

import (
	"context"
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// RegisterTenantScoped declares a tenant-scoped configuration key with its
// default value and optional key options. The key behaves identically to one
// registered via Register from the perspective of the existing public API
// (Get, Set, OnChange, List) — the default value and legacy global cache are
// untouched — but also becomes eligible for tenant-specific overrides via the
// Task 5 methods (SetForTenant, GetForTenant, DeleteForTenant, OnTenantChange).
//
// Key options:
//   - WithDescription — human-readable description surfaced in admin responses.
//   - WithValidator   — runs on the default value at registration AND on every
//     per-tenant write.
//   - WithRedaction   — applied by admin handlers AND log renderers; applies
//     identically to the global row and to every tenant override.
//
// Semantics:
//   - Must be called before Start(); returns ErrRegisterAfterStart otherwise.
//   - Registering the same (namespace, key) twice — via any mix of Register
//     and RegisterTenantScoped — returns ErrDuplicateKey.
//   - If a validator is configured and it rejects the defaultValue, returns
//     ErrValidation. A broken default would cause silent misbehavior later.
//   - Seeds the legacy cache[nk] = defaultValue under cacheMu so a pre-Start
//     Get call returns the default (same contract as Register).
//
// # Mutable defaults
//
// Avoid mutable defaults (slices, maps, pointers to shared state). The
// registered default is held by reference and shared across every tenant
// that falls through to it — a subscriber (or reader) mutating the default
// is visible to every other tenant's subsequent reads and to every
// OnTenantChange delete echo (which dispatches def.defaultValue). The
// blast radius is N tenants × K keys. Prefer value types (string, int,
// bool, duration), or wrap slices/maps in a defensive copy the caller
// owns. This same caveat applies to Register; tenant scoping widens the
// surface, not the shape.
//
// Concurrency: the two writes (registry insert + cacheMu seed) happen under
// separate locks; no other goroutine can observe the in-between state because
// RegisterTenantScoped is only legal before Start (see the started.Load guard
// above). After Start, the registry and tenantScopedRegistry maps are
// read-only, so the single-write-then-freeze pattern matches Register exactly.
func (c *Client) RegisterTenantScoped(namespace, key string, defaultValue any, opts ...KeyOption) error {
	if c == nil || c.closed.Load() {
		return ErrClosed
	}

	if c.started.Load() {
		return ErrRegisterAfterStart
	}

	if err := validateKeyArgs(namespace, key); err != nil {
		return err
	}

	nk := nskey{Namespace: namespace, Key: key}

	// Build the key definition from defaults + options. Same shape as Register
	// so the rest of the Client treats tenant-scoped and globals-only keys
	// uniformly for description, validator, and redaction lookups.
	def := keyDef{
		defaultValue: defaultValue,
		redaction:    RedactNone,
	}

	for _, o := range opts {
		o(&def)
	}

	// Validate the default value if a validator is set. Mirrors register.go
	// behavior — we prefer a fail-fast signal at registration over a confusing
	// validation error emerging later from a tenant write.
	if def.validator != nil {
		if err := def.validator(defaultValue); err != nil {
			return fmt.Errorf("%w: default value rejected: %w", ErrValidation, err)
		}
	}

	// Atomically insert into both registry and tenantScopedRegistry under the
	// same registryMu write. No other writer runs concurrently (this path is
	// pre-Start and single-threaded for the typical consumer, but the lock is
	// still correct for defensive concurrent registration).
	c.registryMu.Lock()

	if _, exists := c.registry[nk]; exists {
		c.registryMu.Unlock()

		return fmt.Errorf("%w: %s/%s", ErrDuplicateKey, namespace, key)
	}

	c.registry[nk] = def
	c.tenantScopedRegistry[nk] = struct{}{}

	c.registryMu.Unlock()

	// Seed the legacy cache with the default so a pre-Start Get returns the
	// default value (same contract as Register's implicit seeding at Start —
	// see client.go:182-184). We do it eagerly here so the behavior holds
	// even if Start is never called (e.g. in a Client built via NewForTesting
	// and discarded without Start).
	c.cacheMu.Lock()
	c.cache[nk] = defaultValue
	c.cacheMu.Unlock()

	return nil
}

// extractTenantID pulls a tenant ID from ctx and validates it against
// core.IsValidTenantID. Returns the tenant ID verbatim (as stored in ctx
// by core.ContextWithTenantID — no trimming or normalization) on success,
// or an error on any of:
//
//   - ctx has no tenant ID   → ErrMissingTenantContext
//   - tenant ID fails regex  → ErrInvalidTenantID
//   - tenant ID == sentinel  → ErrInvalidTenantID (cannot collide with the
//     "_global" shared-row sentinel per decision D2)
//
// Decision D8 (locked): tenant-scoped reads and writes never silently fall
// back to a shared global when the tenant is missing. This is the only
// extraction path used by every tenant-scoped method — centralizing it here
// ensures uniform fail-closed behavior.
func extractTenantID(ctx context.Context) (string, error) {
	id := core.GetTenantIDContext(ctx)
	if id == "" {
		return "", fmt.Errorf("%w", ErrMissingTenantContext)
	}

	if id == store.SentinelGlobal {
		return "", fmt.Errorf("%w: tenantID must not be the %q sentinel", ErrInvalidTenantID, store.SentinelGlobal)
	}

	if !core.IsValidTenantID(id) {
		return "", fmt.Errorf("%w", ErrInvalidTenantID)
	}

	return id, nil
}

// requireTenantScoped looks up nk in the registry and verifies both that the
// key is registered AND that it was registered via RegisterTenantScoped.
// Returns the resolved keyDef on success.
//
// Errors:
//   - Missing from registry          → ErrUnknownKey
//   - Registered but not tenant-scoped → ErrTenantScopeNotRegistered
func (c *Client) requireTenantScoped(namespace, key string) (keyDef, nskey, error) {
	nk := nskey{Namespace: namespace, Key: key}

	// Nil-receiver guard: matches the ErrClosed pattern used by every public
	// tenant-scoped entry point. Every caller of requireTenantScoped also
	// guards against nil at the public-method level, but the defensive check
	// here forestalls a nil-pointer panic if a future caller forgets.
	if c == nil {
		return keyDef{}, nk, ErrClosed
	}

	c.registryMu.RLock()
	def, registered := c.registry[nk]
	_, tenantScoped := c.tenantScopedRegistry[nk]
	c.registryMu.RUnlock()

	if !registered {
		return keyDef{}, nk, fmt.Errorf("%w: %s/%s", ErrUnknownKey, namespace, key)
	}

	if !tenantScoped {
		return keyDef{}, nk, fmt.Errorf("%w: %s/%s", ErrTenantScopeNotRegistered, namespace, key)
	}

	return def, nk, nil
}

// SetForTenant persists a tenant-specific override for (namespace, key)
// scoped to the tenant ID carried in ctx. The value is validated against
// the key's registered validator (if any), JSON-marshaled, persisted to
// the backing store, and the in-memory tenantCache is updated immediately
// for same-process read consistency.
//
// Subscribers are NOT fired from SetForTenant. The changefeed echo drives
// OnTenantChange notifications, preserving the set.go:18-21 invariant that
// subscribers observe *backend* state changes, not in-process writes.
//
// Returns:
//   - ErrClosed               — receiver is nil or the Client is closed
//   - ErrNotStarted           — Start has not been called
//   - ErrMissingTenantContext — ctx carries no tenant ID (D8, fail-closed)
//   - ErrInvalidTenantID      — tenant ID fails validation or equals "_global"
//   - ErrUnknownKey           — (namespace, key) was never registered
//   - ErrTenantScopeNotRegistered — key was registered via Register (not
//     RegisterTenantScoped), so tenant overrides are not permitted
//   - ErrValidation           — validator rejected value OR value is not
//     JSON-serializable
//   - any wrapped store error from the backend (Postgres / MongoDB /
//     TestStore)
func (c *Client) SetForTenant(ctx context.Context, namespace, key string, value any, actor string) error {
	if c == nil || c.closed.Load() {
		return ErrClosed
	}

	if !c.started.Load() {
		return ErrNotStarted
	}

	tenantID, err := extractTenantID(ctx)
	if err != nil {
		return err
	}

	def, nk, err := c.requireTenantScoped(namespace, key)
	if err != nil {
		return err
	}

	// Run the registered validator — same chain as global Set at
	// set.go:46-50. A rejected value is reported as ErrValidation with the
	// validator's own error wrapped for diagnosis.
	if def.validator != nil {
		if err := def.validator(value); err != nil {
			return fmt.Errorf("%w: %w", ErrValidation, err)
		}
	}

	// Build the Entry. TenantID here is informational only — the store
	// contract ignores Entry.TenantID and uses the separate tenantID
	// argument as authoritative (internal/store/store.go:72-76).
	entry := store.Entry{
		Namespace: namespace,
		Key:       key,
		TenantID:  tenantID,
		UpdatedBy: actor,
	}

	canonical, err := c.persistTenantValue(ctx, tenantID, entry, value)
	if err != nil {
		return err
	}

	// Write-through cache: update immediately so a subsequent GetForTenant
	// in the same process sees the new override without waiting for the
	// changefeed roundtrip. Uses the canonical (JSON round-tripped) value
	// so type agreement with refresh.go is guaranteed (set.go:70-78
	// precedent).
	c.cacheMu.Lock()
	c.tenantCache.set(tenantID, nk, canonical)
	c.cacheMu.Unlock()

	return nil
}

// GetForTenant returns the current value for (namespace, key) scoped to the
// tenant ID carried in ctx.
//
// Resolution order (TRD §4.2):
//  1. tenantCache[tenantID][nk] — per-tenant override (hot path)
//  2. cache[nk] — shared global value (override absent, global was set)
//  3. def.defaultValue — neither override nor global persisted (startup)
//
// found is true whenever a value can be returned. It is only false when
// err is non-nil (a nil-safety or registration error). The "no tenant
// override yet" case always resolves to the global or default and
// therefore returns (value, true, nil).
//
// In lazy mode, a tenantCache miss triggers a single-flight
// store.GetTenantValue with a 5s timeout. Store errors during the miss-
// populate path are logged and swallowed — the method falls through to
// the global/default cascade so a degraded store does not block reads.
//
// Errors (value is nil, found is false):
//   - ErrClosed, ErrNotStarted, ErrMissingTenantContext, ErrInvalidTenantID,
//     ErrUnknownKey, ErrTenantScopeNotRegistered
func (c *Client) GetForTenant(ctx context.Context, namespace, key string) (any, bool, error) {
	if c == nil || c.closed.Load() {
		return nil, false, ErrClosed
	}

	if !c.started.Load() {
		return nil, false, ErrNotStarted
	}

	tenantID, err := extractTenantID(ctx)
	if err != nil {
		return nil, false, err
	}

	def, nk, err := c.requireTenantScoped(namespace, key)
	if err != nil {
		return nil, false, err
	}

	// 1. Try the tenant cache under RLock. Per the tenantCache contract in
	// tenant_cache.go, BOTH implementations (eager map and LRU) are safe
	// for read-only get() under cacheMu.RLock: the eager map is read-only
	// from this method's perspective, and the LRU's MRU-promotion happens
	// under the library's OWN internal lock (hashicorp/golang-lru/v2), not
	// the outer cacheMu. Concurrent hits therefore do not serialize
	// through a write lock — critical for lazy hot-path throughput.
	c.cacheMu.RLock()
	v, hit := c.tenantCache.get(tenantID, nk)
	c.cacheMu.RUnlock()

	if hit {
		return v, true, nil
	}

	// 1b. Lazy-mode miss: delegate to the helper so this method stays
	// focused on the cascade (tenantCache → legacy global → default).
	if c.tenantLoadMode == tenantLoadLazy {
		if val, found, handled := c.getForTenantLazyMissLocked(ctx, tenantID, namespace, key, nk); handled {
			return val, found, nil
		}
	}

	// 2. Fall through to the legacy global cache. This is the D3 fallthrough
	// contract — tenant without an override sees whatever Set wrote to the
	// global row (or the default if Set was never called).
	c.cacheMu.RLock()
	globalVal, hasGlobal := c.cache[nk]
	c.cacheMu.RUnlock()

	if hasGlobal {
		return globalVal, true, nil
	}

	// 3. Registered default. Never errors on "no override" — GetForTenant
	// always returns a value when the key is correctly registered.
	return def.defaultValue, true, nil
}

// getForTenantLazyMissLocked executes the lazy-mode cache-miss path:
// single-flight coalesced fetch from the backend, populate the LRU on a hit,
// and swallow-with-log on failure (falling through to the global/default
// cascade in the caller).
//
// Return contract:
//   - (value, true, true)  — override present in the store (including nil-value
//     overrides — "found=true" is the sole signal, the value may legitimately
//     be nil).
//   - (nil, false, false)  — no override; caller proceeds with the cascade.
//   - (nil, false, false)  — fetch failed; caller proceeds with the cascade.
//
// The closure's return is wrapped in a struct {value, found} so that the
// outer code branches on the explicit `found` flag rather than the historical
// `fetched != nil` heuristic, which conflated a nil-valued override (found=true,
// value=nil) with "no override" (found=false). See AC3.
//
// The "Locked" suffix reflects that the method takes cacheMu.Lock internally
// around the LRU populate; it does not require the caller to hold any lock.
func (c *Client) getForTenantLazyMissLocked(ctx context.Context, tenantID, namespace, key string, nk nskey) (any, bool, bool) {
	sfKey := singleflightKey(tenantID, namespace, key)

	// Struct-wrap the single-flight payload so "nil-valued override" and
	// "no override" are unambiguously distinguishable. The previous
	// (fetched any, err error) shape collapsed both to (nil, nil) and
	// caused the AC3 nil-value bug.
	type sfResult struct {
		value any
		found bool
	}

	res, fetchErr, _ := c.sfg.Do(sfKey, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(ctx, tenantStoreTimeout)
		defer cancel()

		decoded, found, err := c.fetchTenantValue(fetchCtx, tenantID, namespace, key)
		if err != nil {
			return sfResult{}, err
		}

		if !found {
			return sfResult{found: false}, nil
		}

		// Populate the LRU under cacheMu.Lock before returning so every
		// shared-call waiter sees the same cached state.
		c.cacheMu.Lock()
		c.tenantCache.set(tenantID, nk, decoded)
		c.cacheMu.Unlock()

		return sfResult{value: decoded, found: true}, nil
	})
	if fetchErr != nil {
		c.logWarn(ctx, "lazy GetForTenant store fetch failed, falling through to global/default",
			fetchErrFields(namespace, key, tenantID, fetchErr)...,
		)
		// Attribute the fall-through for operators: a non-zero rate
		// here indicates the lazy cache is absorbing backend failures
		// behind the scenes. The metric is tenant-agnostic by design
		// (cardinality) — namespace+key alone is sufficient to locate
		// the affected key.
		c.recordTenantLazyFetchError(ctx, namespace, key)

		return nil, false, false
	}

	// sfg.Do returns `any`; the type assertion is safe because the closure
	// always returns an sfResult.
	r, _ := res.(sfResult)
	if !r.found {
		return nil, false, false
	}

	return r.value, true, true
}

// DeleteForTenant removes the tenant-specific override for (namespace, key)
// scoped to the tenant ID carried in ctx. The row is removed from the
// backing store; the in-memory tenantCache entry is cleared immediately for
// same-process read consistency.
//
// Subscribers are NOT fired from DeleteForTenant. The changefeed echo for
// the delete fires OnTenantChange with newValue = def.defaultValue — the
// tenant has reverted to global/default (TRD §4.4). Preserves the
// set.go:18-21 invariant: subscribers observe backend state changes only.
//
// Delete is idempotent at the backend: removing a non-existent override
// returns nil, not an error. This matches the store.Store contract
// documented at internal/store/store.go:82.
//
// If no override row exists for the tenant (idempotent delete), the
// underlying backend emits no change event and OnTenantChange does NOT
// fire. Callers waiting for the echo — e.g. tests that assert a callback
// runs after DeleteForTenant — must ensure a row exists first
// (SetForTenant) or they will wait forever.
//
// Returns the same error set as SetForTenant (except ErrValidation, which
// is not reachable on delete).
func (c *Client) DeleteForTenant(ctx context.Context, namespace, key, actor string) error {
	if c == nil || c.closed.Load() {
		return ErrClosed
	}

	if !c.started.Load() {
		return ErrNotStarted
	}

	tenantID, err := extractTenantID(ctx)
	if err != nil {
		return err
	}

	_, nk, err := c.requireTenantScoped(namespace, key)
	if err != nil {
		return err
	}

	if err := c.removeTenantValue(ctx, tenantID, namespace, key, actor); err != nil {
		return err
	}

	// Write-through cache delete: clear the override immediately so a
	// subsequent GetForTenant in the same process falls through to the
	// global/default cascade without waiting for the changefeed roundtrip.
	c.cacheMu.Lock()
	c.tenantCache.delete(tenantID, nk)
	c.cacheMu.Unlock()

	return nil
}

// emptyTenantList is the canonical empty slice returned by ListTenantsForKey
// on any error or unregistered-key path. Defined as a package-level var so
// every error branch shares the same zero-allocation sentinel instead of
// rebuilding a fresh []string{} on each call. Callers MUST NOT mutate the
// returned slice — it is structurally shared across every error response.
// Matches the "return the same zero-length slice" pattern used by a handful
// of stdlib helpers (e.g. strings.Split returning a 1-element slice of
// empty) to avoid unnecessary allocation on the cold path.
var emptyTenantList = []string{}

// ListTenantsForKey returns a sorted, deduplicated list of tenant IDs that
// have an override for (namespace, key). Returns an empty slice (never nil)
// on any error; errors are logged at warn level. Callers that need to
// distinguish "empty" from "errored" should use the admin surface, which
// returns explicit HTTP status codes.
//
// The '_global' sentinel is excluded by the backend; this method surfaces
// only actual tenant IDs. Unlike the other tenant methods, ListTenantsForKey
// does NOT require a tenant ID in ctx — it is an administrative/reflection
// query. An internal 5s timeout bounds the backend call.
func (c *Client) ListTenantsForKey(namespace, key string) []string {
	if c == nil || c.closed.Load() {
		return emptyTenantList
	}

	if _, _, err := c.requireTenantScoped(namespace, key); err != nil {
		c.logWarn(context.Background(), "ListTenantsForKey: registration check failed, returning empty slice",
			registrationErrFields(namespace, key, err)...,
		)

		return emptyTenantList
	}

	ctx, cancel := context.WithTimeout(context.Background(), tenantStoreTimeout)
	defer cancel()

	tenants, err := c.listTenantsForKey(ctx, namespace, key)
	if err != nil {
		c.logWarn(ctx, "ListTenantsForKey: backend query failed, returning empty slice",
			registrationErrFields(namespace, key, err)...,
		)

		return emptyTenantList
	}

	return tenants
}

// singleflightKey builds the composite string key used by the Client's
// singleflight.Group to coalesce concurrent lazy-mode miss fetches on the
// same (tenantID, namespace, key) tuple. The U+001F (Unit Separator)
// delimiter (see the `unitSeparator` constant in register.go) is a control
// character that cannot appear in valid tenantIDs, namespaces, or keys —
// the same scheme the changefeed debouncer used before it was moved to a
// struct key in onEvent.
//
// Safety depends on validateKeyArgs rejecting namespace/key that contain
// U+001F AND on core.IsValidTenantID rejecting tenant IDs that contain
// U+001F (it accepts only `[A-Za-z0-9][A-Za-z0-9_-]*`, so the delimiter is
// structurally impossible in a valid tenant ID). If either guard weakens,
// distinct tuples could collide on the same singleflight slot.
func singleflightKey(tenantID, namespace, key string) string {
	return tenantID + unitSeparator + namespace + unitSeparator + key
}

// fetchErrFields builds the log.Field slice for a lazy-mode cache-miss
// backend failure. Isolated in a helper so the call sites at
// GetForTenant stay concise.
func fetchErrFields(namespace, key, tenantID string, err error) []log.Field {
	return []log.Field{
		log.String("namespace", namespace),
		log.String("key", key),
		log.String("tenant_id", tenantID),
		log.Err(err),
	}
}

// registrationErrFields builds the log.Field slice for
// ListTenantsForKey's warn paths. The tenant ID is intentionally absent —
// ListTenantsForKey is tenant-agnostic (lists every tenant).
func registrationErrFields(namespace, key string, err error) []log.Field {
	return []log.Field{
		log.String("namespace", namespace),
		log.String("key", key),
		log.Err(err),
	}
}
