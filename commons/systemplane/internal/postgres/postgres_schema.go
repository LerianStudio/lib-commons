// Package postgres — schema bootstrap.
//
// This file carries ensureSchema, the constructor-time DDL routine that
// creates (or upgrades) the backing table, column, index, trigger function,
// and trigger. It is split out from postgres.go solely to keep each file
// below the 500-LOC guardrail. The logic belongs to the same Store type
// and is treated as private to the postgres package.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// ensureSchema creates the table, tenant-scoped column + index, and the
// NOTIFY trigger in a single transaction. All DDL is idempotent so the
// constructor can be called against a fresh database or an existing
// populated one (see TRD §3.1).
//
// # Two-phase migration
//
// The method operates in one of two phases depending on Config.TenantSchemaEnabled:
//
// Phase 1 (default, TenantSchemaEnabled=false) — rolling-deploy compat:
//
//  1. CREATE TABLE IF NOT EXISTS with legacy PRIMARY KEY (namespace, key).
//  2. ALTER TABLE ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT '_global'.
//  3. UPDATE ... SET tenant_id='_global' WHERE tenant_id IS NULL OR ”.
//  4. CREATE OR REPLACE FUNCTION systemplane_notify (emits tenant_id in payload;
//     under phase 1 the emitted tenant_id is always '_global' because no
//     tenant rows exist).
//  5. CREATE TRIGGER systemplane_notify_trigger AFTER INSERT OR UPDATE OR DELETE.
//
// The legacy (namespace, key) primary key is preserved so pre-tenant
// lib-commons binaries (v5.0.x) that upsert via ON CONFLICT (namespace, key)
// continue to work during a rolling deploy.
//
// Phase 2 (opt-in via WithTenantSchemaEnabled()):
//
//		All phase-1 steps, plus:
//
//	 6. Pre-flight duplicate-detection: if any (namespace, key) has BOTH a row
//	    with tenant_id IN (NULL, '') AND a sibling '_global' row, abort with a
//	    clear error. The backfill in step 3 would otherwise violate the
//	    composite unique and silently erase one of the rows.
//	 7. ALTER TABLE ... DROP CONSTRAINT IF EXISTS <table>_pkey (idempotent).
//	 8. CREATE UNIQUE INDEX IF NOT EXISTS <table>_pkey_v2 ON ... (namespace, key, tenant_id).
//
// Phase 2 is what SetTenantValue / DeleteTenantValue require; the store's
// tenant methods return ErrTenantSchemaNotEnabled while running in phase 1.
// Flip phase 2 only after every consumer binary has been upgraded to a
// lib-commons release that tolerates the new schema (v5.1+). See
// MIGRATION_TENANT_SCOPED.md §4 for the runbook.
func (s *Store) ensureSchema(ctx context.Context) error {
	// Phase 2 pre-flight runs OUTSIDE the main DDL transaction so the
	// detection query reads committed rows and so a validation failure
	// exits with a clean error rather than an aborted transaction message.
	if s.cfg.TenantSchemaEnabled {
		if err := s.verifyNoAmbiguousTenantRows(ctx); err != nil {
			return err
		}
	}

	tx, err := s.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	createTable := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		namespace   TEXT NOT NULL,
		key         TEXT NOT NULL,
		value       JSONB NOT NULL,
		updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_by  TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (namespace, key)
	)`, s.cfg.Table)

	if _, err := tx.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Add tenant_id column with sentinel default. Idempotent via IF NOT EXISTS.
	// Runs in both phases: phase 1 uses the column purely as a default-valued
	// marker so the NOTIFY payload can carry tenant_id; phase 2 uses it as
	// part of the composite unique.
	addTenantColumn := fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT '%s'`,
		s.cfg.Table, store.SentinelGlobal, // #nosec G201 -- table name validated; store.SentinelGlobal is a package constant
	)
	if _, err := tx.ExecContext(ctx, addTenantColumn); err != nil {
		return fmt.Errorf("add tenant column: %w", err)
	}

	// Idempotent backfill. A prior installation that added the column
	// without a default may have rows with NULL; enforce the sentinel.
	// Runs in both phases for the same reason the column add does.
	backfillTenant := fmt.Sprintf(
		`UPDATE %s SET tenant_id = '%s' WHERE tenant_id IS NULL OR tenant_id = ''`,
		s.cfg.Table, store.SentinelGlobal, // #nosec G201 -- table name validated; store.SentinelGlobal is a package constant
	)
	if _, err := tx.ExecContext(ctx, backfillTenant); err != nil {
		return fmt.Errorf("backfill tenant_id: %w", err)
	}

	if s.cfg.TenantSchemaEnabled {
		// Drop the old (namespace, key) primary key. Postgres names the
		// constraint "<table>_pkey" by default when the table was created
		// with PRIMARY KEY (...). DROP CONSTRAINT IF EXISTS keeps this
		// idempotent for fresh DBs where the constraint may have been
		// renamed, dropped, or already migrated to the v2 index.
		dropOldPK := fmt.Sprintf(
			`ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s_pkey`,
			s.cfg.Table, s.cfg.Table, // #nosec G201 -- table name validated as Postgres identifier in New()
		)
		if _, err := tx.ExecContext(ctx, dropOldPK); err != nil {
			return fmt.Errorf("drop old primary key: %w", err)
		}

		// Composite unique index that replaces the old PK. This is what
		// subsequent ON CONFLICT clauses target in phase 2.
		createNewUnique := fmt.Sprintf(
			`CREATE UNIQUE INDEX IF NOT EXISTS %s_pkey_v2 ON %s (namespace, key, tenant_id)`,
			s.cfg.Table, s.cfg.Table, // #nosec G201 -- table name validated as Postgres identifier in New()
		)
		if _, err := tx.ExecContext(ctx, createNewUnique); err != nil {
			return fmt.Errorf("create composite unique index: %w", err)
		}
	}

	// The trigger function references the channel name literally. We have
	// already validated it against safeIdentifierRe, so direct interpolation
	// is safe here. The function handles INSERT/UPDATE (NEW is populated)
	// and DELETE (OLD is populated, NEW is NULL) — branching on TG_OP keeps
	// a single function covering all three events. Installed in both phases
	// so the NOTIFY payload shape is consistent regardless of schema mode.
	createFunc := fmt.Sprintf(`CREATE OR REPLACE FUNCTION systemplane_notify() RETURNS TRIGGER AS $$
BEGIN
	IF TG_OP = 'DELETE' THEN
		PERFORM pg_notify('%[1]s', json_build_object(
			'namespace', OLD.namespace,
			'key', OLD.key,
			'tenant_id', OLD.tenant_id
		)::text);
		RETURN OLD;
	ELSE
		PERFORM pg_notify('%[1]s', json_build_object(
			'namespace', NEW.namespace,
			'key', NEW.key,
			'tenant_id', NEW.tenant_id
		)::text);
		RETURN NEW;
	END IF;
END;
$$ LANGUAGE plpgsql`, s.cfg.Channel)

	if _, err := tx.ExecContext(ctx, createFunc); err != nil {
		return fmt.Errorf("create function: %w", err)
	}

	dropTrigger := "DROP TRIGGER IF EXISTS systemplane_notify_trigger ON " + s.cfg.Table // #nosec G202 -- table name validated as Postgres identifier in New()
	if _, err := tx.ExecContext(ctx, dropTrigger); err != nil {
		return fmt.Errorf("drop trigger: %w", err)
	}

	createTrigger := fmt.Sprintf(`CREATE TRIGGER systemplane_notify_trigger
AFTER INSERT OR UPDATE OR DELETE ON %s
FOR EACH ROW EXECUTE FUNCTION systemplane_notify()`, s.cfg.Table) // #nosec G201 -- table name validated as Postgres identifier in New()

	if _, err := tx.ExecContext(ctx, createTrigger); err != nil {
		return fmt.Errorf("create trigger: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// verifyNoAmbiguousTenantRows is the phase-2 pre-flight guard (H8). If the
// table already contains rows with tenant_id IN (NULL, ”) AND a sibling
// '_global' row for the same (namespace, key), the subsequent backfill would
// collide with the composite unique index and silently erase one of the two
// rows. We fail loudly so operators consolidate the data before re-running
// ensureSchema with TenantSchemaEnabled=true.
//
// Skipped on fresh tables — the pg_class lookup and COUNT both fall through
// harmlessly when the table doesn't exist yet.
func (s *Store) verifyNoAmbiguousTenantRows(ctx context.Context) error {
	tableExists, err := s.tableExists(ctx)
	if err != nil {
		return fmt.Errorf("check table exists: %w", err)
	}

	if !tableExists {
		return nil
	}

	// If the tenant_id column hasn't been added yet (very first installation
	// or upgrade from a pre-column release), there is nothing to check.
	columnExists, err := s.tenantColumnExists(ctx)
	if err != nil {
		return fmt.Errorf("check tenant_id column exists: %w", err)
	}

	if !columnExists {
		return nil
	}

	query := fmt.Sprintf( // #nosec G201 -- table name validated as Postgres identifier in New()
		`SELECT COUNT(*) FROM %s a
		 WHERE (a.tenant_id IS NULL OR a.tenant_id = '')
		   AND EXISTS (
		     SELECT 1 FROM %s b
		     WHERE b.namespace = a.namespace
		       AND b.key = a.key
		       AND b.tenant_id = $1
		   )`,
		s.cfg.Table, s.cfg.Table,
	)

	var collisions int
	if err := s.cfg.DB.QueryRowContext(ctx, query, store.SentinelGlobal).Scan(&collisions); err != nil {
		// A sql.ErrNoRows here would be a driver bug on COUNT(*); treat it
		// defensively as zero rather than fail the migration.
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return fmt.Errorf("detect ambiguous rows: %w", err)
	}

	if collisions > 0 {
		return fmt.Errorf(
			"systemplane/postgres: ambiguous pre-migration state: multiple rows for (ns, key) with inconsistent tenant_id — consolidate before enabling phase 2 (%d affected)",
			collisions,
		)
	}

	return nil
}

// tableExists reports whether s.cfg.Table is present in the current search_path.
// Uses regclass casting to tolerate schema-qualified table names without
// explicit parsing; a missing table yields sql.ErrNoRows on the cast.
func (s *Store) tableExists(ctx context.Context) (bool, error) {
	var oid sql.NullInt64

	err := s.cfg.DB.QueryRowContext(ctx, `SELECT to_regclass($1)::oid`, s.cfg.Table).Scan(&oid)
	if err != nil {
		return false, fmt.Errorf("to_regclass: %w", err)
	}

	return oid.Valid && oid.Int64 > 0, nil
}

// tenantColumnExists reports whether the tenant_id column is already present
// on s.cfg.Table. Checks information_schema rather than a catalog trick so
// the query is robust across Postgres versions and extension-added columns.
func (s *Store) tenantColumnExists(ctx context.Context) (bool, error) {
	const query = `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name = $1 AND column_name = 'tenant_id'
	)`

	var exists bool
	if err := s.cfg.DB.QueryRowContext(ctx, query, s.cfg.Table).Scan(&exists); err != nil {
		return false, fmt.Errorf("information_schema.columns: %w", err)
	}

	return exists, nil
}
