// Package store defines the internal storage abstraction for systemplane backends.
//
// It is the only interface kept between the public Client and the concrete
// Postgres / MongoDB implementations. The interface is intentionally small so
// that each backend can be implemented in ~300-350 LOC.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNilBackend is returned when a Store constructor receives a nil database handle.
var ErrNilBackend = errors.New("systemplane/store: nil backend handle")

// SentinelGlobal is the tenant_id value carried in Entry and Event structures
// for shared (non-tenant-scoped) rows. It is the single source of truth shared
// by every backend (internal/postgres, internal/mongodb), the public Client,
// and the contract test suite — a rename here is the only rename needed to
// move the sentinel across the codebase.
//
// The leading underscore is deliberate: the tenant-manager validation regex
// ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ (commons/tenant-manager/core/validation.go)
// rejects identifiers that start with "_", so no real tenant ID can collide
// with the sentinel. See TRD §3.1 decision D2.
const SentinelGlobal = "_global"

// ErrClosed is returned when a method is called on a nil or closed Store.
var ErrClosed = errors.New("systemplane/store: store is closed or nil")

// ErrTenantSchemaNotEnabled is returned by SetTenantValue / DeleteTenantValue
// when the backend is running in phase-1 compat mode. In phase 1 the legacy
// unique constraint on (namespace, key) is preserved so pre-tenant binaries
// (v5.0.x) can continue to upsert without hitting ON CONFLICT arbiter
// mismatches. Tenant writes are therefore rejected at the store boundary
// with a clear error rather than a cryptic DB-level unique violation.
// See commons/systemplane/MIGRATION_TENANT_SCOPED.md §4 for the upgrade path.
var ErrTenantSchemaNotEnabled = errors.New("systemplane/store: tenant schema not enabled (phase 1 compat); enable via WithTenantSchemaEnabled() once all consumers are upgraded")

// Entry is the persisted shape of a single configuration key.
//
// TenantID carries the tenant scope for the row. The sentinel value "_global"
// identifies the shared (non-tenant-scoped) row; any other value is a tenant
// override. Backends populate TenantID on reads and honour it on writes.
type Entry struct {
	Namespace string
	Key       string
	TenantID  string
	Value     []byte // JSON-encoded
	UpdatedAt time.Time
	UpdatedBy string
}

// Event is what a changefeed delivers when a key is modified.
//
// TenantID identifies which tenant's row changed. The sentinel "_global"
// indicates a change to the shared row; any other value indicates a
// tenant-scoped override.
type Event struct {
	Namespace string
	Key       string
	TenantID  string
}

// Store is implemented by internal/postgres and internal/mongodb.
type Store interface {
	// List returns all entries. Used to hydrate the Client at Start().
	List(ctx context.Context) ([]Entry, error)

	// Get returns a single entry by namespace and key.
	Get(ctx context.Context, namespace, key string) (Entry, bool, error)

	// Set persists an entry using last-write-wins semantics.
	// The backend is responsible for notifying subscribers via its changefeed.
	Set(ctx context.Context, e Entry) error

	// Subscribe blocks until ctx is cancelled, invoking handler for each
	// change event received from the backend's changefeed mechanism.
	Subscribe(ctx context.Context, handler func(Event)) error

	// Close releases backend resources. Idempotent.
	Close() error

	// GetTenantValue returns the tenant-specific override for (namespace, key)
	// scoped to tenantID. Returns (Entry{}, false, nil) when no override exists
	// for this tenant — callers are expected to fall back to the "_global" row.
	// The returned Entry has its TenantID populated with the requested tenantID
	// when found = true.
	GetTenantValue(ctx context.Context, tenantID, namespace, key string) (Entry, bool, error)

	// SetTenantValue persists an entry under the given tenantID using
	// last-write-wins semantics. The backend writes tenantID into the row's
	// tenant_id column/field and emits a changefeed event whose Event.TenantID
	// equals tenantID. The Entry.TenantID field is ignored; tenantID is
	// authoritative.
	SetTenantValue(ctx context.Context, tenantID string, e Entry) error

	// DeleteTenantValue removes a single tenant's override row for
	// (namespace, key). actor is recorded for audit. The backend emits a
	// changefeed event for the deletion so subscribers can drop the cached
	// override and re-derive the effective value from the "_global" row.
	// Returns nil when no matching row exists (delete is idempotent).
	DeleteTenantValue(ctx context.Context, tenantID, namespace, key, actor string) error

	// ListTenantValues returns every row in the store, including both the
	// "_global" rows and every tenant-scoped override. Callers filter by
	// TenantID as needed; used by the Client to hydrate the tenantCache at
	// Start(). Returns an empty slice, not nil, when the store is empty.
	ListTenantValues(ctx context.Context) ([]Entry, error)

	// ListTenantsForKey returns a sorted, deduplicated list of distinct
	// tenant IDs that have an override for (namespace, key). The "_global"
	// sentinel is excluded from the result. Returns an empty slice, not nil,
	// when no tenant has overridden the key.
	ListTenantsForKey(ctx context.Context, namespace, key string) ([]string, error)
}
