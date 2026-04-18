// Package postgres — tenant-scoped Store method implementations.
//
// This file implements the five tenant methods added to the store.Store
// interface in the tenant-scoping rollout (TRD §2.3): GetTenantValue,
// SetTenantValue, DeleteTenantValue, ListTenantValues, and
// ListTenantsForKey. The lifecycle helpers (schema bootstrap, LISTEN loop,
// close) and the global-path methods (Get, Set, List) live alongside in
// postgres.go. Splitting tenant methods out keeps each file below the
// 500-LOC guardrail without introducing an artificial package boundary.
//
// All tenant methods mirror the span / error-wrap / nil-receiver pattern
// established by the global methods in postgres.go. The tenant_id value
// comes from the tenantID argument (never from Entry.TenantID) so the
// caller cannot accidentally write to the wrong tenant by forgetting to
// set the field on the Entry struct — this matches the store.Store
// interface contract documented in internal/store/store.go.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// GetTenantValue returns the tenant-specific override for (namespace, key)
// scoped to tenantID. Returns (Entry{}, false, nil) when no override exists
// for this tenant — the caller is expected to fall back to the global row.
// The returned Entry.TenantID equals tenantID when found is true.
func (s *Store) GetTenantValue(ctx context.Context, tenantID, namespace, key string) (store.Entry, bool, error) {
	if s == nil || s.isClosed() {
		return store.Entry{}, false, store.ErrClosed
	}

	ctx, span, finish := s.startSpan(ctx, "systemplane.postgres.get_tenant_value",
		attribute.String("tenant_id", tenantID),
		attribute.String("namespace", namespace),
		attribute.String("key", key),
	)
	defer finish()

	query := fmt.Sprintf( // #nosec G201 -- table name validated as Postgres identifier in New()
		`SELECT namespace, key, tenant_id, value, updated_at, updated_by FROM %s WHERE namespace = $1 AND key = $2 AND tenant_id = $3`,
		s.cfg.Table,
	)

	var e store.Entry

	err := s.cfg.DB.QueryRowContext(ctx, query, namespace, key, tenantID).
		Scan(&e.Namespace, &e.Key, &e.TenantID, &e.Value, &e.UpdatedAt, &e.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Entry{}, false, nil
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")

		return store.Entry{}, false, fmt.Errorf("systemplane/postgres: get_tenant_value: %w", err)
	}

	return e, true, nil
}

// SetTenantValue persists an entry under the given tenantID using an
// INSERT ... ON CONFLICT UPDATE against the composite unique index on
// (namespace, key, tenant_id). The Entry.TenantID field is ignored;
// tenantID is authoritative — this matches the store.Store interface
// contract. The backing trigger emits NOTIFY with tenant_id in the
// payload so subscribers can route to OnTenantChange.
func (s *Store) SetTenantValue(ctx context.Context, tenantID string, e store.Entry) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	if !s.cfg.TenantSchemaEnabled {
		return store.ErrTenantSchemaNotEnabled
	}

	if tenantID == "" {
		return errors.New("systemplane/postgres: tenantID must not be empty")
	}

	if tenantID == sentinelGlobal {
		return errors.New("systemplane/postgres: tenantID must not be the '_global' sentinel")
	}

	if e.Namespace == "" {
		return errors.New("systemplane/postgres: namespace must not be empty")
	}

	if e.Key == "" {
		return errors.New("systemplane/postgres: key must not be empty")
	}

	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = time.Now().UTC()
	}

	ctx, span, finish := s.startSpan(ctx, "systemplane.postgres.set_tenant_value",
		attribute.String("tenant_id", tenantID),
		attribute.String("namespace", e.Namespace),
		attribute.String("key", e.Key),
	)
	defer finish()

	query := fmt.Sprintf(`INSERT INTO %s (namespace, key, tenant_id, value, updated_at, updated_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (namespace, key, tenant_id) DO UPDATE
SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
		s.cfg.Table, // #nosec G201 -- table name validated as Postgres identifier in New()
	)

	if _, err := s.cfg.DB.ExecContext(ctx, query, e.Namespace, e.Key, tenantID, e.Value, e.UpdatedAt, e.UpdatedBy); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "upsert failed")

		return fmt.Errorf("systemplane/postgres: set_tenant_value: %w", err)
	}

	return nil
}

// DeleteTenantValue removes a single tenant's override row for
// (namespace, key). actor is recorded as a span attribute for audit
// purposes but is not written to the database — this matches existing
// Set behavior (no audit row written; the trigger's NOTIFY payload is
// the source of truth for change detection). The Postgres trigger is
// wired to AFTER INSERT OR UPDATE OR DELETE, so even a no-op delete
// does not fire NOTIFY because the trigger fires per-row and there is
// no row to emit. Consumers relying on DeleteForTenant to echo a
// changefeed event must ensure the row exists first. Returns nil when
// no matching row exists (delete is idempotent by sql.DELETE semantics).
func (s *Store) DeleteTenantValue(ctx context.Context, tenantID, namespace, key, actor string) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	if !s.cfg.TenantSchemaEnabled {
		return store.ErrTenantSchemaNotEnabled
	}

	if tenantID == "" {
		return errors.New("systemplane/postgres: tenantID must not be empty")
	}

	if tenantID == sentinelGlobal {
		return errors.New("systemplane/postgres: tenantID must not be the '_global' sentinel")
	}

	if namespace == "" {
		return errors.New("systemplane/postgres: namespace must not be empty")
	}

	if key == "" {
		return errors.New("systemplane/postgres: key must not be empty")
	}

	ctx, span, finish := s.startSpan(ctx, "systemplane.postgres.delete_tenant_value",
		attribute.String("tenant_id", tenantID),
		attribute.String("namespace", namespace),
		attribute.String("key", key),
		attribute.String("actor", actor),
	)
	defer finish()

	query := fmt.Sprintf( // #nosec G201 -- table name validated as Postgres identifier in New()
		`DELETE FROM %s WHERE namespace = $1 AND key = $2 AND tenant_id = $3`,
		s.cfg.Table,
	)

	if _, err := s.cfg.DB.ExecContext(ctx, query, namespace, key, tenantID); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "delete failed")

		return fmt.Errorf("systemplane/postgres: delete_tenant_value: %w", err)
	}

	return nil
}

// ListTenantValues returns every row in the table, including both the
// '_global' rows and every tenant-scoped override. Ordering is
// deterministic (namespace, key, tenant_id) so hydration iteration is
// repeatable across restarts. Callers filter by Entry.TenantID to decide
// whether a row populates the global cache or the per-tenant cache — the
// Client at Start() uses this to hydrate tenantCache in eager mode
// (TRD §4.5).
func (s *Store) ListTenantValues(ctx context.Context) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span, finish := s.startSpan(ctx, "systemplane.postgres.list_tenant_values")
	defer finish()

	query := fmt.Sprintf( // #nosec G201 -- table name validated as Postgres identifier in New()
		`SELECT namespace, key, tenant_id, value, updated_at, updated_by FROM %s ORDER BY namespace, key, tenant_id`,
		s.cfg.Table,
	)

	rows, err := s.cfg.DB.QueryContext(ctx, query)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")

		return nil, fmt.Errorf("systemplane/postgres: list_tenant_values: %w", err)
	}
	defer rows.Close()

	var entries []store.Entry

	for rows.Next() {
		var e store.Entry

		if err := rows.Scan(&e.Namespace, &e.Key, &e.TenantID, &e.Value, &e.UpdatedAt, &e.UpdatedBy); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "scan failed")

			return nil, fmt.Errorf("systemplane/postgres: list_tenant_values scan: %w", err)
		}

		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "rows iteration failed")

		return nil, fmt.Errorf("systemplane/postgres: list_tenant_values rows: %w", err)
	}

	// Return empty slice, not nil.
	if entries == nil {
		entries = []store.Entry{}
	}

	return entries, nil
}

// ListTenantsForKey returns a sorted, deduplicated list of distinct
// tenant IDs that have an override for (namespace, key). The '_global'
// sentinel is excluded because it represents "no tenant override", not a
// real tenant. Returns an empty slice (not nil) when no tenant has
// overridden the key.
func (s *Store) ListTenantsForKey(ctx context.Context, namespace, key string) ([]string, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span, finish := s.startSpan(ctx, "systemplane.postgres.list_tenants_for_key",
		attribute.String("namespace", namespace),
		attribute.String("key", key),
	)
	defer finish()

	query := fmt.Sprintf( // #nosec G201 -- table name validated as Postgres identifier in New()
		`SELECT DISTINCT tenant_id FROM %s WHERE namespace = $1 AND key = $2 AND tenant_id <> $3 ORDER BY tenant_id`,
		s.cfg.Table,
	)

	rows, err := s.cfg.DB.QueryContext(ctx, query, namespace, key, sentinelGlobal)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")

		return nil, fmt.Errorf("systemplane/postgres: list_tenants_for_key: %w", err)
	}
	defer rows.Close()

	var tenants []string

	for rows.Next() {
		var tenantID string

		if err := rows.Scan(&tenantID); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "scan failed")

			return nil, fmt.Errorf("systemplane/postgres: list_tenants_for_key scan: %w", err)
		}

		tenants = append(tenants, tenantID)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "rows iteration failed")

		return nil, fmt.Errorf("systemplane/postgres: list_tenants_for_key rows: %w", err)
	}

	// Return empty slice, not nil.
	if tenants == nil {
		tenants = []string{}
	}

	return tenants, nil
}
