//go:build unit

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusal is a migration refused by the session's lock_timeout, as
// golang-migrate's postgres driver returns it.
func refusal() error {
	return database.Error{
		OrigErr: &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"},
		Err:     "migration failed",
	}
}

// step is one scripted Steps(1): the version it reaches and whether the
// migration was marked dirty before err.
type step struct {
	version uint
	dirty   bool
	err     error
}

// refusalMig scripts *migrate.Migrate for applyMigrationsRetrying.
type refusalMig struct {
	steps    []step
	version  uint
	dirty    bool
	applied  bool
	forced   []int
	forceErr error
}

func (m *refusalMig) Steps(int) error {
	if len(m.steps) == 0 {
		return os.ErrNotExist
	}

	next := m.steps[0]
	m.steps = m.steps[1:]

	if next.err == nil || next.dirty {
		m.version, m.dirty, m.applied = next.version, next.dirty, true
	}

	return next.err
}

func (m *refusalMig) Up() error { return migrate.ErrNoChange }

func (m *refusalMig) Version() (uint, bool, error) {
	if !m.applied {
		return 0, false, migrate.ErrNilVersion
	}

	return m.version, m.dirty, nil
}

func (m *refusalMig) Force(version int) error {
	m.forced = append(m.forced, version)

	if m.forceErr != nil {
		return m.forceErr
	}

	m.dirty = false
	m.applied = version >= 0

	if m.applied {
		m.version = uint(version)
	}

	return nil
}

// refusalSource ships versions 1, 3 and 4 (no 2): the version before 3 is 1.
// Version 4 builds an index CONCURRENTLY, which never runs as one transaction.
func refusalSource(t *testing.T) source.Driver {
	t.Helper()

	files := fstest.MapFS{
		"000001_a.up.sql": {Data: []byte("CREATE TABLE a (id int);")},
		"000003_b.up.sql": {Data: []byte("SET lock_timeout = '3s'; ALTER TABLE a ADD COLUMN b int;")},
		"000004_c.up.sql": {Data: []byte("CREATE INDEX CONCURRENTLY a_b ON a (b);")},
	}

	src, err := iofs.New(files, ".")
	require.NoError(t, err)

	t.Cleanup(func() { _ = src.Close() })

	return src
}

func retryRun(retries int) migrationRun {
	return migrationRun{lockRetries: retries, lockRetryBackoff: time.Millisecond}
}

func TestApplyMigrationsRetrying(t *testing.T) {
	t.Parallel()

	t.Run("a refused migration is reset to the previous source version and applied on retry", func(t *testing.T) {
		t.Parallel()

		logger := &capturingLogger{}
		run := retryRun(3)
		run.logger = logger

		mig := &refusalMig{steps: []step{
			{version: 1},
			{version: 3, dirty: true, err: refusal()},
			{version: 3},
		}}

		require.NoError(t, applyMigrationsRetrying(context.Background(), mig, refusalSource(t), run))
		assert.Equal(t, []int{1}, mig.forced, "the version before 3 in the source is 1, never 2")
		assert.Equal(t, uint(3), mig.version)
		assert.False(t, mig.dirty)
		assert.Contains(t, logger.all(), "migration refused by lock_timeout; retrying")
	})

	t.Run("a refused first migration is reset to no version", func(t *testing.T) {
		t.Parallel()

		mig := &refusalMig{steps: []step{
			{version: 1, dirty: true, err: refusal()},
			{version: 1},
		}}

		require.NoError(t, applyMigrationsRetrying(context.Background(), mig, refusalSource(t), retryRun(1)))
		assert.Equal(t, []int{database.NilVersion}, mig.forced)
	})

	t.Run("exhausted retries return the refusal and leave the version clean", func(t *testing.T) {
		t.Parallel()

		mig := &refusalMig{steps: []step{
			{version: 1},
			{version: 3, dirty: true, err: refusal()},
			{version: 3, dirty: true, err: refusal()},
			{version: 3, dirty: true, err: refusal()},
		}}

		err := applyMigrationsRetrying(context.Background(), mig, refusalSource(t), retryRun(2))
		require.Error(t, err)
		assert.True(t, refusedByLockTimeout(err))
		assert.Equal(t, []int{1, 1, 1}, mig.forced, "cleared after every refusal, the last one too")
		assert.Equal(t, uint(1), mig.version)
		assert.False(t, mig.dirty, "the next run starts clean instead of refusing a dirty schema")
		assert.Empty(t, mig.steps)
	})

	t.Run("no retries configured leaves a refusal dirty, as before", func(t *testing.T) {
		t.Parallel()

		mig := &refusalMig{steps: []step{{version: 1, dirty: true, err: refusal()}}}

		err := applyMigrationsRetrying(context.Background(), mig, refusalSource(t), retryRun(0))
		require.Error(t, err)
		assert.Empty(t, mig.forced)
		assert.True(t, mig.dirty)
	})

	t.Run("any other failure is never retried", func(t *testing.T) {
		t.Parallel()

		failure := database.Error{OrigErr: &pgconn.PgError{Code: "42P07"}, Err: "migration failed"}
		mig := &refusalMig{steps: []step{{version: 1, dirty: true, err: failure}}}

		err := applyMigrationsRetrying(context.Background(), mig, refusalSource(t), retryRun(5))
		assert.Equal(t, failure, err, "returned as it came")
		assert.Empty(t, mig.forced)
		assert.True(t, mig.dirty)
	})

	t.Run("a migration that may have committed part of itself stays dirty", func(t *testing.T) {
		t.Parallel()

		logger := &capturingLogger{}
		run := retryRun(5)
		run.logger = logger

		mig := &refusalMig{steps: []step{
			{version: 1},
			{version: 3},
			{version: 4, dirty: true, err: refusal()},
		}}

		err := applyMigrationsRetrying(context.Background(), mig, refusalSource(t), run)
		require.Error(t, err)
		assert.True(t, refusedByLockTimeout(err))
		assert.Empty(t, mig.forced)
		assert.True(t, mig.dirty)
		assert.Contains(t, logger.all(), "not provably one transaction")
	})

	t.Run("multi-statement runs commit statement by statement and stay dirty", func(t *testing.T) {
		t.Parallel()

		run := retryRun(5)
		run.allowMultiStatements = true

		mig := &refusalMig{steps: []step{{version: 1, dirty: true, err: refusal()}}}

		require.Error(t, applyMigrationsRetrying(context.Background(), mig, refusalSource(t), run))
		assert.Empty(t, mig.forced)
		assert.True(t, mig.dirty)
	})

	t.Run("a refusal before the migration was marked dirty is retried with nothing to clear", func(t *testing.T) {
		t.Parallel()

		mig := &refusalMig{steps: []step{
			{version: 1},
			{err: refusal()},
			{version: 3},
		}}

		require.NoError(t, applyMigrationsRetrying(context.Background(), mig, refusalSource(t), retryRun(1)))
		assert.Empty(t, mig.forced)
		assert.Equal(t, uint(3), mig.version)
	})

	t.Run("a failed reset returns the refusal and stays dirty", func(t *testing.T) {
		t.Parallel()

		mig := &refusalMig{
			steps:    []step{{version: 1, dirty: true, err: refusal()}},
			forceErr: errors.New("connection reset"),
		}

		err := applyMigrationsRetrying(context.Background(), mig, refusalSource(t), retryRun(5))
		require.Error(t, err)
		assert.True(t, refusedByLockTimeout(err))
		assert.Equal(t, []int{database.NilVersion}, mig.forced, "one attempt, no retry")
		assert.True(t, mig.dirty)
	})

	t.Run("a cancel during the pause stops with the version clean", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		time.AfterFunc(100*time.Millisecond, cancel)

		run := migrationRun{lockRetries: 5, lockRetryBackoff: time.Hour}
		mig := &refusalMig{steps: []step{
			{version: 1},
			{version: 3, dirty: true, err: refusal()},
		}}

		started := time.Now()
		require.NoError(t, applyMigrationsRetrying(ctx, mig, refusalSource(t), run),
			"the caller reports the stop from ctx, as for a stop between migrations")
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		assert.Less(t, time.Since(started), 10*time.Second)
		assert.Equal(t, uint(1), mig.version)
		assert.False(t, mig.dirty)
	})
}

func TestRefusedByLockTimeout(t *testing.T) {
	t.Parallel()

	pgRefusal := &pgconn.PgError{Code: "55P03"}

	assert.True(t, refusedByLockTimeout(refusal()), "golang-migrate's Run error, by value")
	assert.True(t, refusedByLockTimeout(&database.Error{OrigErr: pgRefusal}), "its SetVersion error, by pointer")
	assert.True(t, refusedByLockTimeout(pgRefusal))
	assert.False(t, refusedByLockTimeout(database.Error{OrigErr: &pgconn.PgError{Code: "57014"}}), "a statement_timeout cut")
	assert.False(t, refusedByLockTimeout(errors.New("lock timeout")))
	assert.False(t, refusedByLockTimeout(nil))
}

func TestRunsAsOneTransaction(t *testing.T) {
	t.Parallel()

	atomic := map[string]string{
		"one statement":       "ALTER TABLE a ADD COLUMN b int",
		"several statements":  "SET lock_timeout = '3s'; ALTER TABLE a ADD COLUMN b int; CREATE INDEX a_b ON a (b);",
		"an explicit block":   "BEGIN; ALTER TABLE a ADD COLUMN b int; COMMIT;",
		"END closing a block": "BEGIN;\nALTER TABLE a ADD COLUMN b int;\nEND;\n-- trailing comment\n",
		"plpgsql body with END and COMMIT words": `CREATE FUNCTION f() RETURNS trigger AS $$
BEGIN
  IF NEW.x THEN RAISE EXCEPTION 'no COMMIT here; END'; END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql; CREATE TRIGGER t BEFORE UPDATE ON a FOR EACH ROW EXECUTE FUNCTION f();`,
		"tagged dollar body":          "DO $body$ BEGIN COMMIT; END $body$; SELECT 1;",
		"DO among other statements":   "SET lock_timeout = '3s'; DO $$ BEGIN PERFORM 1; END $$;",
		"keywords in comments":        "-- COMMIT; ROLLBACK;\n/* CREATE INDEX CONCURRENTLY /* nested */ x; */ ALTER TABLE a ADD COLUMN b int;",
		"keywords in strings":         "COMMENT ON TABLE a IS 'COMMIT; CONCURRENTLY; ROLLBACK'; SELECT E'it\\'s; COMMIT';",
		"keywords in identifiers":     `ALTER TABLE a ADD COLUMN "commit;concurrently" int; SELECT 1;`,
		"CASE ... END in a statement": "UPDATE a SET b = CASE WHEN b > 0 THEN 1 ELSE 0 END; SELECT 1;",
		"positional parameter":        "SELECT $1; SELECT 2;",
		// PostgreSQL refuses such a file as a syntax error before running any of it.
		"unterminated dollar body": "SELECT $x$ never closed; COMMIT; SELECT 1;",
	}

	for name, sql := range atomic {
		t.Run("atomic/"+name, func(t *testing.T) {
			t.Parallel()

			assert.True(t, runsAsOneTransaction(sql))
		})
	}

	partial := map[string]string{
		"index built concurrently":      "CREATE INDEX CONCURRENTLY a_b ON a (b);",
		"concurrently among statements": "SET lock_timeout = '3s'; DROP INDEX CONCURRENTLY a_b;",
		"commit mid-file":               "BEGIN; ALTER TABLE a ADD COLUMN b int; COMMIT; ALTER TABLE a ADD COLUMN c int;",
		"end mid-file":                  "BEGIN; ALTER TABLE a ADD COLUMN b int; END; ALTER TABLE a ADD COLUMN c int;",
		"rollback":                      "BEGIN; ALTER TABLE a ADD COLUMN b int; ROLLBACK; ALTER TABLE a ADD COLUMN c int;",
		"abort":                         "ABORT; SELECT 1;",
		"prepared transaction":          "BEGIN; SELECT 1; PREPARE TRANSACTION 'x';",
		"a lone DO can commit inside":   "DO $$ BEGIN UPDATE a SET b = 1; COMMIT; END $$;",
		"a lone CALL can commit inside": "CALL backfill();",
		"lowercase keywords":            "begin; alter table a add column b int; commit; select 1;",
		"escape string then commit":     "SELECT E'\\''; COMMIT; SELECT 1;",
	}

	for name, sql := range partial {
		t.Run("partial/"+name, func(t *testing.T) {
			t.Parallel()

			assert.False(t, runsAsOneTransaction(sql))
		})
	}
}
