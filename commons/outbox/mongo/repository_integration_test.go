//go:build integration

package mongo

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	libMongo "github.com/LerianStudio/lib-commons/v5/commons/mongo"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox/outboxtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.opentelemetry.io/otel/trace/noop"
)

type integrationRepoFixture struct {
	ctx            context.Context
	client         *libMongo.Client
	repo           *Repository
	collectionName string
	tenantCtx      context.Context
}

func setupMongoContainer(t *testing.T) (string, func()) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := tcmongo.Run(ctx,
		"mongo:7",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Waiting for connections").WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	return uri, func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()

		require.NoError(t, container.Terminate(closeCtx))
	}
}

func newIntegrationRepoFixture(t *testing.T, repoOpts ...Option) *integrationRepoFixture {
	t.Helper()

	uri, cleanup := setupMongoContainer(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	client, err := libMongo.NewClient(ctx, libMongo.Config{
		URI:      uri,
		Database: "outbox_integration_db",
		Logger:   libLog.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Close(context.Background()))
	})

	collectionName := "outbox_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	repo, err := NewRepository(client, append([]Option{WithLogger(libLog.NewNop()), WithCollectionName(collectionName)}, repoOpts...)...)
	require.NoError(t, err)

	return &integrationRepoFixture{
		ctx:            ctx,
		client:         client,
		repo:           repo,
		collectionName: collectionName,
		tenantCtx:      outbox.ContextWithTenantID(ctx, "tenant-a"),
	}
}

func createFixtureEvent(t *testing.T, fx *integrationRepoFixture, eventType string) *outbox.OutboxEvent {
	t.Helper()

	return createFixtureEventForTenant(t, fx, "tenant-a", eventType)
}

func createFixtureEventForTenant(t *testing.T, fx *integrationRepoFixture, tenantID string, eventType string) *outbox.OutboxEvent {
	t.Helper()

	ctx := fx.ctx
	if tenantID != "" {
		ctx = outbox.ContextWithTenantID(ctx, tenantID)
	}

	event, err := outbox.NewOutboxEvent(ctx, eventType, uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)

	created, err := fx.repo.Create(ctx, event)
	require.NoError(t, err)

	return created
}

func updateFixtureEventState(t *testing.T, fx *integrationRepoFixture, id uuid.UUID, tenantID string, status string, attempts int, updatedAt time.Time) {
	t.Helper()

	database, err := fx.client.Database(fx.ctx)
	require.NoError(t, err)

	filter := bson.M{"id": id.String()}
	if fx.repo.tenantField != "" {
		filter[fx.repo.tenantField] = tenantID
	}

	update := bson.M{"$set": bson.M{"status": status, "attempts": attempts, "updated_at": updatedAt}}
	_, err = database.Collection(fx.collectionName).UpdateOne(fx.ctx, filter, update)
	require.NoError(t, err)
}

func TestRepository_IntegrationCreateListAndMarkPublished(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	created := createFixtureEvent(t, fx, "payment.created")
	pending, err := fx.repo.ListPending(fx.tenantCtx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, outbox.OutboxStatusProcessing, pending[0].Status)

	now := time.Now().UTC()
	require.NoError(t, fx.repo.MarkPublished(fx.tenantCtx, created.ID, now))

	published, err := fx.repo.GetByID(fx.tenantCtx, created.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusPublished, published.Status)
	require.NotNil(t, published.PublishedAt)
}

func TestRepository_IntegrationContractSuite(t *testing.T) {
	outboxtest.Run(t, func(t *testing.T) outbox.OutboxRepository {
		t.Helper()
		return newIntegrationRepoFixture(t).repo
	})
}

func TestRepository_IntegrationResetForRetry(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	event := createFixtureEvent(t, fx, "payment.failed")
	stale := time.Now().UTC().Add(-time.Hour)
	updateFixtureEventState(t, fx, event.ID, "tenant-a", outbox.OutboxStatusFailed, 1, stale)

	retried, err := fx.repo.ResetForRetry(fx.tenantCtx, 10, time.Now().UTC(), 5)
	require.NoError(t, err)
	require.Len(t, retried, 1)
	require.Equal(t, event.ID, retried[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, retried[0].Status)
}

func TestRepository_IntegrationResetStuckProcessing(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	retryEvent := createFixtureEvent(t, fx, "payment.stuck.retry")
	exhaustedEvent := createFixtureEvent(t, fx, "payment.stuck.exhausted")
	stale := time.Now().UTC().Add(-time.Hour)

	updateFixtureEventState(t, fx, retryEvent.ID, "tenant-a", outbox.OutboxStatusProcessing, 1, stale)
	updateFixtureEventState(t, fx, exhaustedEvent.ID, "tenant-a", outbox.OutboxStatusProcessing, 2, stale)

	reset, err := fx.repo.ResetStuckProcessing(fx.tenantCtx, 10, time.Now().UTC(), 3)
	require.NoError(t, err)
	require.Len(t, reset, 1)
	require.Equal(t, retryEvent.ID, reset[0].ID)
	require.Equal(t, outbox.OutboxStatusProcessing, reset[0].Status)
	require.Equal(t, 2, reset[0].Attempts)

	exhausted, err := fx.repo.GetByID(fx.tenantCtx, exhaustedEvent.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusInvalid, exhausted.Status)
	require.Equal(t, 3, exhausted.Attempts)
	require.Equal(t, "max dispatch attempts exceeded", exhausted.LastError)
}

func TestRepository_IntegrationTenantIsolation(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	tenantA := outbox.ContextWithTenantID(fx.ctx, "tenant-a")
	tenantB := outbox.ContextWithTenantID(fx.ctx, "tenant-b")

	eventA := createFixtureEventForTenant(t, fx, "tenant-a", "payment.a")
	eventB := createFixtureEventForTenant(t, fx, "tenant-b", "payment.b")

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
	require.ErrorIs(t, err, mongodriver.ErrNoDocuments)

	tenants, err := fx.repo.ListTenants(fx.ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"tenant-a", "tenant-b"}, tenants)
}

func TestRepository_IntegrationDispatcherLifecycle_DefaultScope(t *testing.T) {
	fx := newIntegrationRepoFixture(t, WithAllowEmptyTenant())
	created := createFixtureEventForTenant(t, fx, "", "payment.default-scope")

	handlers := outbox.NewHandlerRegistry()
	var handled atomic.Bool
	require.NoError(t, handlers.Register("payment.default-scope", func(_ context.Context, event *outbox.OutboxEvent) error {
		require.Equal(t, created.ID, event.ID)
		handled.Store(true)

		return nil
	}))

	dispatcher, err := outbox.NewDispatcher(fx.repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), outbox.WithBatchSize(10), outbox.WithPublishMaxAttempts(1))
	require.NoError(t, err)

	result := dispatcher.DispatchOnceResult(context.Background())
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, result.Published)
	require.True(t, handled.Load())

	stored, err := fx.repo.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, outbox.OutboxStatusPublished, stored.Status)

	tenants, err := fx.repo.ListTenants(context.Background())
	require.NoError(t, err)
	require.Empty(t, tenants)
}
