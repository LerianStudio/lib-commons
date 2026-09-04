//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v7/commons/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sqlmockSliceConverter struct{}

func (sqlmockSliceConverter) ConvertValue(value any) (driver.Value, error) {
	switch value.(type) {
	case []uuid.UUID:
		return "uuid-array", nil
	default:
		return driver.DefaultParameterConverter.ConvertValue(value)
	}
}

func TestRepository_ListPendingByTypes_Validation(t *testing.T) {
	t.Parallel()

	repo := &Repository{
		client:             &libPostgres.Client{},
		tenantResolver:     noopTenantResolver{},
		tenantDiscoverer:   noopTenantDiscoverer{},
		tableName:          defaultOutboxTableName,
		transactionTimeout: time.Second,
	}

	tests := []struct {
		name       string
		eventTypes []string
		limit      int
		wantErr    error
	}{
		{name: "non-positive limit", eventTypes: []string{"dict.created"}, wantErr: ErrLimitMustBePositive},
		{name: "nil types", eventTypes: nil, limit: 1, wantErr: ErrEventTypeRequired},
		{name: "only blank types", eventTypes: []string{" ", "\t"}, limit: 1, wantErr: ErrEventTypeRequired},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			events, err := repo.ListPendingByTypes(context.Background(), testCase.eventTypes, testCase.limit)
			require.ErrorIs(t, err, testCase.wantErr)
			assert.Nil(t, events)
		})
	}
}

func TestRepository_ListPendingByTypes_UninitializedWithNilContext(t *testing.T) {
	t.Parallel()

	events, err := (&Repository{}).ListPendingByTypes(nil, []string{"dict.high"}, 1)
	require.ErrorIs(t, err, ErrRepositoryNotInitialized)
	assert.Nil(t, events)
}

func TestRepository_ListPendingByTypes_ClaimsInPriorityAndFIFOOrder(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(sqlmockSliceConverter{}))
	require.NoError(t, err)

	repo := &Repository{
		client:           &libPostgres.Client{},
		tenantResolver:   noopTenantResolver{},
		tenantDiscoverer: noopTenantDiscoverer{},
		primaryDBLookup: func(context.Context) (*sql.DB, error) {
			return db, nil
		},
		tableName:          defaultOutboxTableName,
		transactionTimeout: time.Second,
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	firstID := uuid.New()
	secondID := uuid.New()
	rows := sqlmock.NewRows([]string{
		"id", "event_type", "aggregate_id", "payload", "status", "attempts",
		"published_at", "last_error", "created_at", "updated_at",
	}).
		AddRow(firstID, "dict.high", uuid.New(), []byte(`{"order":1}`), outbox.OutboxStatusPending, 0, nil, nil, now, now).
		AddRow(secondID, "dict.low", uuid.New(), []byte(`{"order":2}`), outbox.OutboxStatusPending, 0, nil, nil, now.Add(-time.Hour), now.Add(-time.Hour))

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*event_type IN \(\$2, \$3\).*ORDER BY CASE event_type WHEN \$2 THEN 0 WHEN \$3 THEN 1 ELSE 2 END, created_at ASC, id ASC LIMIT \$4 FOR UPDATE SKIP LOCKED`).
		WithArgs(outbox.OutboxStatusPending, "dict.high", "dict.low", 2).
		WillReturnRows(rows)
	mock.ExpectExec(`UPDATE .* SET status = \$1::outbox_event_status, updated_at = \$2 WHERE id = ANY\(\$3::uuid\[\]\) AND status = \$4::outbox_event_status`).
		WithArgs(outbox.OutboxStatusProcessing, sqlmock.AnyArg(), sqlmock.AnyArg(), outbox.OutboxStatusPending).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	mock.ExpectClose()

	events, err := repo.ListPendingByTypes(context.Background(), []string{" dict.high ", "dict.low", "dict.high"}, 2)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, firstID, events[0].ID)
	assert.Equal(t, secondID, events[1].ID)
	assert.Equal(t, outbox.OutboxStatusProcessing, events[0].Status)
	assert.Equal(t, outbox.OutboxStatusProcessing, events[1].Status)
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepository_ListPendingByTypes_ReturnsEmptyWhenNothingIsPending(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	repo := newPendingTypesRepository(db)
	rows := sqlmock.NewRows([]string{
		"id", "event_type", "aggregate_id", "payload", "status", "attempts",
		"published_at", "last_error", "created_at", "updated_at",
	})

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*event_type IN \(\$2\).*FOR UPDATE SKIP LOCKED`).
		WithArgs(outbox.OutboxStatusPending, "dict.high", 1).
		WillReturnRows(rows)
	mock.ExpectCommit()
	mock.ExpectClose()

	events, err := repo.ListPendingByTypes(context.Background(), []string{"dict.high"}, 1)
	require.NoError(t, err)
	assert.Empty(t, events)
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepository_ListPendingByTypes_RollsBackWhenSelectionFails(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	repo := newPendingTypesRepository(db)
	queryErr := errors.New("query failed")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*event_type IN \(\$2\).*FOR UPDATE SKIP LOCKED`).
		WithArgs(outbox.OutboxStatusPending, "dict.high", 1).
		WillReturnError(queryErr)
	mock.ExpectRollback()
	mock.ExpectClose()

	events, err := repo.ListPendingByTypes(context.Background(), []string{"dict.high"}, 1)
	require.ErrorIs(t, err, queryErr)
	assert.Nil(t, events)
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepository_ListPendingByTypes_RollsBackWhenClaimUpdateFails(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(sqlmockSliceConverter{}))
	require.NoError(t, err)

	repo := newPendingTypesRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	rows := sqlmock.NewRows([]string{
		"id", "event_type", "aggregate_id", "payload", "status", "attempts",
		"published_at", "last_error", "created_at", "updated_at",
	}).AddRow(uuid.New(), "dict.high", uuid.New(), []byte(`{"order":1}`), outbox.OutboxStatusPending, 0, nil, nil, now, now)
	updateErr := errors.New("update failed")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*event_type IN \(\$2\).*FOR UPDATE SKIP LOCKED`).
		WithArgs(outbox.OutboxStatusPending, "dict.high", 1).
		WillReturnRows(rows)
	mock.ExpectExec(`UPDATE .* SET status = \$1::outbox_event_status`).
		WithArgs(outbox.OutboxStatusProcessing, sqlmock.AnyArg(), sqlmock.AnyArg(), outbox.OutboxStatusPending).
		WillReturnError(updateErr)
	mock.ExpectRollback()
	mock.ExpectClose()

	events, err := repo.ListPendingByTypes(context.Background(), []string{"dict.high"}, 1)
	require.ErrorIs(t, err, updateErr)
	assert.Nil(t, events)
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepository_ListPendingByTypes_ReportsPrimaryDatabaseFailure(t *testing.T) {
	t.Parallel()

	lookupErr := errors.New("primary database unavailable")
	repo := &Repository{
		client:           &libPostgres.Client{},
		tenantResolver:   noopTenantResolver{},
		tenantDiscoverer: noopTenantDiscoverer{},
		primaryDBLookup: func(context.Context) (*sql.DB, error) {
			return nil, lookupErr
		},
		tableName:          defaultOutboxTableName,
		transactionTimeout: time.Second,
	}

	events, err := repo.ListPendingByTypes(context.Background(), []string{"dict.high"}, 1)
	require.ErrorIs(t, err, lookupErr)
	assert.Nil(t, events)
}

func newPendingTypesRepository(db *sql.DB) *Repository {
	return &Repository{
		client:           &libPostgres.Client{},
		tenantResolver:   noopTenantResolver{},
		tenantDiscoverer: noopTenantDiscoverer{},
		primaryDBLookup: func(context.Context) (*sql.DB, error) {
			return db, nil
		},
		tableName:          defaultOutboxTableName,
		transactionTimeout: time.Second,
	}
}

var _ outbox.MultiTypePendingRepository = (*Repository)(nil)
