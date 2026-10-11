//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lockRetryTestID = "4242"

const advisoryLockQuery = `SELECT pg_advisory_lock\(\$1\)`

// lockDB is one sqlmock database whose advisory-lock calls follow expect.
func lockDB(t *testing.T, expect func(sqlmock.Sqlmock)) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	expect(mock)

	return db, mock
}

// lock runs lockedMigrationConn and closes the connection it returns.
func lock(ctx context.Context, db *sql.DB, run migrationRun) error {
	conn, err := lockedMigrationConn(ctx, db, lockRetryTestID, run)
	if err != nil {
		return err
	}

	return conn.Close()
}

// expectBusy scripts a lock wait that outlasts any LockTimeout in these tests.
func expectBusy(mock sqlmock.Sqlmock) {
	mock.ExpectExec(advisoryLockQuery).WithArgs(lockRetryTestID).
		WillDelayFor(time.Minute).WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectGranted(mock sqlmock.Sqlmock) {
	mock.ExpectExec(advisoryLockQuery).WithArgs(lockRetryTestID).WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestLockedMigrationConn_Retries(t *testing.T) {
	t.Parallel()

	t.Run("a lock busy twice is taken on the third wait", func(t *testing.T) {
		t.Parallel()

		db, mock := lockDB(t, func(mock sqlmock.Sqlmock) {
			expectBusy(mock)
			expectBusy(mock)
			expectGranted(mock)
		})

		logger := &capturingLogger{}
		run := migrationRun{
			lockTimeout:      20 * time.Millisecond,
			lockRetries:      3,
			lockRetryBackoff: time.Millisecond,
			logger:           logger,
		}

		require.NoError(t, lock(context.Background(), db, run))
		require.NoError(t, mock.ExpectationsWereMet())
		assert.Contains(t, logger.all(), "migration lock busy; retrying")
	})

	t.Run("exhausted retries fail with ErrMigrationLockTimeout", func(t *testing.T) {
		t.Parallel()

		db, mock := lockDB(t, func(mock sqlmock.Sqlmock) {
			expectBusy(mock)
			expectBusy(mock)
			expectBusy(mock)
		})

		run := migrationRun{lockTimeout: 20 * time.Millisecond, lockRetries: 2, lockRetryBackoff: time.Millisecond}

		err := lock(context.Background(), db, run)
		require.ErrorIs(t, err, ErrMigrationLockTimeout)
		assert.Contains(t, err.Error(), "3 attempts")
		require.NoError(t, mock.ExpectationsWereMet(), "one first wait plus exactly two retries")
	})

	t.Run("no retries configured fails on the first busy wait", func(t *testing.T) {
		t.Parallel()

		db, mock := lockDB(t, expectBusy)

		err := lock(context.Background(), db, migrationRun{lockTimeout: 20 * time.Millisecond})
		require.ErrorIs(t, err, ErrMigrationLockTimeout)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("any other lock failure is never retried", func(t *testing.T) {
		t.Parallel()

		db, mock := lockDB(t, func(mock sqlmock.Sqlmock) {
			mock.ExpectExec(advisoryLockQuery).WithArgs(lockRetryTestID).WillReturnError(errors.New("connection reset"))
		})

		run := migrationRun{lockTimeout: time.Second, lockRetries: 5, lockRetryBackoff: time.Millisecond}

		err := lock(context.Background(), db, run)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrMigrationLockTimeout)
		assert.Contains(t, err.Error(), "failed to acquire migration lock")
		require.NoError(t, mock.ExpectationsWereMet(), "a single attempt")
	})

	t.Run("a cancel during the pause stops waiting", func(t *testing.T) {
		t.Parallel()

		db, mock := lockDB(t, expectBusy)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		time.AfterFunc(200*time.Millisecond, cancel)

		run := migrationRun{lockTimeout: 20 * time.Millisecond, lockRetries: 5, lockRetryBackoff: time.Hour}

		started := time.Now()
		err := lock(ctx, db, run)
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, ErrMigrationLockTimeout)
		assert.Less(t, time.Since(started), 10*time.Second)
		require.NoError(t, mock.ExpectationsWereMet(), "no second wait after the cancel")
	})

	t.Run("a cancel during a wait stops retrying", func(t *testing.T) {
		t.Parallel()

		db, mock := lockDB(t, func(mock sqlmock.Sqlmock) {
			expectBusy(mock)
			expectBusy(mock)
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		time.AfterFunc(300*time.Millisecond, cancel)

		run := migrationRun{lockTimeout: 200 * time.Millisecond, lockRetries: 5, lockRetryBackoff: time.Millisecond}

		err := lock(ctx, db, run)
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, ErrMigrationLockTimeout)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestMigrationLockRetryPause(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 100*time.Millisecond, migrationLockRetryPause(100*time.Millisecond, 1))
	assert.Equal(t, 200*time.Millisecond, migrationLockRetryPause(100*time.Millisecond, 2))
	assert.Equal(t, 400*time.Millisecond, migrationLockRetryPause(100*time.Millisecond, 3))
	assert.Equal(t, time.Duration(0), migrationLockRetryPause(0, 3), "zero backoff retries at once")
}

func TestMigrator_PassesLockRetriesToTheRun(t *testing.T) {
	t.Parallel()

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:       "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
		DatabaseName:     "ledger",
		MigrationsFS:     embeddedMigrations(),
		LockTimeout:      time.Second,
		LockRetries:      4,
		LockRetryBackoff: 250 * time.Millisecond,
	})
	require.NoError(t, err)

	run := m.baseRun()
	assert.Equal(t, 4, run.lockRetries)
	assert.Equal(t, 250*time.Millisecond, run.lockRetryBackoff)
}
