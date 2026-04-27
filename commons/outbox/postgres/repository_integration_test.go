//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox/outboxtest"
	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.opentelemetry.io/otel/trace/noop"
)

type integrationRepoFixture struct {
	ctx       context.Context
	client    *libPostgres.Client
	primaryDB *sql.DB
	repo      *Repository
	tableName string
	tenantCtx context.Context
}

func newIntegrationRepoFixture(t *testing.T) *integrationRepoFixture {
	t.Helper()

	dsn, cleanup := integrationPostgresDSN(t)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}

	return newIntegrationRepoFixtureWithDSN(t, dsn)
}

func integrationPostgresDSN(t *testing.T) (string, func()) {
	t.Helper()

	return setupOutboxPostgresContainer(t)
}

func newIntegrationRepoFixtureWithDSN(t *testing.T, dsn string) *integrationRepoFixture {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client, err := libPostgres.New(libPostgres.Config{PrimaryDSN: dsn, ReplicaDSN: dsn})
	require.NoError(t, err)

	require.NoError(t, client.Connect(ctx))
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("cleanup: client close: %v", err)
		}
	})

	primaryDB, err := client.Primary()
	require.NoError(t, err)

	tableName := "outbox_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]

	_, err = primaryDB.ExecContext(ctx, `
DO $$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'outbox_event_status') THEN
		CREATE TYPE outbox_event_status AS ENUM ('PENDING','PROCESSING','PUBLISHED','FAILED','INVALID');
	END IF;
END
$$;
`)
	require.NoError(t, err)

	_, err = primaryDB.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE %s (
	id UUID NOT NULL,
	event_type VARCHAR(255) NOT NULL,
	aggregate_id UUID NOT NULL,
	payload JSONB NOT NULL,
	status outbox_event_status NOT NULL DEFAULT 'PENDING',
	attempts INT NOT NULL DEFAULT 0,
	published_at TIMESTAMPTZ,
	last_error VARCHAR(512),
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	tenant_id TEXT NOT NULL,
	PRIMARY KEY (tenant_id, id)
);
`, quoteIdentifier(tableName)))
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := primaryDB.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", quoteIdentifier(tableName))); err != nil {
			t.Errorf("cleanup: drop table %s: %v", tableName, err)
		}
	})

	resolver, err := NewColumnResolver(
		client,
		WithColumnResolverTableName(tableName),
		WithColumnResolverTenantColumn("tenant_id"),
	)
	require.NoError(t, err)

	repo, err := NewRepository(
		client,
		resolver,
		resolver,
		WithTableName(tableName),
		WithTenantColumn("tenant_id"),
	)
	require.NoError(t, err)

	return &integrationRepoFixture{
		ctx:       ctx,
		client:    client,
		primaryDB: primaryDB,
		repo:      repo,
		tableName: tableName,
		tenantCtx: outbox.ContextWithTenantID(ctx, "tenant-a"),
	}
}

func setupOutboxPostgresContainer(t *testing.T) (string, func()) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("outbox_it"),
		tcpostgres.WithUsername("outbox"),
		tcpostgres.WithPassword("outbox"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	return dsn, func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()

		require.NoError(t, container.Terminate(closeCtx))
	}
}

func createFixtureEvent(t *testing.T, fx *integrationRepoFixture, eventType string) *outbox.OutboxEvent {
	t.Helper()

	return createFixtureEventForTenant(t, fx, "tenant-a", eventType)
}

func createFixtureEventForTenant(
	t *testing.T,
	fx *integrationRepoFixture,
	tenantID string,
	eventType string,
) *outbox.OutboxEvent {
	t.Helper()

	eventCtx := outbox.ContextWithTenantID(fx.ctx, tenantID)
	event, err := outbox.NewOutboxEvent(eventCtx, eventType, uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)

	created, err := fx.repo.Create(eventCtx, event)
	require.NoError(t, err)

	return created
}

func updateFixtureEventStateForTenant(
	t *testing.T,
	fx *integrationRepoFixture,
	id uuid.UUID,
	tenantID string,
	status string,
	attempts int,
	updatedAt time.Time,
) {
	t.Helper()

	_, err := fx.primaryDB.ExecContext(
		fx.ctx,
		fmt.Sprintf(
			"UPDATE %s SET status = $1::outbox_event_status, attempts = $2, updated_at = $3 WHERE id = $4 AND tenant_id = $5",
			quoteIdentifier(fx.tableName),
		),
		status,
		attempts,
		updatedAt,
		id,
		tenantID,
	)
	require.NoError(t, err)
}

func updateFixtureEventState(
	t *testing.T,
	fx *integrationRepoFixture,
	id uuid.UUID,
	status string,
	attempts int,
	updatedAt time.Time,
) {
	t.Helper()

	updateFixtureEventStateForTenant(t, fx, id, "tenant-a", status, attempts, updatedAt)
}

func TestIntegration_Repository_CreateListAndMarkFailed(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	created := createFixtureEvent(t, fx, "payment.created")
	require.NotNil(t, created)

	pending, err := fx.repo.ListPending(fx.tenantCtx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, outbox.OutboxStatusProcessing, pending[0].Status)

	require.NoError(t, fx.repo.MarkFailed(fx.tenantCtx, created.ID, "password=abc123", 5))

	updated, err := fx.repo.GetByID(fx.tenantCtx, created.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusFailed, updated.Status)
	require.NotContains(t, updated.LastError, "abc123")
}

func TestIntegration_Repository_ContractSuite(t *testing.T) {
	dsn, cleanup := integrationPostgresDSN(t)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}

	var current *integrationRepoFixture

	outboxtest.Run(t, func(t *testing.T) outbox.OutboxRepository {
		t.Helper()
		current = newIntegrationRepoFixtureWithDSN(t, dsn)
		return current.repo
	}, outboxtest.WithTransactionFactory(func(t *testing.T, ctx context.Context) (outbox.Tx, func()) {
		t.Helper()
		require.NotNil(t, current)

		tx, err := current.primaryDB.BeginTx(ctx, nil)
		require.NoError(t, err)

		return tx, func() {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				t.Errorf("cleanup: tx rollback: %v", err)
			}
		}
	}))
}

func TestIntegration_Repository_MarkPublished(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	event := createFixtureEvent(t, fx, "payment.published")

	now := time.Now().UTC()
	updateFixtureEventState(t, fx, event.ID, outbox.OutboxStatusProcessing, 0, now)
	require.NoError(t, fx.repo.MarkPublished(fx.tenantCtx, event.ID, now))

	published, err := fx.repo.GetByID(fx.tenantCtx, event.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusPublished, published.Status)
}

func TestIntegration_Repository_MarkInvalidRedactsSensitiveData(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	event := createFixtureEvent(t, fx, "payment.invalid")

	now := time.Now().UTC()
	updateFixtureEventState(t, fx, event.ID, outbox.OutboxStatusProcessing, 0, now)
	require.NoError(t, fx.repo.MarkInvalid(fx.tenantCtx, event.ID, "token=super-secret"))

	invalid, err := fx.repo.GetByID(fx.tenantCtx, event.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusInvalid, invalid.Status)
	require.NotContains(t, invalid.LastError, "super-secret")
}

func TestIntegration_Repository_ListPendingByType(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	target := createFixtureEvent(t, fx, "payment.priority")
	_ = createFixtureEvent(t, fx, "payment.non-priority")

	priorityEvents, err := fx.repo.ListPendingByType(fx.tenantCtx, "payment.priority", 10)
	require.NoError(t, err)
	require.Len(t, priorityEvents, 1)
	require.Equal(t, target.ID, priorityEvents[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, priorityEvents[0].Status)
}

func TestIntegration_Repository_ResetForRetry(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	event := createFixtureEvent(t, fx, "payment.failed")

	staleTime := time.Now().UTC().Add(-time.Hour)
	updateFixtureEventState(t, fx, event.ID, outbox.OutboxStatusFailed, 1, staleTime)

	retried, err := fx.repo.ResetForRetry(fx.tenantCtx, 10, time.Now().UTC(), 5)
	require.NoError(t, err)
	require.Len(t, retried, 1)
	require.Equal(t, event.ID, retried[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, retried[0].Status)
}

func TestIntegration_Repository_ResetStuckProcessing(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	retryEvent := createFixtureEvent(t, fx, "payment.stuck.retry")
	exhaustedEvent := createFixtureEvent(t, fx, "payment.stuck.exhausted")

	staleTime := time.Now().UTC().Add(-time.Hour)
	updateFixtureEventState(t, fx, retryEvent.ID, outbox.OutboxStatusProcessing, 1, staleTime)
	updateFixtureEventState(t, fx, exhaustedEvent.ID, outbox.OutboxStatusProcessing, 2, staleTime)

	resetStuck, err := fx.repo.ResetStuckProcessing(fx.tenantCtx, 10, time.Now().UTC(), 3)
	require.NoError(t, err)
	require.Len(t, resetStuck, 1)
	require.Equal(t, retryEvent.ID, resetStuck[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, resetStuck[0].Status)
	require.Equal(t, 2, resetStuck[0].Attempts)

	exhausted, err := fx.repo.GetByID(fx.tenantCtx, exhaustedEvent.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusInvalid, exhausted.Status)
	require.Equal(t, 3, exhausted.Attempts)
	require.Equal(t, "max dispatch attempts exceeded", exhausted.LastError)
}

func TestIntegration_Repository_CreateWithTx(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	tx, err := fx.primaryDB.BeginTx(fx.tenantCtx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("cleanup: tx rollback: %v", err)
		}
	})

	event, err := outbox.NewOutboxEvent(fx.tenantCtx, "payment.tx.create", uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)

	created, err := fx.repo.CreateWithTx(fx.tenantCtx, tx, event)
	require.NoError(t, err)
	require.NotNil(t, created)

	require.NoError(t, tx.Commit())

	stored, err := fx.repo.GetByID(fx.tenantCtx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, stored.ID)
}

func TestIntegration_Repository_MarkPublishedRequiresProcessingState(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	event := createFixtureEvent(t, fx, "payment.state.guard")
	err := fx.repo.MarkPublished(fx.tenantCtx, event.ID, time.Now().UTC())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrStateTransitionConflict)
}

func TestIntegration_Repository_CreateForcesPendingLifecycleInvariants(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	now := time.Now().UTC()
	publishedAt := now.Add(-time.Minute)

	created, err := fx.repo.Create(
		fx.tenantCtx,
		&outbox.OutboxEvent{
			ID:          uuid.New(),
			EventType:   "payment.invariant.override",
			AggregateID: uuid.New(),
			Payload:     []byte(`{"ok":true}`),
			Status:      outbox.OutboxStatusPublished,
			Attempts:    9,
			PublishedAt: &publishedAt,
			LastError:   "must not persist",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	)
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, outbox.OutboxStatusPending, created.Status)
	require.Equal(t, 0, created.Attempts)
	require.Nil(t, created.PublishedAt)
	require.Empty(t, created.LastError)
}

func TestIntegration_Repository_TenantIsolationBoundaries(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	tenantA := outbox.ContextWithTenantID(fx.ctx, "tenant-a")
	tenantB := outbox.ContextWithTenantID(fx.ctx, "tenant-b")

	eventA := createFixtureEventForTenant(t, fx, "tenant-a", "payment.isolation.a")
	eventB := createFixtureEventForTenant(t, fx, "tenant-b", "payment.isolation.b")

	pendingA, err := fx.repo.ListPending(tenantA, 10)
	require.NoError(t, err)
	require.Len(t, pendingA, 1)
	require.Equal(t, eventA.ID, pendingA[0].ID)

	pendingB, err := fx.repo.ListPending(tenantB, 10)
	require.NoError(t, err)
	require.Len(t, pendingB, 1)
	require.Equal(t, eventB.ID, pendingB[0].ID)

	_, err = fx.repo.GetByID(tenantA, eventB.ID)
	require.Error(t, err)
	require.ErrorIs(t, err, sql.ErrNoRows)

	err = fx.repo.MarkPublished(tenantA, eventB.ID, time.Now().UTC())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrStateTransitionConflict)

	storedB, err := fx.repo.GetByID(tenantB, eventB.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusProcessing, storedB.Status)
}

func TestIntegration_Repository_InvalidTenantIDsAreRejected(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	invalidCtx := outbox.ContextWithTenantID(fx.ctx, "_invalid")
	event, err := outbox.NewOutboxEvent(invalidCtx, "payment.invalid-tenant", uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)

	_, err = fx.repo.Create(invalidCtx, event)
	require.ErrorIs(t, err, outbox.ErrInvalidTenantID)

	now := time.Now().UTC()
	_, err = fx.primaryDB.ExecContext(
		fx.ctx,
		fmt.Sprintf(
			"INSERT INTO %s (id, event_type, aggregate_id, payload, status, attempts, created_at, updated_at, tenant_id) VALUES ($1, $2, $3, $4, $5::outbox_event_status, $6, $7, $8, $9)",
			quoteIdentifier(fx.tableName),
		),
		uuid.New(),
		"payment.invalid-discovery",
		uuid.New(),
		[]byte(`{"ok":true}`),
		outbox.OutboxStatusPending,
		0,
		now,
		now,
		"_invalid",
	)
	require.NoError(t, err)

	tenants, err := fx.repo.ListTenants(fx.ctx)
	require.Nil(t, tenants)
	require.ErrorIs(t, err, outbox.ErrInvalidTenantID)
}

func TestIntegration_Repository_MarkFailedAndInvalidRequireProcessingState(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	failedEvent := createFixtureEvent(t, fx, "payment.failed.guard")
	err := fx.repo.MarkFailed(fx.tenantCtx, failedEvent.ID, "retry error", 3)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrStateTransitionConflict)

	invalidEvent := createFixtureEvent(t, fx, "payment.invalid.guard")
	err = fx.repo.MarkInvalid(fx.tenantCtx, invalidEvent.ID, "non-retryable error")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrStateTransitionConflict)
}

func TestIntegration_Repository_DispatcherLifecyclePersistsPublishedState(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	created := createFixtureEvent(t, fx, "payment.dispatch.lifecycle")
	require.NotNil(t, created)

	handlers := outbox.NewHandlerRegistry()
	var handled atomic.Bool

	require.NoError(t, handlers.Register("payment.dispatch.lifecycle", func(_ context.Context, event *outbox.OutboxEvent) error {
		require.NotNil(t, event)
		require.Equal(t, created.ID, event.ID)
		handled.Store(true)

		return nil
	}))

	dispatcher, err := outbox.NewDispatcher(
		fx.repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		outbox.WithBatchSize(10),
		outbox.WithPublishMaxAttempts(1),
	)
	require.NoError(t, err)

	result := dispatcher.DispatchOnceResult(fx.tenantCtx)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, result.Published)
	require.Equal(t, 0, result.Failed)
	require.Equal(t, 0, result.StateUpdateFailed)
	require.True(t, handled.Load())

	stored, err := fx.repo.GetByID(fx.tenantCtx, created.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusPublished, stored.Status)
	require.NotNil(t, stored.PublishedAt)
	require.True(t, stored.UpdatedAt.After(created.UpdatedAt) || stored.UpdatedAt.Equal(created.UpdatedAt))
}

func TestIntegration_ColumnResolver_DiscoverTenants(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	_, err := fx.repo.Create(
		outbox.ContextWithTenantID(fx.ctx, "tenant-b"),
		&outbox.OutboxEvent{
			ID:          uuid.New(),
			EventType:   "payment.discover",
			AggregateID: uuid.New(),
			Payload:     []byte(`{"ok":true}`),
			Status:      outbox.OutboxStatusPending,
			Attempts:    0,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		},
	)
	require.NoError(t, err)

	resolver, err := NewColumnResolver(
		fx.client,
		WithColumnResolverTableName(fx.tableName),
		WithColumnResolverTenantColumn("tenant_id"),
	)
	require.NoError(t, err)

	tenants, err := resolver.DiscoverTenants(fx.ctx)
	require.NoError(t, err)
	require.Contains(t, tenants, "tenant-b")
}

func TestIntegration_SchemaResolver_ApplyTenantAndDiscoverTenants(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	tenantSchema := uuid.NewString()
	defaultTenant := uuid.NewString()

	_, err := fx.primaryDB.ExecContext(fx.ctx, fmt.Sprintf("CREATE SCHEMA %s", quoteIdentifier(tenantSchema)))
	require.NoError(t, err)
	_, err = fx.primaryDB.ExecContext(fx.ctx, fmt.Sprintf("CREATE TABLE %s.%s (id UUID)", quoteIdentifier(tenantSchema), quoteIdentifier(defaultOutboxTableName)))
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := fx.primaryDB.ExecContext(fx.ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(tenantSchema))); err != nil {
			t.Errorf("cleanup: drop schema %s: %v", tenantSchema, err)
		}
	})

	resolver, err := NewSchemaResolver(fx.client, WithDefaultTenantID(defaultTenant))
	require.NoError(t, err)

	tx, err := fx.primaryDB.BeginTx(fx.ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("cleanup: tx rollback: %v", err)
		}
	})

	require.NoError(t, resolver.ApplyTenant(fx.ctx, tx, tenantSchema))

	var currentSchema string
	require.NoError(t, tx.QueryRowContext(fx.ctx, "SELECT current_schema()").Scan(&currentSchema))
	require.Equal(t, tenantSchema, currentSchema)
	require.NoError(t, tx.Rollback())

	tenants, err := resolver.DiscoverTenants(fx.ctx)
	require.NoError(t, err)
	require.Contains(t, tenants, tenantSchema)
	require.NotContains(t, tenants, defaultTenant)
}
