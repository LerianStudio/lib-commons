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
	"fmt"
)

// ensureSchema creates the table, tenant-scoped column + index, and the
// NOTIFY trigger in a single transaction. All DDL is idempotent so the
// constructor can be called against a fresh database or an existing
// populated one (see TRD §3.1). The four additive statements added in the
// tenant-scoping rollout are:
//
//  1. ALTER TABLE ... ADD COLUMN IF NOT EXISTS tenant_id ... DEFAULT '_global'
//  2. UPDATE ... SET tenant_id = '_global' WHERE tenant_id IS NULL OR empty
//     (defensive backfill; the NOT NULL DEFAULT above already covers every
//     newly-inserted row, but an earlier installation that lacked the column
//     may have rows that need to land on the sentinel explicitly).
//  3. ALTER TABLE ... DROP CONSTRAINT IF EXISTS <table>_pkey
//     (the pre-tenant schema made (namespace, key) the primary key; under
//     tenant scoping the composite becomes (namespace, key, tenant_id)).
//  4. CREATE UNIQUE INDEX IF NOT EXISTS <table>_pkey_v2 ON ... (ns, key, tenant_id)
//     (enforces uniqueness under the widened tuple; also serves as the
//     target for ON CONFLICT upserts in Set / SetTenantValue).
//
// The NOTIFY trigger function is rewritten to include tenant_id in the
// payload and to handle DELETE events — the function branches on TG_OP so
// a single definition serves INSERT/UPDATE (where NEW is populated) and
// DELETE (where OLD is populated and NEW is NULL). The trigger itself is
// extended from AFTER INSERT OR UPDATE to AFTER INSERT OR UPDATE OR DELETE.
func (s *Store) ensureSchema(ctx context.Context) error {
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

	// 1. Add tenant_id column with sentinel default. Idempotent via IF NOT EXISTS.
	addTenantColumn := fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT '%s'`,
		s.cfg.Table, sentinelGlobal, // #nosec G201 -- table name validated; sentinelGlobal is a package constant
	)
	if _, err := tx.ExecContext(ctx, addTenantColumn); err != nil {
		return fmt.Errorf("add tenant column: %w", err)
	}

	// 2. Idempotent backfill. A prior installation that added the column
	// without a default may have rows with NULL; enforce the sentinel.
	backfillTenant := fmt.Sprintf(
		`UPDATE %s SET tenant_id = '%s' WHERE tenant_id IS NULL OR tenant_id = ''`,
		s.cfg.Table, sentinelGlobal, // #nosec G201 -- table name validated; sentinelGlobal is a package constant
	)
	if _, err := tx.ExecContext(ctx, backfillTenant); err != nil {
		return fmt.Errorf("backfill tenant_id: %w", err)
	}

	// 3. Drop the old (namespace, key) primary key. Postgres names the
	// constraint "<table>_pkey" by default when the table was created with
	// PRIMARY KEY (...). Using DROP CONSTRAINT IF EXISTS keeps this
	// idempotent for fresh DBs where the constraint may have been renamed,
	// dropped in a previous run, or already migrated to the v2 index.
	dropOldPK := fmt.Sprintf(
		`ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s_pkey`,
		s.cfg.Table, s.cfg.Table, // #nosec G201 -- table name validated as Postgres identifier in New()
	)
	if _, err := tx.ExecContext(ctx, dropOldPK); err != nil {
		return fmt.Errorf("drop old primary key: %w", err)
	}

	// 4. Composite unique index that replaces the old PK. This is what
	// subsequent ON CONFLICT clauses target.
	createNewUnique := fmt.Sprintf(
		`CREATE UNIQUE INDEX IF NOT EXISTS %s_pkey_v2 ON %s (namespace, key, tenant_id)`,
		s.cfg.Table, s.cfg.Table, // #nosec G201 -- table name validated as Postgres identifier in New()
	)
	if _, err := tx.ExecContext(ctx, createNewUnique); err != nil {
		return fmt.Errorf("create composite unique index: %w", err)
	}

	// The trigger function references the channel name literally. We have
	// already validated it against safeIdentifierRe, so direct interpolation
	// is safe here. The function handles INSERT/UPDATE (NEW is populated)
	// and DELETE (OLD is populated, NEW is NULL) — branching on TG_OP keeps
	// a single function covering all three events.
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
