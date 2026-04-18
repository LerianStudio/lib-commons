// Tenant-scoped registration and (in Task 5) read/write/delete/list/subscribe
// paths for the systemplane Client.
//
// This file currently ships only RegisterTenantScoped. The corresponding
// SetForTenant / GetForTenant / DeleteForTenant / ListTenantsForKey /
// OnTenantChange methods (plus the five typed accessor mirrors) land in Task 5
// alongside the changefeed routing that distinguishes global vs per-tenant
// events. See docs/pre-dev/tenant-scoped-systemplane/tasks.md §Task 5 and
// trd.md §2.1 for the full planned surface.
package systemplane

import "fmt"

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
