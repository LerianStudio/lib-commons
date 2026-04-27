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
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
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

type integrationMongoSuite struct {
	ctx    context.Context
	client *libMongo.Client
}

type integrationTenantDatabaseResolver struct {
	tenants   []string
	databases map[string]*mongodriver.Database
}

func (resolver *integrationTenantDatabaseResolver) ListTenants(context.Context, string) ([]string, error) {
	return append([]string(nil), resolver.tenants...), nil
}

func (resolver *integrationTenantDatabaseResolver) DatabaseForTenant(_ context.Context, tenantID string, _ string) (*mongodriver.Database, error) {
	database := resolver.databases[tenantID]
	if database == nil {
		return nil, ErrTenantDatabaseRequired
	}

	return database, nil
}

func setupMongoContainer(t *testing.T) (string, func()) {
	t.Helper()

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

		container, err := tcmongo.Run(ctx,
			"mongo:7",
			testcontainers.WithWaitStrategy(
				wait.ForLog("Waiting for connections").WithStartupTimeout(30*time.Second),
			),
		)
		if err != nil {
			cancel()
			lastErr = err
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)

			continue
		}

		uri, err := container.ConnectionString(ctx)
		cancel()
		if err != nil {
			lastErr = err
			termCtx, termCancel := context.WithTimeout(context.Background(), 30*time.Second)
			termErr := container.Terminate(termCtx)
			termCancel()
			require.NoError(t, termErr, "failed to terminate MongoDB container after connection string error")
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)

			continue
		}

		return uri, func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer closeCancel()

			require.NoError(t, container.Terminate(closeCtx))
		}
	}

	require.NoError(t, lastErr)
	return "", nil
}

func newIntegrationRepoFixture(t *testing.T, repoOpts ...Option) *integrationRepoFixture {
	t.Helper()

	uri, cleanup := setupMongoContainer(t)
	t.Cleanup(cleanup)

	setupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := libMongo.NewClient(setupCtx, libMongo.Config{
		URI:      uri,
		Database: "outbox_integration_db",
		Logger:   libLog.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		require.NoError(t, client.Close(cleanupCtx))
	})

	return newIntegrationRepoFixtureFromClient(t, context.Background(), client, repoOpts...)
}

func newIntegrationRepoFixtureFromClient(t *testing.T, ctx context.Context, client *libMongo.Client, repoOpts ...Option) *integrationRepoFixture {
	t.Helper()

	collectionName := "outbox_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	indexCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo, err := NewRepositoryWithContext(indexCtx, client, append([]Option{WithLogger(libLog.NewNop()), WithCollectionName(collectionName)}, repoOpts...)...)
	require.NoError(t, err)

	return &integrationRepoFixture{
		ctx:            ctx,
		client:         client,
		repo:           repo,
		collectionName: collectionName,
		tenantCtx:      outbox.ContextWithTenantID(ctx, "tenant-a"),
	}
}

func newIntegrationMongoSuite(t *testing.T) *integrationMongoSuite {
	t.Helper()

	uri, cleanup := setupMongoContainer(t)
	t.Cleanup(cleanup)

	setupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := libMongo.NewClient(setupCtx, libMongo.Config{
		URI:      uri,
		Database: "outbox_integration_db",
		Logger:   libLog.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		require.NoError(t, client.Close(cleanupCtx))
	})

	return &integrationMongoSuite{ctx: context.Background(), client: client}
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

func TestIntegration_Repository_CreateListAndMarkPublished(t *testing.T) {
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

func TestIntegration_Repository_ContractSuite(t *testing.T) {
	suite := newIntegrationMongoSuite(t)

	outboxtest.Run(t, func(t *testing.T) outbox.OutboxRepository {
		t.Helper()
		return newIntegrationRepoFixtureFromClient(t, suite.ctx, suite.client).repo
	})
}

func TestIntegration_Repository_CustomTenantFieldContractSuite(t *testing.T) {
	suite := newIntegrationMongoSuite(t)

	outboxtest.Run(t, func(t *testing.T) outbox.OutboxRepository {
		t.Helper()
		return newIntegrationRepoFixtureFromClient(t, suite.ctx, suite.client, WithTenantField("scope")).repo
	})
}

func TestIntegration_Repository_ResetForRetry(t *testing.T) {
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

func TestIntegration_Repository_ResetStuckProcessing(t *testing.T) {
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

func TestIntegration_Repository_TenantIsolation(t *testing.T) {
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

func TestIntegration_Repository_ListTenantsRejectsInvalidTenantID(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	database, err := fx.client.Database(fx.ctx)
	require.NoError(t, err)

	_, err = database.Collection(fx.collectionName).InsertOne(fx.ctx, bson.M{
		"status":            outbox.OutboxStatusPending,
		fx.repo.tenantField: "_invalid",
	})
	require.NoError(t, err)

	tenants, err := fx.repo.ListTenants(fx.ctx)
	require.Nil(t, tenants)
	require.ErrorIs(t, err, outbox.ErrInvalidTenantID)
}

func TestIntegration_Repository_DispatcherLifecycleDefaultScope(t *testing.T) {
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

func TestIntegration_Repository_UsesTenantScopedMongoDatabaseFromContext(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	driverClient, err := fx.client.Client(fx.ctx)
	require.NoError(t, err)

	tenantDB := driverClient.Database("tenant_outbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16])
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		require.NoError(t, tenantDB.Drop(cleanupCtx))
	})

	tenantCtx := outbox.ContextWithTenantID(fx.ctx, "tenant-a")
	tenantCtx = tmcore.ContextWithMB(tenantCtx, tenantDB)

	created := createFixtureEventForTenant(t, fx, "tenant-a", "payment.tenant-db")
	tenantEvent, err := outbox.NewOutboxEvent(tenantCtx, "payment.tenant-db.ctx", uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)
	createdViaTenantDB, err := fx.repo.Create(tenantCtx, tenantEvent)
	require.NoError(t, err)
	require.NotNil(t, createdViaTenantDB)

	defaultDB, err := fx.client.Database(fx.ctx)
	require.NoError(t, err)

	defaultCount, err := defaultDB.Collection(fx.collectionName).CountDocuments(fx.ctx, bson.M{"id": created.ID.String()})
	require.NoError(t, err)
	require.Equal(t, int64(1), defaultCount)

	defaultTenantScopedCount, err := defaultDB.Collection(fx.collectionName).CountDocuments(fx.ctx, bson.M{"id": createdViaTenantDB.ID.String()})
	require.NoError(t, err)
	require.Equal(t, int64(0), defaultTenantScopedCount)

	tenantDefaultCount, err := tenantDB.Collection(fx.collectionName).CountDocuments(fx.ctx, bson.M{"id": created.ID.String()})
	require.NoError(t, err)
	require.Equal(t, int64(0), tenantDefaultCount)

	tenantCount, err := tenantDB.Collection(fx.collectionName).CountDocuments(fx.ctx, bson.M{"id": createdViaTenantDB.ID.String()})
	require.NoError(t, err)
	require.Equal(t, int64(1), tenantCount)

	_, err = fx.repo.GetByID(tenantCtx, created.ID)
	require.Error(t, err)
	require.ErrorIs(t, err, mongodriver.ErrNoDocuments)

	defaultCtx := outbox.ContextWithTenantID(fx.ctx, "tenant-a")
	_, err = fx.repo.GetByID(defaultCtx, createdViaTenantDB.ID)
	require.Error(t, err)
	require.ErrorIs(t, err, mongodriver.ErrNoDocuments)
}

func TestIntegration_Repository_UsesModuleScopedMongoDatabaseFromContext(t *testing.T) {
	fx := newIntegrationRepoFixture(t, WithModule("payments"))
	driverClient, err := fx.client.Client(fx.ctx)
	require.NoError(t, err)

	moduleDB := driverClient.Database("tenant_module_outbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16])
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		require.NoError(t, moduleDB.Drop(cleanupCtx))
	})

	ctx := outbox.ContextWithTenantID(fx.ctx, "tenant-a")
	event, err := outbox.NewOutboxEvent(ctx, "payment.module-db.missing", uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)
	_, err = fx.repo.Create(ctx, event)
	require.ErrorIs(t, err, ErrTenantDatabaseRequired)

	moduleCtx := tmcore.ContextWithMB(ctx, moduleDB, "payments")
	moduleEvent, err := outbox.NewOutboxEvent(moduleCtx, "payment.module-db", uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)
	created, err := fx.repo.Create(moduleCtx, moduleEvent)
	require.NoError(t, err)
	require.NotNil(t, created)

	moduleCount, err := moduleDB.Collection(fx.collectionName).CountDocuments(fx.ctx, bson.M{"id": created.ID.String()})
	require.NoError(t, err)
	require.Equal(t, int64(1), moduleCount)
}

func TestIntegration_Repository_DispatcherDrainsTenantDatabaseWithResolver(t *testing.T) {
	suite := newIntegrationMongoSuite(t)
	driverClient, err := suite.client.Client(suite.ctx)
	require.NoError(t, err)

	tenantDB := driverClient.Database("tenant_resolver_outbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16])
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		require.NoError(t, tenantDB.Drop(cleanupCtx))
	})

	resolver := &integrationTenantDatabaseResolver{
		tenants: []string{"tenant-a"},
		databases: map[string]*mongodriver.Database{
			"tenant-a": tenantDB,
		},
	}
	fx := newIntegrationRepoFixtureFromClient(t, suite.ctx, suite.client, WithModule("payments"), WithTenantDatabaseResolver(resolver))

	tenantCtx := outbox.ContextWithTenantID(suite.ctx, "tenant-a")
	tenantCtx = tmcore.ContextWithMB(tenantCtx, tenantDB, "payments")
	event, err := outbox.NewOutboxEvent(tenantCtx, "payment.tenant-resolver", uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)
	created, err := fx.repo.Create(tenantCtx, event)
	require.NoError(t, err)

	handlers := outbox.NewHandlerRegistry()
	var handled atomic.Bool
	handledCh := make(chan struct{})
	runCtx, cancel := context.WithTimeout(suite.ctx, 10*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, handlers.Register("payment.tenant-resolver", func(handlerCtx context.Context, event *outbox.OutboxEvent) error {
		tenantID, ok := outbox.TenantIDFromContext(handlerCtx)
		require.True(t, ok)
		require.Equal(t, "tenant-a", tenantID)
		require.Equal(t, created.ID, event.ID)
		if handled.CompareAndSwap(false, true) {
			close(handledCh)
		}

		return nil
	}))

	dispatcher, err := outbox.NewDispatcher(fx.repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), outbox.WithBatchSize(10), outbox.WithPublishMaxAttempts(1), outbox.WithDispatchInterval(time.Hour))
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		runDone <- dispatcher.RunContext(runCtx, nil)
	}()

	select {
	case <-handledCh:
	case <-runCtx.Done():
		require.NoError(t, runCtx.Err())
	}

	require.Eventually(t, func() bool {
		stored, getErr := fx.repo.GetByID(tenantCtx, created.ID)
		return getErr == nil && stored.Status == outbox.OutboxStatusPublished
	}, 5*time.Second, 50*time.Millisecond)

	dispatcher.Stop()
	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.Fail(t, "dispatcher did not stop within timeout")
	}
}
