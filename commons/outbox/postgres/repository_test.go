//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v6/commons/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type noopTenantResolver struct{}

func (noopTenantResolver) ApplyTenant(context.Context, *sql.Tx, string) error { return nil }

type noopTenantDiscoverer struct{}

func (noopTenantDiscoverer) DiscoverTenants(context.Context) ([]string, error) { return nil, nil }

type requireTenantResolver struct{}

func (requireTenantResolver) ApplyTenant(context.Context, *sql.Tx, string) error { return nil }

func (requireTenantResolver) RequiresTenant() bool { return true }

type panicLogger struct {
	seen bool
}

func (logger *panicLogger) Log(context.Context, int, string, ...any) {
	logger.seen = true
}

func (logger *panicLogger) With(...any) obs.Logger {
	return logger
}

func (logger *panicLogger) WithGroup(string) obs.Logger {
	return logger
}

func (logger *panicLogger) Enabled(int) bool {
	return true
}

func (logger *panicLogger) Sync(context.Context) error {
	return nil
}

func TestValidateIdentifier(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateIdentifier("outbox_events"))
	require.NoError(t, validateIdentifier("tenant_01"))

	invalid := []string{
		"",
		"123table",
		"outbox-events",
		"public.outbox",
		`outbox"; DROP TABLE users; --`,
		"outbox events",
	}

	for _, candidate := range invalid {
		require.Error(t, validateIdentifier(candidate), candidate)
	}

	tooLong := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	require.Len(t, tooLong, 64)
	require.Error(t, validateIdentifier(tooLong))
}

func TestValidateIdentifierPath(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateIdentifierPath("public.outbox_events"))
	require.NoError(t, validateIdentifierPath("tenant_01.outbox_events"))

	require.Error(t, validateIdentifierPath("public."))
	require.Error(t, validateIdentifierPath(`public."outbox"`))
	require.Error(t, validateIdentifierPath("public.outbox-events"))
}

func TestQuoteIdentifierFunctions(t *testing.T) {
	t.Parallel()

	require.Equal(t, `"outbox_events"`, quoteIdentifier("outbox_events"))
	require.Equal(t, `"a""b"`, quoteIdentifier(`a"b`))
	require.Equal(t, `"public"."outbox_events"`, quoteIdentifierPath("public.outbox_events"))
	require.Equal(t, `"public"."out""box"`, quoteIdentifierPath(`public.out"box`))
}

func TestSplitStuckEventsAndApplyState(t *testing.T) {
	t.Parallel()

	retryID := uuid.New()
	exhaustedID := uuid.New()

	events := []*outbox.OutboxEvent{
		{ID: retryID, Attempts: 1, Status: outbox.OutboxStatusProcessing},
		{ID: exhaustedID, Attempts: 2, Status: outbox.OutboxStatusProcessing},
		nil,
	}

	retryEvents, exhaustedIDs := splitStuckEvents(events, 3)
	require.Len(t, retryEvents, 1)
	require.Equal(t, retryID, retryEvents[0].ID)
	require.Equal(t, []uuid.UUID{exhaustedID}, exhaustedIDs)

	now := time.Now().UTC()
	applyStuckReprocessingState(retryEvents, now)
	require.Equal(t, 2, retryEvents[0].Attempts)
	require.Equal(t, outbox.OutboxStatusProcessing, retryEvents[0].Status)
	require.Equal(t, now, retryEvents[0].UpdatedAt)
}

func TestNewRepository_Validation(t *testing.T) {
	t.Parallel()

	repo, err := NewRepository(nil, noopTenantResolver{}, noopTenantDiscoverer{})
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrConnectionRequired)

	client := &libPostgres.Client{}

	repo, err = NewRepository(client, nil, noopTenantDiscoverer{})
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrTenantResolverRequired)

	repo, err = NewRepository(client, noopTenantResolver{}, nil)
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrTenantDiscovererRequired)

	repo, err = NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{}, WithTableName("bad-table"))
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrInvalidIdentifier)

	repo, err = NewRepository(client, noopTenantResolver{}, noopTenantDiscoverer{}, WithTenantColumn("tenant-id"))
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrInvalidIdentifier)
}

func TestQuoteIdentifier_StripsNullByte(t *testing.T) {
	t.Parallel()

	quoted := quoteIdentifier("tenant\x00_id")
	require.Equal(t, `"tenant_id"`, quoted)
}

func TestRepository_MarkFailedValidation(t *testing.T) {
	t.Parallel()

	repo := &Repository{
		client:             &libPostgres.Client{},
		tenantResolver:     noopTenantResolver{},
		tenantDiscoverer:   noopTenantDiscoverer{},
		tableName:          "outbox_events",
		transactionTimeout: time.Second,
	}

	err := repo.MarkFailed(context.Background(), uuid.Nil, "failed", 3)
	require.ErrorIs(t, err, ErrIDRequired)

	err = repo.MarkFailed(context.Background(), uuid.New(), "failed", 0)
	require.ErrorIs(t, err, ErrMaxAttemptsMustBePositive)
}

func TestRepository_ListPendingByTypeValidation(t *testing.T) {
	t.Parallel()

	repo := &Repository{
		client:             &libPostgres.Client{},
		tenantResolver:     noopTenantResolver{},
		tenantDiscoverer:   noopTenantDiscoverer{},
		tableName:          "outbox_events",
		transactionTimeout: time.Second,
	}

	_, err := repo.ListPendingByType(context.Background(), "   ", 1)
	require.ErrorIs(t, err, ErrEventTypeRequired)
}

type resultWithRows struct {
	rows int64
	err  error
}

func (result resultWithRows) LastInsertId() (int64, error) {
	return 0, nil
}

func (result resultWithRows) RowsAffected() (int64, error) {
	if result.err != nil {
		return 0, result.err
	}

	return result.rows, nil
}

func TestEnsureRowsAffected(t *testing.T) {
	t.Parallel()

	err := ensureRowsAffected(nil)
	require.ErrorIs(t, err, ErrStateTransitionConflict)

	err = ensureRowsAffected(resultWithRows{err: errors.New("rows failure")})
	require.ErrorContains(t, err, "rows affected")

	err = ensureRowsAffected(resultWithRows{rows: 0})
	require.ErrorIs(t, err, ErrStateTransitionConflict)

	err = ensureRowsAffected(resultWithRows{rows: 1})
	require.NoError(t, err)
}

func TestEnsureRowsAffectedExact(t *testing.T) {
	t.Parallel()

	err := ensureRowsAffectedExact(nil, 1)
	require.ErrorIs(t, err, ErrStateTransitionConflict)

	err = ensureRowsAffectedExact(resultWithRows{err: errors.New("rows failure")}, 1)
	require.ErrorContains(t, err, "rows affected")

	err = ensureRowsAffectedExact(resultWithRows{rows: 0}, 1)
	require.ErrorIs(t, err, ErrStateTransitionConflict)

	err = ensureRowsAffectedExact(resultWithRows{rows: 1}, 2)
	require.ErrorIs(t, err, ErrStateTransitionConflict)

	err = ensureRowsAffectedExact(resultWithRows{rows: 2}, 2)
	require.NoError(t, err)
}

func TestValidateCreateEvent(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()

	valid := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "payment.created",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	require.NoError(t, validateCreateEvent(valid))

	err := validateCreateEvent(nil)
	require.ErrorIs(t, err, outbox.ErrOutboxEventRequired)

	err = validateCreateEvent(&outbox.OutboxEvent{AggregateID: uuid.New(), EventType: "a", Payload: []byte(`{"ok":true}`)})
	require.ErrorIs(t, err, ErrIDRequired)

	err = validateCreateEvent(&outbox.OutboxEvent{ID: uuid.New(), AggregateID: uuid.New(), EventType: "   ", Payload: []byte(`{"ok":true}`)})
	require.ErrorIs(t, err, ErrEventTypeRequired)

	err = validateCreateEvent(&outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte(`{"ok":true}`)})
	require.ErrorIs(t, err, ErrAggregateIDRequired)

	err = validateCreateEvent(&outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", AggregateID: uuid.New()})
	require.ErrorIs(t, err, outbox.ErrOutboxEventPayloadRequired)

	err = validateCreateEvent(&outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", AggregateID: uuid.New(), Payload: []byte("not-json")})
	require.ErrorIs(t, err, outbox.ErrOutboxEventPayloadNotJSON)

	oversizedPayload := make([]byte, outbox.DefaultMaxPayloadBytes+1)
	err = validateCreateEvent(&outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", AggregateID: uuid.New(), Payload: oversizedPayload})
	require.ErrorIs(t, err, outbox.ErrOutboxEventPayloadTooLarge)
}

func TestRepository_TenantIDFromContext(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true}
	tenantID, err := repo.tenantIDFromContext(context.Background())
	require.Empty(t, tenantID)
	require.ErrorIs(t, err, outbox.ErrTenantIDRequired)

	repo.requireTenant = false
	tenantID, err = repo.tenantIDFromContext(context.Background())
	require.NoError(t, err)
	require.Empty(t, tenantID)

	ctx := outbox.ContextWithTenantID(context.Background(), "tenant-a")
	tenantID, err = repo.tenantIDFromContext(ctx)
	require.NoError(t, err)
	require.Equal(t, "tenant-a", tenantID)

	ctx = outbox.ContextWithTenantID(context.Background(), "_invalid")
	tenantID, err = repo.tenantIDFromContext(ctx)
	require.Empty(t, tenantID)
	require.ErrorIs(t, err, outbox.ErrInvalidTenantID)
}

func TestNewRepository_WithTypedNilLoggerFallsBackToNop(t *testing.T) {
	t.Parallel()

	var logger *panicLogger

	repo, err := NewRepository(
		&libPostgres.Client{},
		noopTenantResolver{},
		noopTenantDiscoverer{},
		WithLogger(logger),
	)
	require.NoError(t, err)
	require.NotNil(t, repo)
	require.NotNil(t, repo.logger)
}

func TestNewRepository_PropagatesResolverTenantRequirement(t *testing.T) {
	t.Parallel()

	repo, err := NewRepository(
		&libPostgres.Client{},
		requireTenantResolver{},
		noopTenantDiscoverer{},
	)
	require.NoError(t, err)

	tenantID, tenantErr := repo.tenantIDFromContext(context.Background())
	require.Empty(t, tenantID)
	require.ErrorIs(t, tenantErr, outbox.ErrTenantIDRequired)
}

func TestNormalizedCreateValues_EnforcesInitialLifecycleInvariants(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	publishedAt := now.Add(-time.Minute)

	values, err := normalizedCreateValues(&outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "payment.created",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
		Status:      outbox.OutboxStatusPublished,
		Attempts:    7,
		PublishedAt: &publishedAt,
		LastError:   "internal details",
		CreatedAt:   now,
		UpdatedAt:   now.Add(-time.Hour),
	}, now)
	require.NoError(t, err)

	require.Equal(t, outbox.OutboxStatusPending, values.status)
	require.Equal(t, 0, values.attempts)
	require.Nil(t, values.publishedAt)
	require.Empty(t, values.lastError)
	require.Equal(t, now, values.createdAt)
	require.Equal(t, now, values.updatedAt)
}

func TestNormalizedCreateValues_TrimsEventType(t *testing.T) {
	t.Parallel()

	values, err := normalizedCreateValues(&outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "  payment.created  ",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
	}, time.Now().UTC())
	require.NoError(t, err)

	require.Equal(t, "payment.created", values.eventType)
}

func TestNormalizedCreateValues_NilEventReturnsError(t *testing.T) {
	t.Parallel()

	values, err := normalizedCreateValues(nil, time.Now().UTC())
	require.ErrorIs(t, err, outbox.ErrOutboxEventRequired)
	require.Zero(t, values)
}

func TestRepository_CreateManyWithTx_Validation(t *testing.T) {
	t.Parallel()

	initialized, err := NewRepository(
		&libPostgres.Client{},
		noopTenantResolver{},
		noopTenantDiscoverer{},
	)
	require.NoError(t, err)

	valid := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "payment.created",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
	}

	tests := []struct {
		name       string
		repo       *Repository
		events     []*outbox.OutboxEvent
		wantErr    error
		wantLen    int
		wantNonNil bool
	}{
		{
			name:    "uninitialized repository takes precedence",
			repo:    &Repository{},
			events:  []*outbox.OutboxEvent{valid},
			wantErr: ErrRepositoryNotInitialized,
		},
		{
			name:       "nil batch is an empty no-op",
			repo:       initialized,
			wantNonNil: true,
		},
		{
			name:       "empty batch is a no-op",
			repo:       initialized,
			events:     []*outbox.OutboxEvent{},
			wantNonNil: true,
		},
		{
			name:    "nil event uses create validation",
			repo:    initialized,
			events:  []*outbox.OutboxEvent{valid, nil},
			wantErr: outbox.ErrOutboxEventRequired,
		},
		{
			name: "invalid event uses create validation",
			repo: initialized,
			events: []*outbox.OutboxEvent{
				valid,
				{EventType: "payment.invalid", AggregateID: uuid.New(), Payload: []byte(`{"ok":true}`)},
			},
			wantErr: ErrIDRequired,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			created, createErr := tt.repo.CreateManyWithTx(context.Background(), nil, tt.events)
			if tt.wantErr != nil {
				require.ErrorIs(t, createErr, tt.wantErr)
				require.Nil(t, created)
				return
			}

			require.NoError(t, createErr)
			require.Len(t, created, tt.wantLen)
			if tt.wantNonNil {
				require.NotNil(t, created)
			}
		})
	}
}

func TestRepository_CreateManyWithTx_UsesSingleInsertAndPreservesInputOrder(t *testing.T) {
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
	events := []*outbox.OutboxEvent{
		{
			ID:          uuid.New(),
			EventType:   "  payment.first  ",
			AggregateID: uuid.New(),
			Payload:     []byte(`{"sequence":1}`),
			Status:      outbox.OutboxStatusPublished,
			Attempts:    4,
			LastError:   "must not persist",
			CreatedAt:   now,
			UpdatedAt:   now.Add(-time.Minute),
		},
		{
			ID:          uuid.New(),
			EventType:   "payment.second",
			AggregateID: uuid.New(),
			Payload:     []byte(`{"sequence":2}`),
			Status:      outbox.OutboxStatusFailed,
			Attempts:    7,
			LastError:   "must not persist either",
			CreatedAt:   now.Add(time.Second),
			UpdatedAt:   now.Add(2 * time.Second),
		},
	}

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	mock.ExpectQuery(`INSERT INTO "outbox_events"`).
		WithArgs(
			events[0].ID, "payment.first", events[0].AggregateID, events[0].Payload,
			outbox.OutboxStatusPending, 0, nil, "", events[0].CreatedAt, events[0].CreatedAt,
			events[1].ID, events[1].EventType, events[1].AggregateID, events[1].Payload,
			outbox.OutboxStatusPending, 0, nil, "", events[1].CreatedAt, events[1].UpdatedAt,
		).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "event_type", "aggregate_id", "payload", "status", "attempts",
			"published_at", "last_error", "created_at", "updated_at",
		}).
			AddRow(
				events[1].ID, events[1].EventType, events[1].AggregateID, events[1].Payload,
				outbox.OutboxStatusPending, 0, nil, "", events[1].CreatedAt, events[1].UpdatedAt,
			).
			AddRow(
				events[0].ID, "payment.first", events[0].AggregateID, events[0].Payload,
				outbox.OutboxStatusPending, 0, nil, "", events[0].CreatedAt, events[0].CreatedAt,
			))

	created, err := repo.CreateManyWithTx(context.Background(), tx, events)
	require.NoError(t, err)
	require.Len(t, created, len(events))
	require.Equal(t, events[0].ID, created[0].ID)
	require.Equal(t, events[1].ID, created[1].ID)
	require.Equal(t, "payment.first", created[0].EventType)
	require.Equal(t, outbox.OutboxStatusPending, created[0].Status)
	require.Zero(t, created[0].Attempts)
	require.Nil(t, created[0].PublishedAt)
	require.Empty(t, created[0].LastError)
	require.Equal(t, events[0].CreatedAt, created[0].UpdatedAt)

	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepository_CreateIdempotentWithTx_Validation(t *testing.T) {
	t.Parallel()

	validEvent := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "payment.created",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
	}

	uninitialized := &Repository{}
	_, err := uninitialized.CreateIdempotentWithTx(context.Background(), nil, validEvent)
	require.ErrorIs(t, err, ErrRepositoryNotInitialized)

	repo := &Repository{
		client:             &libPostgres.Client{},
		tenantResolver:     noopTenantResolver{},
		tenantDiscoverer:   noopTenantDiscoverer{},
		tableName:          "outbox_events",
		transactionTimeout: time.Second,
	}

	_, err = repo.CreateIdempotentWithTx(context.Background(), nil, nil)
	require.ErrorIs(t, err, outbox.ErrOutboxEventRequired)

	_, err = repo.CreateIdempotentWithTx(context.Background(), nil, &outbox.OutboxEvent{
		EventType:   "payment.created",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
	})
	require.ErrorIs(t, err, ErrIDRequired)

	_, err = repo.CreateIdempotentWithTx(context.Background(), nil, &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "   ",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
	})
	require.ErrorIs(t, err, ErrEventTypeRequired)

	_, err = repo.CreateIdempotentWithTx(context.Background(), nil, &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "payment.created",
		AggregateID: uuid.New(),
		Payload:     []byte("not-json"),
	})
	require.ErrorIs(t, err, outbox.ErrOutboxEventPayloadNotJSON)
}

func TestRepository_ImplementsIdempotentWriter(t *testing.T) {
	t.Parallel()

	require.Implements(t, (*outbox.IdempotentWriter)(nil), (*Repository)(nil))
}

func TestRepository_RequiresTenant(t *testing.T) {
	t.Parallel()

	require.True(t, (*Repository)(nil).RequiresTenant())
	require.True(t, (&Repository{requireTenant: true}).RequiresTenant())
	require.True(t, (&Repository{tenantColumn: "tenant_id"}).RequiresTenant())
	require.False(t, (&Repository{}).RequiresTenant())
}
