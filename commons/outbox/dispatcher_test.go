//go:build unit

package outbox

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

type fakeRepo struct {
	mu                 sync.Mutex
	pending            []*OutboxEvent
	pendingByTenant    map[string][]*OutboxEvent
	pendingByType      map[string][]*OutboxEvent
	stuck              []*OutboxEvent
	failedForRetry     []*OutboxEvent
	markedPub          []uuid.UUID
	markPublishedCalls []uuid.UUID
	markedFail         []uuid.UUID
	markedInv          []uuid.UUID
	tenants            []string
	tenantsErr         error
	listPendingErr     error
	listPendingTypeErr error
	resetStuckErr      error
	resetForRetryErr   error
	markPublishedErr   error
	markFailedErr      error
	markInvalidErr     error
	listPendingBlocked <-chan struct{}
	blockIgnoresCtx    bool
	listPendingCalls   int32
	listPendingTenants []string
}

type tenantAwareFakeRepo struct {
	*fakeRepo
	requiresTenant bool
}

func (repo *tenantAwareFakeRepo) RequiresTenant() bool {
	if repo == nil {
		return true
	}

	return repo.requiresTenant
}

func (repo *fakeRepo) Create(context.Context, *OutboxEvent) (*OutboxEvent, error) {
	return nil, nil
}

func (repo *fakeRepo) CreateWithTx(context.Context, Tx, *OutboxEvent) (*OutboxEvent, error) {
	return nil, nil
}

func (repo *fakeRepo) ListPending(ctx context.Context, _ int) ([]*OutboxEvent, error) {
	atomic.AddInt32(&repo.listPendingCalls, 1)

	if repo.listPendingBlocked != nil {
		if repo.blockIgnoresCtx {
			<-repo.listPendingBlocked
		} else {
			select {
			case <-repo.listPendingBlocked:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	if repo.listPendingErr != nil {
		return nil, repo.listPendingErr
	}

	if repo.pendingByTenant != nil {
		tenantID, ok := TenantIDFromContext(ctx)
		if ok {
			repo.mu.Lock()
			repo.listPendingTenants = append(repo.listPendingTenants, tenantID)
			repo.mu.Unlock()

			if tenantPending, exists := repo.pendingByTenant[tenantID]; exists {
				return tenantPending, nil
			}
		}
	}

	return repo.pending, nil
}

func (repo *fakeRepo) ListPendingByType(_ context.Context, eventType string, _ int) ([]*OutboxEvent, error) {
	if repo.listPendingTypeErr != nil {
		return nil, repo.listPendingTypeErr
	}

	if repo.pendingByType != nil {
		if events, exists := repo.pendingByType[eventType]; exists {
			return events, nil
		}

		return nil, nil
	}

	result := make([]*OutboxEvent, 0)
	for _, event := range repo.pending {
		if event != nil && event.EventType == eventType {
			result = append(result, event)
		}
	}

	return result, nil
}

func (repo *fakeRepo) ListTenants(context.Context) ([]string, error) {
	if repo.tenantsErr != nil {
		return nil, repo.tenantsErr
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()

	return append([]string(nil), repo.tenants...), nil
}

func (repo *fakeRepo) listPendingCallCount() int {
	return int(atomic.LoadInt32(&repo.listPendingCalls))
}

func (repo *fakeRepo) GetByID(context.Context, uuid.UUID) (*OutboxEvent, error) { return nil, nil }

func (repo *fakeRepo) MarkPublished(_ context.Context, id uuid.UUID, _ time.Time) error {
	repo.mu.Lock()
	repo.markPublishedCalls = append(repo.markPublishedCalls, id)
	repo.mu.Unlock()

	if repo.markPublishedErr != nil {
		return repo.markPublishedErr
	}

	repo.mu.Lock()
	repo.markedPub = append(repo.markedPub, id)
	repo.mu.Unlock()

	return nil
}

func (repo *fakeRepo) MarkFailed(_ context.Context, id uuid.UUID, _ string, _ int) error {
	if repo.markFailedErr != nil {
		return repo.markFailedErr
	}

	repo.mu.Lock()
	repo.markedFail = append(repo.markedFail, id)
	repo.mu.Unlock()

	return nil
}

func (repo *fakeRepo) ListFailedForRetry(context.Context, int, time.Time, int) ([]*OutboxEvent, error) {
	return nil, nil
}

func (repo *fakeRepo) ResetForRetry(context.Context, int, time.Time, int) ([]*OutboxEvent, error) {
	if repo.resetForRetryErr != nil {
		return nil, repo.resetForRetryErr
	}

	return repo.failedForRetry, nil
}

func (repo *fakeRepo) ResetStuckProcessing(context.Context, int, time.Time, int) ([]*OutboxEvent, error) {
	if repo.resetStuckErr != nil {
		return nil, repo.resetStuckErr
	}

	return repo.stuck, nil
}

func (repo *fakeRepo) MarkInvalid(_ context.Context, id uuid.UUID, _ string) error {
	if repo.markInvalidErr != nil {
		return repo.markInvalidErr
	}

	repo.mu.Lock()
	repo.markedInv = append(repo.markedInv, id)
	repo.mu.Unlock()

	return nil
}

func (repo *fakeRepo) listPendingTenantOrder() []string {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	return append([]string(nil), repo.listPendingTenants...)
}

func TestDispatcher_DispatchOncePublishes(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()

	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok")}}

	handled := false
	require.NoError(t, handlers.Register("payment.created", func(_ context.Context, event *OutboxEvent) error {
		handled = true
		require.Equal(t, eventID, event.ID)

		return nil
	}))

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(1),
	)
	require.NoError(t, err)

	processed := dispatcher.DispatchOnce(context.Background())
	require.Equal(t, 1, processed)
	require.True(t, handled)
	require.Len(t, repo.markedPub, 1)
	require.Equal(t, eventID, repo.markedPub[0])
}

func TestDispatcher_DispatchOnceMarksInvalidOnNonRetryable(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()

	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok")}}

	nonRetryable := errors.New("non-retryable")
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return nonRetryable
	}))

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(1),
		WithRetryClassifier(RetryClassifierFunc(func(err error) bool {
			return errors.Is(err, nonRetryable)
		})),
	)
	require.NoError(t, err)

	_ = dispatcher.DispatchOnce(context.Background())
	require.Len(t, repo.markedInv, 1)
	require.Equal(t, eventID, repo.markedInv[0])
	require.Empty(t, repo.markedFail)
}

func TestDeduplicateEvents_FiltersNilAndDuplicates(t *testing.T) {
	t.Parallel()

	idA := uuid.New()
	idB := uuid.New()

	events := []*OutboxEvent{
		nil,
		{ID: idA},
		{ID: idA},
		nil,
		{ID: idB},
	}

	result := deduplicateEvents(events)
	require.Len(t, result, 2)
	require.Equal(t, idA, result[0].ID)
	require.Equal(t, idB, result[1].ID)
}

func TestDispatcher_DispatchOnceStopsOnContextCancellation(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	firstID := uuid.New()
	secondID := uuid.New()
	repo.pending = []*OutboxEvent{
		{ID: firstID, EventType: "payment.created", Payload: []byte("1")},
		{ID: secondID, EventType: "payment.created", Payload: []byte("2")},
	}

	handlers := NewHandlerRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	handled := make([]uuid.UUID, 0, 2)

	require.NoError(t, handlers.Register("payment.created", func(_ context.Context, event *OutboxEvent) error {
		handled = append(handled, event.ID)
		if event.ID == firstID {
			cancel()
		}

		return nil
	}))

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), WithPublishMaxAttempts(1))
	require.NoError(t, err)

	processed := dispatcher.DispatchOnce(ctx)
	require.Equal(t, 1, processed)
	require.Equal(t, []uuid.UUID{firstID}, handled)
	require.Equal(t, []uuid.UUID{firstID}, repo.markedPub)
}

func TestDispatcher_DispatchOnceMarkPublishedErrorDoesNotMarkFailedOrInvalid(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{markPublishedErr: errors.New("db write failed")}
	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok")}}

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return nil
	}))

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), WithPublishMaxAttempts(1))
	require.NoError(t, err)

	result := dispatcher.DispatchOnceResult(context.Background())
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, result.Published)
	require.Equal(t, 1, result.StateUpdateFailed)
	require.Equal(t, 0, result.Failed)
	require.Equal(t, []uuid.UUID{eventID}, repo.markPublishedCalls)
	require.Empty(t, repo.markedPub)
	require.Empty(t, repo.markedFail)
	require.Empty(t, repo.markedInv)
}

func TestDispatcher_DispatchOnceRetryableErrorMarksFailed(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok")}}

	handlers := NewHandlerRegistry()
	retryErr := errors.New("temporary broker outage")
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return retryErr
	}))

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(1),
	)
	require.NoError(t, err)

	processed := dispatcher.DispatchOnce(context.Background())
	require.Equal(t, 1, processed)
	require.Equal(t, []uuid.UUID{eventID}, repo.markedFail)
	require.Empty(t, repo.markedInv)
	require.Empty(t, repo.markedPub)
}

func TestDispatcher_PublishEventWithRetry_SucceedsAfterTransientError(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()
	event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}

	attempts := 0
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary failure")
		}

		return nil
	}))

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(3),
		WithPublishBackoff(time.Millisecond),
	)
	require.NoError(t, err)

	err = dispatcher.publishEventWithRetry(context.Background(), event)
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
}

func TestDispatcher_PublishEventWithRetry_StopsOnNonRetryableError(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()
	event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}

	nonRetryable := errors.New("validation failed")
	attempts := 0
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		attempts++

		return nonRetryable
	}))

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(5),
		WithPublishBackoff(time.Millisecond),
		WithRetryClassifier(RetryClassifierFunc(func(err error) bool {
			return errors.Is(err, nonRetryable)
		})),
	)
	require.NoError(t, err)

	err = dispatcher.publishEventWithRetry(context.Background(), event)
	require.Error(t, err)
	require.Equal(t, 1, attempts)
}

func TestDispatcher_PublishEventWithRetry_StopsWhenContextCancelled(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()
	event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}

	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return errors.New("temporary failure")
	}))

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(5),
		WithPublishBackoff(50*time.Millisecond),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	err = dispatcher.publishEventWithRetry(ctx, event)
	require.Error(t, err)
	require.Contains(t, err.Error(), "publish retry wait interrupted")
}

func TestNewDispatcher_ValidationErrors(t *testing.T) {
	t.Parallel()

	handlers := NewHandlerRegistry()

	dispatcher, err := NewDispatcher(nil, handlers, nil, noop.NewTracerProvider().Tracer("test"))
	require.Nil(t, dispatcher)
	require.ErrorIs(t, err, ErrOutboxRepositoryRequired)

	repo := &fakeRepo{}
	dispatcher, err = NewDispatcher(repo, nil, nil, noop.NewTracerProvider().Tracer("test"))
	require.Nil(t, dispatcher)
	require.ErrorIs(t, err, ErrHandlerRegistryRequired)
}

func TestDeduplicateEvents_EmptyInput(t *testing.T) {
	t.Parallel()

	result := deduplicateEvents(nil)
	require.Nil(t, result)
}

func TestDispatcher_DispatchOnceNilReceiver(t *testing.T) {
	t.Parallel()

	var dispatcher *Dispatcher

	require.Equal(t, 0, dispatcher.DispatchOnce(context.Background()))
}

func TestDispatcher_DispatchOnceResultNilContext(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	handlers := NewHandlerRegistry()

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"))
	require.NoError(t, err)

	result := dispatcher.DispatchOnceResult(nil)
	require.Equal(t, 0, result.Processed)
	require.Equal(t, 0, result.Published)
	require.Equal(t, 0, result.Failed)
	require.Equal(t, 0, result.StateUpdateFailed)
}

func TestDispatcher_DispatchOnceResult_ZeroValueIsSafe(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{}

	require.NotPanics(t, func() {
		result := dispatcher.DispatchOnceResult(context.Background())
		require.Equal(t, DispatchResult{}, result)
	})
}

func TestDispatcher_RunStopShutdownLifecycle(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{tenants: []string{"tenant-1"}}
	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithDispatchInterval(5*time.Millisecond),
	)
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		runDone <- dispatcher.Run(nil)
	}()

	require.Eventually(t, func() bool {
		return repo.listPendingCallCount() > 0
	}, time.Second, time.Millisecond)

	require.NoError(t, dispatcher.Shutdown(context.Background()))

	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("dispatcher run did not stop")
	}
}

func TestDispatcher_RunContext_CanRestartAfterShutdown(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{tenants: []string{"tenant-1"}}
	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithDispatchInterval(5*time.Millisecond),
	)
	require.NoError(t, err)

	runOnce := func() {
		initialCalls := repo.listPendingCallCount()

		runDone := make(chan error, 1)
		go func() {
			runDone <- dispatcher.Run(nil)
		}()

		require.Eventually(t, func() bool {
			return repo.listPendingCallCount() > initialCalls
		}, time.Second, time.Millisecond)

		require.NoError(t, dispatcher.Shutdown(context.Background()))
		require.NoError(t, <-runDone)
	}

	runOnce()
	runOnce()
}

func TestDispatcher_RunContextStopsWhenParentCancelled(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{tenants: []string{"tenant-1"}}
	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithDispatchInterval(5*time.Millisecond),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- dispatcher.RunContext(ctx, nil)
	}()

	require.Eventually(t, func() bool {
		return repo.listPendingCallCount() > 0
	}, time.Second, time.Millisecond)

	cancel()

	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("dispatcher run did not stop after parent context cancellation")
	}
}

func TestDispatcher_RunContextRejectsConcurrentRun(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{tenants: []string{"tenant-1"}}
	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithDispatchInterval(5*time.Millisecond),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- dispatcher.RunContext(ctx, nil)
	}()

	require.Eventually(t, func() bool {
		return repo.listPendingCallCount() > 0
	}, time.Second, time.Millisecond)

	err = dispatcher.RunContext(context.Background(), nil)
	require.ErrorIs(t, err, ErrOutboxDispatcherRunning)

	cancel()
	require.NoError(t, <-runDone)
}

func TestDispatcher_ShutdownTimeoutWhenDispatchBlocked(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	repo := &fakeRepo{
		tenants:            []string{"tenant-1"},
		listPendingBlocked: block,
		blockIgnoresCtx:    true,
	}

	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithDispatchInterval(5*time.Millisecond),
	)
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		runDone <- dispatcher.Run(nil)
	}()

	require.Eventually(t, func() bool {
		return repo.listPendingCallCount() > 0
	}, time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err = dispatcher.Shutdown(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "dispatcher shutdown")

	close(block)

	select {
	case runErr := <-runDone:
		require.NoError(t, runErr)
	case <-time.After(time.Second):
		t.Fatal("dispatcher run did not exit after unblock")
	}
}

func TestDispatcher_CollectEventsPipelinePrioritizesAndDeduplicates(t *testing.T) {
	t.Parallel()

	priorityID := uuid.New()
	stuckID := uuid.New()
	failedID := uuid.New()

	repo := &fakeRepo{
		pendingByType: map[string][]*OutboxEvent{
			"priority.payment": {{ID: priorityID, EventType: "priority.payment", Payload: []byte("p")}},
		},
		stuck: []*OutboxEvent{
			{ID: priorityID, EventType: "priority.payment", Payload: []byte("dup")},
			{ID: stuckID, EventType: "stuck.payment", Payload: []byte("s")},
		},
		failedForRetry: []*OutboxEvent{{ID: failedID, EventType: "failed.payment", Payload: []byte("f")}},
		pending:        []*OutboxEvent{{ID: uuid.New(), EventType: "pending.payment", Payload: []byte("x")}},
	}

	handlers := NewHandlerRegistry()
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithBatchSize(4),
		WithPriorityBudget(2),
		WithMaxFailedPerBatch(2),
		WithPriorityEventTypes("priority.payment"),
	)
	require.NoError(t, err)

	ctx, span := dispatcher.tracer.Start(context.Background(), "test.collect_events")
	defer span.End()

	collected := dispatcher.collectEvents(ctx, span)
	require.Len(t, collected, 3)
	require.Equal(t, priorityID, collected[0].ID)
	require.Equal(t, stuckID, collected[1].ID)
	require.Equal(t, failedID, collected[2].ID)
}

func TestDispatcher_CollectEvents_ContinuesWhenResetStuckProcessingFails(t *testing.T) {
	t.Parallel()

	failedID := uuid.New()
	pendingID := uuid.New()

	repo := &fakeRepo{
		resetStuckErr:  errors.New("reset stuck failed"),
		failedForRetry: []*OutboxEvent{{ID: failedID, EventType: "failed.payment", Payload: []byte("f")}},
		pending:        []*OutboxEvent{{ID: pendingID, EventType: "pending.payment", Payload: []byte("p")}},
	}

	dispatcher, err := NewDispatcher(
		repo,
		NewHandlerRegistry(),
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithBatchSize(4),
		WithMaxFailedPerBatch(2),
	)
	require.NoError(t, err)

	ctx, span := dispatcher.tracer.Start(context.Background(), "test.collect_events_reset_stuck_error")
	defer span.End()

	collected := dispatcher.collectEvents(ctx, span)
	require.Len(t, collected, 2)
	require.Equal(t, failedID, collected[0].ID)
	require.Equal(t, pendingID, collected[1].ID)
}

func TestDispatcher_CollectEvents_ContinuesWhenResetForRetryFails(t *testing.T) {
	t.Parallel()

	stuckID := uuid.New()
	pendingID := uuid.New()

	repo := &fakeRepo{
		stuck:            []*OutboxEvent{{ID: stuckID, EventType: "stuck.payment", Payload: []byte("s")}},
		resetForRetryErr: errors.New("reset retry failed"),
		pending:          []*OutboxEvent{{ID: pendingID, EventType: "pending.payment", Payload: []byte("p")}},
	}

	dispatcher, err := NewDispatcher(
		repo,
		NewHandlerRegistry(),
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithBatchSize(4),
		WithMaxFailedPerBatch(2),
	)
	require.NoError(t, err)

	ctx, span := dispatcher.tracer.Start(context.Background(), "test.collect_events_reset_retry_error")
	defer span.End()

	collected := dispatcher.collectEvents(ctx, span)
	require.Len(t, collected, 2)
	require.Equal(t, stuckID, collected[0].ID)
	require.Equal(t, pendingID, collected[1].ID)
}

func TestDispatcher_CollectEvents_ContinuesWhenListPendingByTypeFails(t *testing.T) {
	t.Parallel()

	stuckID := uuid.New()
	failedID := uuid.New()
	pendingID := uuid.New()

	repo := &fakeRepo{
		listPendingTypeErr: errors.New("list pending by type failed"),
		stuck:              []*OutboxEvent{{ID: stuckID, EventType: "stuck.payment", Payload: []byte("s")}},
		failedForRetry:     []*OutboxEvent{{ID: failedID, EventType: "failed.payment", Payload: []byte("f")}},
		pending:            []*OutboxEvent{{ID: pendingID, EventType: "pending.payment", Payload: []byte("p")}},
	}

	dispatcher, err := NewDispatcher(
		repo,
		NewHandlerRegistry(),
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithBatchSize(4),
		WithPriorityBudget(2),
		WithMaxFailedPerBatch(2),
		WithPriorityEventTypes("priority.payment"),
	)
	require.NoError(t, err)

	ctx, span := dispatcher.tracer.Start(context.Background(), "test.collect_events_priority_error")
	defer span.End()

	collected := dispatcher.collectEvents(ctx, span)
	require.Len(t, collected, 3)
	require.Equal(t, stuckID, collected[0].ID)
	require.Equal(t, failedID, collected[1].ID)
	require.Equal(t, pendingID, collected[2].ID)
}

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
