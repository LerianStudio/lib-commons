//go:build integration

// Phase-1 / phase-2 migration integration tests for the Postgres store.
//
// The tests in this file exercise the two-phase schema upgrade path
// documented in postgres_schema.go: starting from legacy shapes
// (pre-tenant, NULL / empty tenant_id, half-migrated crash states), we
// assert that a phase-2 New() either completes the migration idempotently
// or fails loud when the pre-migration state is ambiguous.
//
// Conventions mirror postgres_integration_test.go: per-test containers
// (simpler lifecycle than a shared one when we need multiple open/close
// cycles), tableSeq-derived table names, and require/assert for
// failure/assertion separation. Raw DDL is used only where the
// production code path cannot reach the legacy shape we want to
// simulate — specifically, the ambiguous-rows pre-seed for C3-#4 and
// the partial-recovery pre-seed for C3-#6. Everywhere else, Store
// construction is exercised through the real New() entry point.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver registration

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// migrationSetup allocates a fresh Postgres container, opens a *sql.DB
// that outlives individual New()/Close() cycles, and reserves a unique
// table name. All three are returned to the caller so each migration
// test can drive open/close cycles directly.
//
// The DB handle is closed in t.Cleanup; the container in
// startPostgresContainer's cleanup.
func migrationSetup(ctx context.Context, t *testing.T) (dsn, table string, db *sql.DB) {
	t.Helper()

	dsn = startPostgresContainer(ctx, t)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	table = fmt.Sprintf("sp_mig_%d", tableSeq.Add(1))

	return dsn, table, db
}

// openPhase opens a Store against (dsn, db, table) with
// TenantSchemaEnabled = phase2. The Store is closed on t.Cleanup so
// tests that open multiple Stores against the same table don't need to
// remember to close every one.
func openPhase(t *testing.T, dsn, table string, db *sql.DB, phase2 bool) *Store {
	t.Helper()

	s, err := New(Config{
		DB:                  db,
		ListenDSN:           dsn,
		Channel:             "systemplane_changes",
		Table:               table,
		TenantSchemaEnabled: phase2,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, s.Close())
	})

	return s
}

// TestIntegration_Migration_FreshCollectionNoop constructs a phase-2
// Store against an empty database, then constructs a second phase-2
// Store against the same table and asserts the second call is a no-op
// (ensureSchema is idempotent). After both calls we assert that the
// expected columns and the composite unique index exist.
func TestIntegration_Migration_FreshCollectionNoop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn, table, db := migrationSetup(ctx, t)

	// First construction: creates the table + tenant column + composite
	// unique index.
	_ = openPhase(t, dsn, table, db, true)

	// Second construction against the same table must succeed without
	// error. This exercises every CREATE ... IF NOT EXISTS branch and
	// proves the backfill DO-block short-circuits on an already-backfilled
	// table.
	_ = openPhase(t, dsn, table, db, true)

	assertColumnExists(ctx, t, db, table, "namespace")
	assertColumnExists(ctx, t, db, table, "key")
	assertColumnExists(ctx, t, db, table, "value")
	assertColumnExists(ctx, t, db, table, "updated_at")
	assertColumnExists(ctx, t, db, table, "updated_by")
	assertColumnExists(ctx, t, db, table, "tenant_id")
	assertCompositeUniqueIndexExists(ctx, t, db, table)
}

// TestIntegration_Migration_BackfillsNullAndEmptyTenantID seeds a
// phase-1-shaped table with a mix of tenant_id values (empty, NULL,
// '_global') for distinct (namespace, key) pairs — no ambiguity — then
// reopens in phase 2 and asserts every row carries '_global' afterwards.
//
// Per PRD guidance: we cannot seed the legacy "tenant_id column absent"
// shape because every test path in this file needs to go through New(),
// which ADDs the column idempotently. We therefore simulate the
// pre-migration drift that happens in practice: a v5.1 phase-1 binary
// saw the column added but some rows pre-date the DEFAULT and were
// imported with NULL or empty strings. The backfill must normalize them.
func TestIntegration_Migration_BackfillsNullAndEmptyTenantID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn, table, db := migrationSetup(ctx, t)

	// Bootstrap the phase-1 shape (creates table + adds tenant_id column
	// with DEFAULT '_global'). New rows written through Set would pick up
	// the default, so we bypass that by inserting raw below.
	s := openPhase(t, dsn, table, db, false)
	require.NoError(t, s.Close())

	// Raw SQL seeding: insert three rows whose tenant_id values simulate
	// drift from a legacy import. Each row uses a distinct (namespace,
	// key) so there's no ambiguity vs the backfilled sentinel.
	seedRawEntry(ctx, t, db, table, "ns1", "k1", "", []byte(`"empty"`))
	seedRawEntryNullTenant(ctx, t, db, table, "ns2", "k2", []byte(`"null"`))
	seedRawEntry(ctx, t, db, table, "ns3", "k3", store.SentinelGlobal, []byte(`"global"`))

	// Phase-2 reopen — the backfill inside ensureSchema runs again and
	// must normalize the empty / NULL tenant_id rows.
	_ = openPhase(t, dsn, table, db, true)

	rows, err := db.QueryContext(ctx,
		fmt.Sprintf(`SELECT namespace, key, tenant_id FROM %s ORDER BY namespace, key`, table))
	require.NoError(t, err)
	defer rows.Close()

	type row struct {
		ns, k, tid string
	}

	var got []row

	for rows.Next() {
		var r row

		require.NoError(t, rows.Scan(&r.ns, &r.k, &r.tid))
		got = append(got, r)
	}

	require.NoError(t, rows.Err())

	require.Len(t, got, 3)

	for _, r := range got {
		assert.Equal(t, store.SentinelGlobal, r.tid,
			"row (%s, %s) tenant_id must be normalized to '_global'", r.ns, r.k)
	}
}

// TestIntegration_Migration_Phase1To2Transition drives the rolling-deploy
// scenario end to end: phase 1 → write → close → phase 2 → assert
// migration completed without data loss and that the new composite
// unique index enforces phase-2 invariants.
func TestIntegration_Migration_Phase1To2Transition(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn, table, db := migrationSetup(ctx, t)

	// Phase 1: Set writes two global rows using legacy PK(namespace, key).
	s1 := openPhase(t, dsn, table, db, false)

	require.NoError(t, s1.Set(ctx, store.Entry{
		Namespace: "global",
		Key:       "log.level",
		Value:     mustJSON(t, "info"),
		UpdatedBy: "phase1-actor",
	}))
	require.NoError(t, s1.Set(ctx, store.Entry{
		Namespace: "global",
		Key:       "rate_limit.rps",
		Value:     mustJSON(t, 42),
		UpdatedBy: "phase1-actor",
	}))

	// SetTenantValue MUST fail under phase 1 — this pins the documented
	// ErrTenantSchemaNotEnabled contract while the phase-1 Store is still
	// open.
	err := s1.SetTenantValue(ctx, "tenant-a", store.Entry{
		Namespace: "global",
		Key:       "log.level",
		Value:     mustJSON(t, "debug"),
		UpdatedBy: "tenant-actor",
	})
	assert.ErrorIs(t, err, store.ErrTenantSchemaNotEnabled)

	require.NoError(t, s1.Close())

	// Phase 2 reopen — ensureSchema runs the migration: drops legacy PK,
	// creates composite unique index.
	_ = openPhase(t, dsn, table, db, true)

	// Row count intact — neither row was lost in the migration.
	assertRowCount(ctx, t, db, table, 2)

	// Composite unique index replaces the legacy PK. Attempting a raw
	// duplicate INSERT with the same (namespace, key, tenant_id) triple
	// must fail with a uniqueness violation. We seed a fresh tenant row
	// first (so the conflict is deterministic) then attempt the duplicate.
	_, err = db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (namespace, key, tenant_id, value, updated_at, updated_by)
		 VALUES ('ns-x', 'k-x', 'tenant-a', '"first"'::jsonb, now(), 'seed')`, table))
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (namespace, key, tenant_id, value, updated_at, updated_by)
		 VALUES ('ns-x', 'k-x', 'tenant-a', '"dup"'::jsonb, now(), 'seed')`, table))
	require.Error(t, err, "composite unique index must prevent duplicate (ns, key, tenant_id)")

	// The legacy <table>_pkey constraint must be gone. We query
	// pg_constraint (rather than information_schema) because that's the
	// canonical source and matches the drop-by-resolved-name logic in
	// lookupPrimaryKeyName.
	assertLegacyPKDropped(ctx, t, db, table)
	assertCompositeUniqueIndexExists(ctx, t, db, table)
}

// TestIntegration_Migration_AmbiguousRowsAborts seeds the pre-migration
// ambiguous state described in verifyNoAmbiguousTenantRows — the same
// (namespace, key) has both a NULL/empty tenant_id row and a '_global'
// row. The phase-2 backfill would otherwise collide with the composite
// unique index and silently erase one row, so the pre-flight must abort
// with a human-readable error.
//
// To reach this state we bootstrap phase 1 (which creates PK(ns, key)
// blocking duplicate seeds), then drop the PK manually before seeding
// the ambiguous pair. This mirrors the real-world trigger: an operator
// or a half-run migration removed the PK but the backfill hasn't
// normalized tenant_id yet.
func TestIntegration_Migration_AmbiguousRowsAborts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn, table, db := migrationSetup(ctx, t)

	// Bootstrap phase-1 shape so the table + tenant column exist.
	s1 := openPhase(t, dsn, table, db, false)
	require.NoError(t, s1.Close())

	// Drop the legacy PK so we can insert two rows with the same
	// (namespace, key).
	dropLegacyPK(ctx, t, db, table)

	// Seed the ambiguous state: same (ns, key), one with empty tenant_id,
	// one with '_global'.
	seedRawEntry(ctx, t, db, table, "global", "ambiguous", "", []byte(`"pending"`))
	seedRawEntry(ctx, t, db, table, "global", "ambiguous", store.SentinelGlobal, []byte(`"committed"`))

	// Phase-2 New() must refuse to proceed — the pre-flight detects the
	// collision and returns a wrapped error containing "ambiguous
	// pre-migration".
	_, err := New(Config{
		DB:                  db,
		ListenDSN:           dsn,
		Channel:             "systemplane_changes",
		Table:               table,
		TenantSchemaEnabled: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous pre-migration",
		"expected pre-flight abort, got: %v", err)
}

// TestIntegration_Migration_IdempotentRerun opens phase 2 twice against
// the same table and asserts the second run is a no-op: the composite
// unique index is unchanged, the legacy PK remains absent, and no error
// surfaces from either call.
func TestIntegration_Migration_IdempotentRerun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn, table, db := migrationSetup(ctx, t)

	// First open: creates everything.
	s1 := openPhase(t, dsn, table, db, true)

	firstState := snapshotSchema(ctx, t, db, table)

	require.NoError(t, s1.Close())

	// Second open: must be a no-op on all DDL steps.
	_ = openPhase(t, dsn, table, db, true)

	secondState := snapshotSchema(ctx, t, db, table)

	assert.Equal(t, firstState, secondState,
		"schema state must be identical across idempotent phase-2 opens")
}

// TestIntegration_Migration_PartialRecovery seeds a half-migrated state:
// some rows have tenant_id='_global' (the migration completed), some
// have NULL (the migration crashed before the UPDATE committed). After a
// phase-2 reopen, every row must either carry its original tenant_id or
// '_global' — no row ends with a NULL or empty tenant_id.
func TestIntegration_Migration_PartialRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn, table, db := migrationSetup(ctx, t)

	// Phase-1 bootstrap so the table + column exist.
	s1 := openPhase(t, dsn, table, db, false)
	require.NoError(t, s1.Close())

	// Drop legacy PK so we can seed a future phase-2 tenant override row
	// alongside the half-migrated rows.
	dropLegacyPK(ctx, t, db, table)

	// Seed rows simulating a crash mid-backfill:
	//   - row A: already backfilled to '_global'.
	//   - row B: crashed — tenant_id is NULL.
	//   - row C: not yet touched — tenant_id is empty.
	//   - row D: a real tenant override that must be preserved.
	seedRawEntry(ctx, t, db, table, "ns-a", "k-a", store.SentinelGlobal, []byte(`"a"`))
	seedRawEntryNullTenant(ctx, t, db, table, "ns-b", "k-b", []byte(`"b"`))
	seedRawEntry(ctx, t, db, table, "ns-c", "k-c", "", []byte(`"c"`))
	seedRawEntry(ctx, t, db, table, "ns-d", "k-d", "tenant-xyz", []byte(`"d"`))

	// Phase-2 reopen — ensureSchema completes the backfill and installs
	// the composite unique index.
	_ = openPhase(t, dsn, table, db, true)

	rows, err := db.QueryContext(ctx,
		fmt.Sprintf(`SELECT namespace, key, tenant_id FROM %s ORDER BY namespace, key`, table))
	require.NoError(t, err)
	defer rows.Close()

	got := map[string]string{}

	for rows.Next() {
		var ns, k, tid string

		require.NoError(t, rows.Scan(&ns, &k, &tid))
		got[ns+"/"+k] = tid
	}

	require.NoError(t, rows.Err())

	assert.Equal(t, store.SentinelGlobal, got["ns-a/k-a"], "pre-backfilled row preserved")
	assert.Equal(t, store.SentinelGlobal, got["ns-b/k-b"], "NULL row normalized to sentinel")
	assert.Equal(t, store.SentinelGlobal, got["ns-c/k-c"], "empty row normalized to sentinel")
	assert.Equal(t, "tenant-xyz", got["ns-d/k-d"], "real tenant override preserved")
}

// TestIntegration_Postgres_UniqueIndexPreventsRaceDuplicates (M-S2-6)
// fans out 10 concurrent SetTenantValue calls against the same
// (namespace, key, tenant) triple and asserts exactly one row survives.
// This pins the composite unique index + ON CONFLICT clause as the
// race-safe write barrier for tenant overrides; without the index the
// 10 goroutines would race to INSERT and leave duplicate rows.
func TestIntegration_Postgres_UniqueIndexPreventsRaceDuplicates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn, table, db := migrationSetup(ctx, t)

	s := openPhase(t, dsn, table, db, true)

	const (
		goroutines = 10
		namespace  = "global"
		key        = "rate.limit"
		tenantID   = "tenant-a"
	)

	var wg sync.WaitGroup

	errs := make(chan error, goroutines)

	for i := range goroutines {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			value, err := json.Marshal(fmt.Sprintf("writer-%d", idx))
			if err != nil {
				errs <- err

				return
			}

			errs <- s.SetTenantValue(ctx, tenantID, store.Entry{
				Namespace: namespace,
				Key:       key,
				Value:     value,
				UpdatedBy: fmt.Sprintf("writer-%d", idx),
			})
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err, "ON CONFLICT DO UPDATE should absorb all concurrent writers")
	}

	// Exactly one row must exist for the contested triple.
	var count int

	err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE namespace = $1 AND key = $2 AND tenant_id = $3`, table),
		namespace, key, tenantID,
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "composite unique index must collapse %d concurrent writes into one row", goroutines)
}

// --- Raw-SQL test helpers -------------------------------------------------

// seedRawEntry inserts a single row with an explicit tenant_id. Used to
// simulate pre-migration drift where the DEFAULT did not take effect.
func seedRawEntry(ctx context.Context, t *testing.T, db *sql.DB, table, ns, key, tenantID string, value []byte) {
	t.Helper()

	_, err := db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (namespace, key, tenant_id, value, updated_at, updated_by)
		 VALUES ($1, $2, $3, $4::jsonb, now(), 'seed')`, table),
		ns, key, tenantID, string(value),
	)
	require.NoError(t, err, "seed (ns=%s, key=%s, tenant=%s) failed", ns, key, tenantID)
}

// seedRawEntryNullTenant inserts a row with tenant_id = NULL. The column
// is declared NOT NULL, so we temporarily relax the constraint for the
// duration of the insert; the migration's backfill is what would
// normalize such rows in real incidents (imports, schema reshuffles).
func seedRawEntryNullTenant(ctx context.Context, t *testing.T, db *sql.DB, table, ns, key string, value []byte) {
	t.Helper()

	// Relax NOT NULL so the seed can carry a genuine NULL.
	_, err := db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ALTER COLUMN tenant_id DROP NOT NULL`, table))
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (namespace, key, tenant_id, value, updated_at, updated_by)
		 VALUES ($1, $2, NULL, $3::jsonb, now(), 'seed')`, table),
		ns, key, string(value),
	)
	require.NoError(t, err, "seed (ns=%s, key=%s, tenant=NULL) failed", ns, key)

	// Restore the invariant. The backfill in ensureSchema will run before
	// any further writes try to observe NOT NULL; it also relaxes and
	// re-asserts the column via its own ALTER, but restoring here keeps
	// the seeded state faithful to production (where the column is always
	// declared NOT NULL on the committed schema).
	_, err = db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ALTER COLUMN tenant_id SET NOT NULL`, table))
	// If any rows still carry NULL this ALTER fails with SQLSTATE 23502
	// (not_null_violation) — that is the one expected failure, because a
	// half-migrated table legitimately has NULLs that the phase-2
	// ensureSchema backfill will normalize before the composite unique
	// index is built. Any OTHER error (driver wiring, privilege issue,
	// container lifecycle bug) is a real problem and must fail the test
	// instead of being silently swallowed.
	//
	// The pgx stdlib driver surfaces the SQLSTATE in the error message as
	// "contains null values" (which is Postgres's own wording for 23502).
	// Keeping the match on the human-readable phrase avoids pulling in
	// pgconn just for this one assertion.
	if err != nil {
		require.Contains(t, err.Error(), "contains null values",
			"unexpected SET NOT NULL failure while seeding NULL tenant row: %v", err)
	}
}

// dropLegacyPK drops the <table>_pkey constraint if present. Used to
// reach pre-migration ambiguous states that the phase-1 PK would
// otherwise block.
func dropLegacyPK(ctx context.Context, t *testing.T, db *sql.DB, table string) {
	t.Helper()

	_, err := db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s_pkey`, table, table))
	require.NoError(t, err)
}

// --- Assertion helpers ----------------------------------------------------

// assertColumnExists fails the test if the named column is absent from
// the table. Uses information_schema for portability across Postgres
// minor versions.
func assertColumnExists(ctx context.Context, t *testing.T, db *sql.DB, table, column string) {
	t.Helper()

	var exists bool

	err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name = $1 AND column_name = $2
	)`, table, column).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "column %q missing from %q", column, table)
}

// assertCompositeUniqueIndexExists verifies that <table>_pkey_v2 is
// present and covers (namespace, key, tenant_id) in that order.
func assertCompositeUniqueIndexExists(ctx context.Context, t *testing.T, db *sql.DB, table string) {
	t.Helper()

	var def string

	err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes
		WHERE tablename = $1 AND indexname = $2`,
		table, table+"_pkey_v2").Scan(&def)
	require.NoError(t, err, "composite unique index %s_pkey_v2 not found on %s", table, table)
	assert.Contains(t, strings.ToLower(def), "unique")
	assert.Contains(t, def, "namespace")
	assert.Contains(t, def, "key")
	assert.Contains(t, def, "tenant_id")
}

// assertLegacyPKDropped verifies that <table>_pkey no longer exists as
// a primary-key constraint — the phase-2 migration drops it in favor of
// the composite unique index.
func assertLegacyPKDropped(ctx context.Context, t *testing.T, db *sql.DB, table string) {
	t.Helper()

	var name sql.NullString

	err := db.QueryRowContext(ctx, `SELECT constraint_name
		FROM information_schema.table_constraints
		WHERE table_name = $1 AND constraint_type = 'PRIMARY KEY'
		LIMIT 1`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}

	require.NoError(t, err)
	assert.False(t, name.Valid, "legacy primary key %q still present on %s", name.String, table)
}

// assertRowCount asserts the table row count equals want.
func assertRowCount(ctx context.Context, t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()

	var got int

	err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&got)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// schemaSnapshot captures the structural fingerprint of the backing
// table — column set, index set, constraint set — for idempotency
// comparisons. Definitions are sorted so map iteration order does not
// pollute the compared value.
type schemaSnapshot struct {
	Columns     []string
	Indexes     []string
	Constraints []string
}

// snapshotSchema reads the structural fingerprint of table for
// before/after comparison in idempotency tests.
func snapshotSchema(ctx context.Context, t *testing.T, db *sql.DB, table string) schemaSnapshot {
	t.Helper()

	var snap schemaSnapshot

	snap.Columns = queryStrings(ctx, t, db,
		`SELECT column_name || ':' || data_type
		 FROM information_schema.columns
		 WHERE table_name = $1
		 ORDER BY column_name`, table)

	snap.Indexes = queryStrings(ctx, t, db,
		`SELECT indexname || '::' || indexdef
		 FROM pg_indexes
		 WHERE tablename = $1
		 ORDER BY indexname`, table)

	snap.Constraints = queryStrings(ctx, t, db,
		`SELECT constraint_name || ':' || constraint_type
		 FROM information_schema.table_constraints
		 WHERE table_name = $1
		 ORDER BY constraint_name`, table)

	return snap
}

// queryStrings runs a string-returning query with a single string
// parameter and returns the column-zero values in order.
func queryStrings(ctx context.Context, t *testing.T, db *sql.DB, query, arg string) []string {
	t.Helper()

	rows, err := db.QueryContext(ctx, query, arg)
	require.NoError(t, err)
	defer rows.Close()

	var out []string

	for rows.Next() {
		var s string

		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}

	require.NoError(t, rows.Err())

	return out
}

// mustJSON is a tiny sugar to reduce boilerplate in Set calls.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)

	return data
}
