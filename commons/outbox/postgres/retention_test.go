//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// stringSliceConverter renders a []string argument so the test can pin the
// normalized keep list sqlmock would otherwise refuse to convert.
type stringSliceConverter struct{}

func (stringSliceConverter) ConvertValue(value any) (driver.Value, error) {
	if values, ok := value.([]string); ok {
		return strings.Join(values, ","), nil
	}

	return driver.DefaultParameterConverter.ConvertValue(value)
}

func TestDeletePublishedBefore_NotInitialized(t *testing.T) {
	t.Parallel()

	deleted, err := uninitializedRepo().DeletePublishedBefore(context.Background(), time.Now().UTC(), nil, 10)
	require.ErrorIs(t, err, ErrRepositoryNotInitialized)
	require.Zero(t, deleted)
}

func TestDeletePublishedBefore_NonPositiveLimitNeverTouchesTheDatabase(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{0, -1} {
		repo := &Repository{
			client:             newTestClient(t),
			tenantResolver:     noopTenantResolver{},
			tenantDiscoverer:   noopTenantDiscoverer{},
			tableName:          "outbox_events",
			transactionTimeout: time.Second,
			primaryDBLookup: func(context.Context) (*sql.DB, error) {
				t.Fatalf("limit %d must not reach the database", limit)
				return nil, nil
			},
		}

		deleted, err := repo.DeletePublishedBefore(context.Background(), time.Now().UTC(), nil, limit)
		require.NoError(t, err)
		require.Zero(t, deleted)
	}
}

func TestDeletePublishedBefore_SkipsWhenTenantTableMissing(t *testing.T) {
	t.Parallel()

	repo := &Repository{
		client:             newTestClient(t),
		tenantResolver:     NoopTenantResolver{},
		tenantDiscoverer:   poolDiscovererShim{},
		poolResolver:       &recordingTenantPoolResolver{},
		requireTenant:      true,
		tableName:          "outbox_events",
		transactionTimeout: time.Second,
		tablePresence: newTablePresenceGuard(func(context.Context, string) (bool, error) {
			return false, nil
		}, time.Hour),
		primaryDBLookup: func(context.Context) (*sql.DB, error) {
			t.Fatal("primaryDBLookup must not be reached when the table is missing")
			return nil, nil
		},
	}

	deleted, err := repo.DeletePublishedBefore(validTenantCtx(), time.Now().UTC(), nil, 10)
	require.NoError(t, err)
	require.Zero(t, deleted)
}

func TestDeletePublishedBefore_BoundedOldestFirstDelete(t *testing.T) {
	t.Parallel()

	before := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		tenantColumn string
		keep         []string
		query        string
		args         []driver.Value
	}{
		{
			name: "no keep list, no tenant column",
			keep: []string{" ", ""},
			query: `DELETE FROM "outbox_events" WHERE id IN (SELECT id FROM "outbox_events" ` +
				`WHERE status = $1::outbox_event_status AND created_at < $2 ORDER BY created_at ASC, id ASC LIMIT $3)`,
			args: []driver.Value{"PUBLISHED", before, 500},
		},
		{
			name: "keep list excluded",
			keep: []string{"leilao.solicitado", " margem.solicitada "},
			query: `DELETE FROM "outbox_events" WHERE id IN (SELECT id FROM "outbox_events" ` +
				`WHERE status = $1::outbox_event_status AND created_at < $2 AND NOT (event_type = ANY($3::text[])) ` +
				`ORDER BY created_at ASC, id ASC LIMIT $4)`,
			args: []driver.Value{"PUBLISHED", before, "leilao.solicitado,margem.solicitada", 500},
		},
		{
			name:         "tenant column scopes both the selection and the delete",
			tenantColumn: "tenant_id",
			query: `DELETE FROM "outbox_events" WHERE id IN (SELECT id FROM "outbox_events" ` +
				`WHERE status = $1::outbox_event_status AND created_at < $2 AND "tenant_id" = $3 ` +
				`ORDER BY created_at ASC, id ASC LIMIT $4) AND "tenant_id" = $3`,
			args: []driver.Value{"PUBLISHED", before, "22222222-2222-2222-2222-222222222222", 500},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
			require.NoError(t, err)

			defer func() { _ = db.Close() }()

			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(test.query)).
				WithArgs(test.args...).
				WillReturnResult(sqlmock.NewResult(0, 37))
			mock.ExpectCommit()

			repo := &Repository{
				client:             newTestClient(t),
				tenantResolver:     noopTenantResolver{},
				tenantDiscoverer:   noopTenantDiscoverer{},
				tenantColumn:       test.tenantColumn,
				tableName:          "outbox_events",
				transactionTimeout: time.Second,
				primaryDBLookup: func(context.Context) (*sql.DB, error) {
					return db, nil
				},
			}

			deleted, err := repo.DeletePublishedBefore(validTenantCtx(), before, test.keep, 500)
			require.NoError(t, err)
			require.Equal(t, int64(37), deleted)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDeletePublishedBefore_BoundsTheStatementByTheTransactionTimeout(t *testing.T) {
	t.Parallel()

	before := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(stringSliceConverter{}))
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	// A DELETE waiting on a row lock looks like this: the statement does not
	// return. The caller passed no deadline, so the repository's transaction
	// timeout has to end the wait.
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM "outbox_events" WHERE id IN (SELECT id FROM "outbox_events" `+
		`WHERE status = $1::outbox_event_status AND created_at < $2 ORDER BY created_at ASC, id ASC LIMIT $3)`)).
		WithArgs("PUBLISHED", before, 500).
		WillDelayFor(2 * time.Second).
		WillReturnResult(sqlmock.NewResult(0, 500))
	mock.ExpectRollback()

	repo := &Repository{
		client:             newTestClient(t),
		tenantResolver:     noopTenantResolver{},
		tenantDiscoverer:   noopTenantDiscoverer{},
		tableName:          "outbox_events",
		transactionTimeout: 50 * time.Millisecond,
		primaryDBLookup: func(context.Context) (*sql.DB, error) {
			return db, nil
		},
	}

	started := time.Now()
	deleted, err := repo.DeletePublishedBefore(validTenantCtx(), before, nil, 500)
	elapsed := time.Since(started)

	require.Error(t, err)
	require.Zero(t, deleted)
	require.Less(t, elapsed, time.Second, "the statement must stop at the transaction timeout, not at the lock holder's pace")

	// The transaction context carries the same deadline as the statement, and
	// database/sql rolls a transaction back on its own goroutine when that
	// context expires. Whichever side wins the race, the driver sees exactly one
	// Rollback; when the deadline side wins it may land after the return above.
	require.Eventually(t, func() bool { return mock.ExpectationsWereMet() == nil },
		time.Second, 5*time.Millisecond, "the transaction must be rolled back exactly once")
}
