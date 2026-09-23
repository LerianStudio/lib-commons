//go:build unit

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v7/commons/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

var testTraceContextJSON = []byte(`{"traceparent":"` + testTraceparent + `"}`)

func TestNewRepository_TraceContextColumnValidation(t *testing.T) {
	t.Parallel()

	_, err := NewRepository(
		&libPostgres.Client{},
		noopTenantResolver{},
		noopTenantDiscoverer{},
		WithTraceContextColumn(`bad"column`),
	)
	require.ErrorIs(t, err, ErrInvalidIdentifier)
}

func TestRepository_CreateWithTx_PersistsTraceContextWhenColumnConfigured(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	repo, err := NewRepository(
		&libPostgres.Client{},
		noopTenantResolver{},
		noopTenantDiscoverer{},
		WithTraceContextColumn(DefaultTraceContextColumn),
	)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	event := &outbox.OutboxEvent{
		ID:           uuid.New(),
		EventType:    "payment.created",
		AggregateID:  uuid.New(),
		Payload:      []byte(`{"ok":true}`),
		CreatedAt:    now,
		UpdatedAt:    now,
		TraceContext: map[string]string{outbox.TraceContextTraceparent: testTraceparent},
	}

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	mock.ExpectQuery(`INSERT INTO "outbox_events"`).
		WithArgs(
			event.ID, event.EventType, event.AggregateID, event.Payload,
			outbox.OutboxStatusPending, 0, nil, "", event.CreatedAt, event.UpdatedAt,
			testTraceContextJSON,
		).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "event_type", "aggregate_id", "payload", "status", "attempts",
			"published_at", "last_error", "created_at", "updated_at", "trace_context",
		}).AddRow(
			event.ID, event.EventType, event.AggregateID, event.Payload,
			outbox.OutboxStatusPending, 0, nil, "", event.CreatedAt, event.UpdatedAt,
			testTraceContextJSON,
		))

	created, err := repo.CreateWithTx(context.Background(), tx, event)
	require.NoError(t, err)
	require.Equal(
		t,
		map[string]string{outbox.TraceContextTraceparent: testTraceparent},
		created.TraceContext,
	)

	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepository_CreateWithTx_OmitsTraceContextWhenColumnAbsent(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	repo, err := NewRepository(
		&libPostgres.Client{},
		noopTenantResolver{},
		noopTenantDiscoverer{},
	)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	event := &outbox.OutboxEvent{
		ID:           uuid.New(),
		EventType:    "payment.created",
		AggregateID:  uuid.New(),
		Payload:      []byte(`{"ok":true}`),
		CreatedAt:    now,
		UpdatedAt:    now,
		TraceContext: map[string]string{outbox.TraceContextTraceparent: testTraceparent},
	}

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	mock.ExpectQuery(`INSERT INTO "outbox_events"`).
		WithArgs(
			event.ID, event.EventType, event.AggregateID, event.Payload,
			outbox.OutboxStatusPending, 0, nil, "", event.CreatedAt, event.UpdatedAt,
		).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "event_type", "aggregate_id", "payload", "status", "attempts",
			"published_at", "last_error", "created_at", "updated_at",
		}).AddRow(
			event.ID, event.EventType, event.AggregateID, event.Payload,
			outbox.OutboxStatusPending, 0, nil, "", event.CreatedAt, event.UpdatedAt,
		))

	created, err := repo.CreateWithTx(context.Background(), tx, event)
	require.NoError(t, err)
	require.Nil(t, created.TraceContext)

	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepository_SelectColumns(t *testing.T) {
	t.Parallel()

	withColumn, err := NewRepository(
		&libPostgres.Client{},
		noopTenantResolver{},
		noopTenantDiscoverer{},
		WithTraceContextColumn(DefaultTraceContextColumn),
	)
	require.NoError(t, err)
	require.Equal(t, outboxColumns+`, "trace_context"`, withColumn.selectColumns())

	withoutColumn, err := NewRepository(
		&libPostgres.Client{},
		noopTenantResolver{},
		noopTenantDiscoverer{},
	)
	require.NoError(t, err)
	require.Equal(t, outboxColumns, withoutColumn.selectColumns())
}

func TestEncodeDecodeTraceContext(t *testing.T) {
	t.Parallel()

	t.Run("nil carrier encodes to SQL NULL", func(t *testing.T) {
		t.Parallel()

		encoded, err := EncodeTraceContext(nil)
		require.NoError(t, err)
		require.Nil(t, encoded)
	})

	t.Run("carrier round trips", func(t *testing.T) {
		t.Parallel()

		encoded, err := EncodeTraceContext(map[string]string{
			outbox.TraceContextTraceparent: testTraceparent,
			"baggage":                      "user=root",
		})
		require.NoError(t, err)
		require.Equal(t, testTraceContextJSON, encoded)

		raw, ok := encoded.([]byte)
		require.True(t, ok)
		require.Equal(
			t,
			map[string]string{outbox.TraceContextTraceparent: testTraceparent},
			decodeTraceContext(raw),
		)
	})

	t.Run("NULL decodes to nil", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, decodeTraceContext(nil))
	})

	t.Run("malformed JSON reads as an absent carrier, never an error", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, decodeTraceContext([]byte("not-json")))
	})
}
