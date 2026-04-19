// Package-level option constructors for Client and key registration.
package systemplane

import (
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
)

// clientConfig holds the merged configuration applied by Option functions.
type clientConfig struct {
	logger        log.Logger
	telemetry     *opentelemetry.Telemetry
	listenChannel string        // Postgres LISTEN channel name
	pollInterval  time.Duration // MongoDB polling interval (zero = change streams)
	debounce      time.Duration
	collection    string // MongoDB collection name
	table         string // Postgres table name

	// listenChannelExplicit records whether WithListenChannel was called.
	// postgres.New uses this to suppress the default-channel collision
	// warning when the caller explicitly chose the default name — the
	// warning is about accidental default usage, not deliberate opt-in.
	listenChannelExplicit bool

	// tenantLoadMode selects eager (default) vs lazy tenant value hydration.
	// tenantCacheMax is the LRU bound in lazy mode; ignored in eager mode.
	tenantLoadMode tenantLoadMode
	tenantCacheMax int

	// tenantSchemaEnabled selects the backend schema phase (default: phase 1
	// compat). When false, the backend preserves the legacy unique constraint
	// on (namespace, key) so pre-tenant binaries (v5.0.x) can upsert during a
	// rolling deploy; tenant writes return ErrTenantSchemaNotEnabled. When
	// true, the backend drops the legacy constraint and creates the composite
	// unique on (namespace, key, tenant_id), enabling tenant writes. Flip this
	// only after every consumer process is on v5.1+.
	tenantSchemaEnabled bool
}

// defaultClientConfig returns sensible defaults.
func defaultClientConfig() clientConfig {
	return clientConfig{
		listenChannel: "systemplane_changes",
		debounce:      100 * time.Millisecond,
		collection:    "systemplane_entries",
		table:         "systemplane_entries",
		// Explicit — relying on the zero value of tenantLoadMode would silently
		// break if tenantLoadEager were ever renumbered (it currently sits at
		// iota == 0). State the default here so the intent is locked to the
		// name, not the ordering.
		tenantLoadMode: tenantLoadEager,
	}
}

// Option configures a Client at construction time.
type Option func(*clientConfig)

// WithLogger sets the structured logger used by the Client and its backend.
func WithLogger(l log.Logger) Option {
	return func(cfg *clientConfig) {
		if l != nil {
			cfg.logger = l
		}
	}
}

// WithTelemetry sets the OpenTelemetry provider for spans and metrics.
func WithTelemetry(t *opentelemetry.Telemetry) Option {
	return func(cfg *clientConfig) {
		if t != nil {
			cfg.telemetry = t
		}
	}
}

// WithListenChannel overrides the Postgres LISTEN/NOTIFY channel name.
// Default: "systemplane_changes". Ignored by MongoDB backends.
//
// Calling this option suppresses the default-channel collision warning,
// even when the caller passes the default name — the warning exists to
// catch silent default usage, not deliberate opt-in.
func WithListenChannel(name string) Option {
	return func(cfg *clientConfig) {
		if name != "" {
			cfg.listenChannel = name
			cfg.listenChannelExplicit = true
		}
	}
}

// WithPollInterval enables polling mode for MongoDB instead of change streams.
// A zero or negative value keeps the default change-stream mode.
// Ignored by Postgres backends.
func WithPollInterval(d time.Duration) Option {
	return func(cfg *clientConfig) {
		if d > 0 {
			cfg.pollInterval = d
		}
	}
}

// WithDebounce sets the trailing-edge debounce window for change notifications.
// Default: 100ms. A zero or negative value disables debouncing.
func WithDebounce(d time.Duration) Option {
	return func(cfg *clientConfig) {
		cfg.debounce = d
	}
}

// WithCollection overrides the MongoDB collection name.
// Default: "systemplane_entries". Ignored by Postgres backends.
func WithCollection(name string) Option {
	return func(cfg *clientConfig) {
		if name != "" {
			cfg.collection = name
		}
	}
}

// WithTable overrides the Postgres table name.
// Default: "systemplane_entries". Ignored by MongoDB backends.
func WithTable(name string) Option {
	return func(cfg *clientConfig) {
		if name != "" {
			cfg.table = name
		}
	}
}

// WithLazyTenantLoad switches tenant value caching from eager hydration (the
// default, which loads every tenant row at Start) to a lazy bounded-LRU cache
// populated on first read.
//
// In lazy mode, GetForTenant issues a single-flight store.GetTenantValue call
// on a cache miss (with a 5s timeout) and evicts the least-recently-used entry
// when the cache reaches max entries. The LRU is backed by
// github.com/hashicorp/golang-lru/v2, which maintains atomic MRU ordering under
// its own internal RWMutex — concurrent cache hits run under the outer
// cacheMu.RLock without serializing on a write lock. The trade-off versus
// eager mode is a ~5-10ms first-touch cost per (tenant, key) tuple in exchange
// for bounded memory. Best fit for deployments with a large tenant population
// where only a subset is routinely active (e.g. >10k tenants × 12 keys with
// ~100 active tenants at a time).
//
// A non-positive max is treated as "disabled" and falls back to eager mode,
// matching the convention used by WithDebounce and WithPollInterval.
func WithLazyTenantLoad(maxEntries int) Option {
	return func(cfg *clientConfig) {
		if maxEntries <= 0 {
			// Treat as disabled: eager mode remains active.
			return
		}

		cfg.tenantLoadMode = tenantLoadLazy
		cfg.tenantCacheMax = maxEntries
	}
}

// WithTenantSchemaEnabled opts the backend into phase-2 schema: the legacy
// unique constraint on (namespace, key) is dropped and replaced by a composite
// unique on (namespace, key, tenant_id). Required before [SetForTenant],
// [DeleteForTenant], and [RegisterTenantScoped]'s write paths can be used;
// without it, those methods return [ErrTenantSchemaNotEnabled].
//
// # Rolling-deploy safety
//
// The default (phase 1) keeps the legacy constraint alive so pre-tenant
// binaries (lib-commons v5.0.x) can continue to upsert via
// ON CONFLICT (namespace, key) during a rolling deploy. Flip this option
// only AFTER every consumer process in the same database is running
// lib-commons v5.1+ — otherwise legacy binaries will fail with
// "no unique or exclusion constraint matching the ON CONFLICT specification"
// the first time they attempt an upsert.
//
// See commons/systemplane/MIGRATION_TENANT_SCOPED.md §4 for the full runbook.
func WithTenantSchemaEnabled() Option {
	return func(cfg *clientConfig) {
		cfg.tenantSchemaEnabled = true
	}
}

// KeyOption configures a single key at registration time.
type KeyOption func(*keyDef)

// WithDescription sets a human-readable description for the key,
// surfaced in admin endpoints.
func WithDescription(s string) KeyOption {
	return func(k *keyDef) {
		k.description = s
	}
}

// WithValidator sets a validation function invoked on every Set.
// The function receives the proposed value and should return a non-nil
// error to reject it.
func WithValidator(fn func(any) error) KeyOption {
	return func(k *keyDef) {
		if fn != nil {
			k.validator = fn
		}
	}
}

// WithRedaction sets the redaction policy for admin and log output.
// Default: RedactNone.
func WithRedaction(policy RedactPolicy) KeyOption {
	return func(k *keyDef) {
		k.redaction = policy
	}
}
