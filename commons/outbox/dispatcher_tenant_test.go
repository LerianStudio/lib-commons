//go:build unit

package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestDispatcher_DispatchAcrossTenantsProcessesEachTenant(t *testing.T) {
	t.Parallel()

	tenantA := "tenant-a"
	tenantB := "tenant-b"
	eventA := uuid.New()
	eventB := uuid.New()

	repo := &fakeRepo{
		tenants: []string{tenantA, tenantB},
		pendingByTenant: map[string][]*OutboxEvent{
			tenantA: {{ID: eventA, EventType: "payment.created", Payload: []byte("a")}},
			tenantB: {{ID: eventB, EventType: "payment.created", Payload: []byte("b")}},
		},
	}

	handlers := NewHandlerRegistry()
	handledTenants := make(map[string]bool)
	require.NoError(t, handlers.Register("payment.created", func(ctx context.Context, _ *OutboxEvent) error {
		tenantID, ok := TenantIDFromContext(ctx)
		require.True(t, ok)
		handledTenants[tenantID] = true

		return nil
	}))

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), WithPublishMaxAttempts(1))
	require.NoError(t, err)

	dispatcher.dispatchAcrossTenants(context.Background())

	require.True(t, handledTenants[tenantA])
	require.True(t, handledTenants[tenantB])
	require.ElementsMatch(t, []uuid.UUID{eventA, eventB}, repo.markedPub)
}

func TestDispatcher_DispatchAcrossTenantsRoundRobinStartingTenant(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{
		tenants: []string{"tenant-a", "tenant-b", "tenant-c"},
		pendingByTenant: map[string][]*OutboxEvent{
			"tenant-a": {},
			"tenant-b": {},
			"tenant-c": {},
		},
	}

	dispatcher, err := NewDispatcher(repo, NewHandlerRegistry(), nil, noop.NewTracerProvider().Tracer("test"))
	require.NoError(t, err)

	dispatcher.dispatchAcrossTenants(context.Background())
	dispatcher.dispatchAcrossTenants(context.Background())

	order := repo.listPendingTenantOrder()
	require.Len(t, order, 6)
	require.Equal(t, "tenant-a", order[0])
	require.Equal(t, "tenant-b", order[3])
}

func TestDispatcher_DispatchAcrossTenants_StopsAfterContextCancelBetweenTenants(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	repo := &fakeRepo{
		tenants: []string{"tenant-a", "tenant-b"},
		pendingByTenant: map[string][]*OutboxEvent{
			"tenant-a": {{ID: uuid.New(), EventType: "payment.created", Payload: []byte("a")}},
			"tenant-b": {{ID: uuid.New(), EventType: "payment.created", Payload: []byte("b")}},
		},
	}

	handlers := NewHandlerRegistry()
	handledTenants := make(map[string]bool)
	require.NoError(t, handlers.Register("payment.created", func(handlerCtx context.Context, _ *OutboxEvent) error {
		tenantID, ok := TenantIDFromContext(handlerCtx)
		require.True(t, ok)
		handledTenants[tenantID] = true
		cancel()

		return nil
	}))

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), WithPublishMaxAttempts(1))
	require.NoError(t, err)

	dispatcher.dispatchAcrossTenants(ctx)

	require.True(t, handledTenants["tenant-a"])
	require.False(t, handledTenants["tenant-b"])
}

func TestDispatcher_DispatchAcrossTenantsEmptyList(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{tenants: []string{}}
	dispatcher, err := NewDispatcher(repo, NewHandlerRegistry(), nil, noop.NewTracerProvider().Tracer("test"))
	require.NoError(t, err)

	dispatcher.dispatchAcrossTenants(context.Background())

	require.Equal(t, 0, repo.listPendingCallCount())
}

func TestDispatcher_DispatchAcrossTenantsEmptyListFallsBackWhenTenantNotRequired(t *testing.T) {
	t.Parallel()

	baseRepo := &fakeRepo{
		tenants: []string{},
		pending: []*OutboxEvent{{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}},
	}
	repo := &tenantAwareFakeRepo{fakeRepo: baseRepo, requiresTenant: false}

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return nil
	}))

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), WithPublishMaxAttempts(1))
	require.NoError(t, err)

	dispatcher.dispatchAcrossTenants(context.Background())

	require.Equal(t, 1, baseRepo.listPendingCallCount())
	require.Len(t, baseRepo.markedPub, 1)
}

func TestDispatcher_DispatchAcrossTenantsEmptyListSkipsWhenTenantRequired(t *testing.T) {
	t.Parallel()

	baseRepo := &fakeRepo{
		tenants: []string{},
		pending: []*OutboxEvent{{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}},
	}
	repo := &tenantAwareFakeRepo{fakeRepo: baseRepo, requiresTenant: true}

	dispatcher, err := NewDispatcher(repo, NewHandlerRegistry(), nil, noop.NewTracerProvider().Tracer("test"))
	require.NoError(t, err)

	dispatcher.dispatchAcrossTenants(context.Background())

	require.Equal(t, 0, baseRepo.listPendingCallCount())
	require.Empty(t, baseRepo.markedPub)
}

func TestDispatcher_HandleListPendingErrorCapsTrackedTenants(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithListPendingFailureThreshold(100),
		WithMaxTrackedListPendingFailureTenants(2),
	)
	require.NoError(t, err)

	ctx := context.Background()
	_, span := dispatcher.tracer.Start(ctx, "test.list_pending_error")

	errFailure := errors.New("list pending failed")
	dispatcher.handleListPendingError(ctx, span, "tenant-1", errFailure)
	dispatcher.handleListPendingError(ctx, span, "tenant-2", errFailure)
	dispatcher.handleListPendingError(ctx, span, "tenant-3", errFailure)

	span.End()

	require.Len(t, dispatcher.listPendingFailureCounts, 2)
	require.Equal(t, 2, dispatcher.listPendingFailureCounts[defaultTenantFailureCounterFallback])
}

func TestDispatcher_BoundedTenantMetricKeyUsesOverflowLabel(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{
		cfg: DispatcherConfig{
			IncludeTenantMetrics:      true,
			MaxTenantMetricDimensions: 2,
		},
		tenantMetricKeys: make(map[string]struct{}),
	}

	require.Equal(t, "tenant-1", dispatcher.boundedTenantMetricKey("tenant-1"))
	require.Equal(t, "tenant-2", dispatcher.boundedTenantMetricKey("tenant-2"))
	require.Equal(t, overflowTenantMetricLabel, dispatcher.boundedTenantMetricKey("tenant-3"))
	require.Equal(t, 2, len(dispatcher.tenantMetricKeys))
}

func TestDispatcher_HandlePublishError_LogsMarkInvalidFailure(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{markInvalidErr: errors.New("mark invalid failed")}
	handlers := NewHandlerRegistry()
	nonRetryable := errors.New("non-retryable")

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithRetryClassifier(RetryClassifierFunc(func(err error) bool {
			return errors.Is(err, nonRetryable)
		})),
	)
	require.NoError(t, err)

	dispatcher.handlePublishError(
		context.Background(),
		dispatcher.logger,
		&OutboxEvent{ID: uuid.New()},
		nonRetryable,
	)

	require.Empty(t, repo.markedInv)
}

func TestDispatcher_HandlePublishError_LogsMarkFailedFailure(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{markFailedErr: errors.New("mark failed failed")}
	handlers := NewHandlerRegistry()

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"))
	require.NoError(t, err)

	dispatcher.handlePublishError(
		context.Background(),
		dispatcher.logger,
		&OutboxEvent{ID: uuid.New()},
		errors.New("retryable"),
	)

	require.Empty(t, repo.markedFail)
}

func TestDispatcher_DispatchOnce_EmptyPayloadMarksFailed(t *testing.T) {
	t.Parallel()

	eventID := uuid.New()
	repo := &fakeRepo{pending: []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: nil}}}
	handlers := NewHandlerRegistry()

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), WithPublishMaxAttempts(1))
	require.NoError(t, err)

	result := dispatcher.DispatchOnceResult(context.Background())

	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, result.Failed)
	require.Equal(t, []uuid.UUID{eventID}, repo.markedFail)
	require.Empty(t, repo.markedPub)
}

func TestDispatcher_DispatchAcrossTenants_ListTenantsErrorDoesNotDispatch(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{tenantsErr: errors.New("list tenants failed")}
	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"))
	require.NoError(t, err)

	dispatcher.dispatchAcrossTenants(context.Background())

	require.Equal(t, 0, repo.listPendingCallCount())
	require.Empty(t, repo.markedPub)
}

func TestNonEmptyTenants_TrimWhitespaceEntries(t *testing.T) {
	t.Parallel()

	tenants := nonEmptyTenants([]string{"tenant-a", "   ", "\ttenant-b\n", "", "tenant-c"})
	require.Equal(t, []string{"tenant-a", "tenant-b", "tenant-c"}, tenants)
}

func TestDispatcher_ClearListPendingFailureCount_ResetsFallbackForOverflowTenant(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithMaxTrackedListPendingFailureTenants(2),
		WithListPendingFailureThreshold(100),
	)
	require.NoError(t, err)

	ctx := context.Background()
	_, span := dispatcher.tracer.Start(ctx, "test.overflow_reset")
	errList := errors.New("list pending failed")

	dispatcher.handleListPendingError(ctx, span, "tenant-1", errList)
	dispatcher.handleListPendingError(ctx, span, "tenant-2", errList)
	dispatcher.handleListPendingError(ctx, span, "tenant-3", errList)
	require.Equal(t, 2, dispatcher.listPendingFailureCounts[defaultTenantFailureCounterFallback])

	dispatcher.clearListPendingFailureCount("tenant-3")
	span.End()

	require.Equal(t, 0, dispatcher.listPendingFailureCounts[defaultTenantFailureCounterFallback])
}
