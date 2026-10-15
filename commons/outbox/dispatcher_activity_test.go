//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package outbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

type activityScopeContextKey struct{}

type activityClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *activityClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()

	return clock.now
}

func (clock *activityClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()

	clock.now = clock.now.Add(duration)
}

type activityCountingRepo struct {
	*fakeRepo

	mu            sync.Mutex
	scopes        []TenantDispatchScope
	topologyCalls int
	queryCalls    map[TenantDispatchScope]int
	pending       map[TenantDispatchScope][]*OutboxEvent
	failed        map[TenantDispatchScope][]*OutboxEvent
	stuck         map[TenantDispatchScope][]*OutboxEvent
}

func newActivityCountingRepo(scopes ...TenantDispatchScope) *activityCountingRepo {
	return &activityCountingRepo{
		fakeRepo:   &fakeRepo{},
		scopes:     append([]TenantDispatchScope(nil), scopes...),
		queryCalls: make(map[TenantDispatchScope]int),
		pending:    make(map[TenantDispatchScope][]*OutboxEvent),
		failed:     make(map[TenantDispatchScope][]*OutboxEvent),
		stuck:      make(map[TenantDispatchScope][]*OutboxEvent),
	}
}

func (repo *activityCountingRepo) ListTenantDispatchScopes(context.Context) ([]TenantDispatchScope, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	repo.topologyCalls++

	return append([]TenantDispatchScope(nil), repo.scopes...), nil
}

func (repo *activityCountingRepo) ContextForTenantDispatchScope(
	ctx context.Context,
	scope TenantDispatchScope,
) context.Context {
	return context.WithValue(ctx, activityScopeContextKey{}, scope)
}

func (repo *activityCountingRepo) ListPending(ctx context.Context, _ int) ([]*OutboxEvent, error) {
	return repo.take(ctx, repo.pending), nil
}

func (repo *activityCountingRepo) ResetForRetry(
	ctx context.Context,
	_ int,
	_ time.Time,
	_ int,
) ([]*OutboxEvent, error) {
	return repo.take(ctx, repo.failed), nil
}

func (repo *activityCountingRepo) ResetStuckProcessing(
	ctx context.Context,
	_ int,
	_ time.Time,
	_ int,
) ([]*OutboxEvent, error) {
	return repo.take(ctx, repo.stuck), nil
}

func (repo *activityCountingRepo) take(
	ctx context.Context,
	queue map[TenantDispatchScope][]*OutboxEvent,
) []*OutboxEvent {
	scope, _ := ctx.Value(activityScopeContextKey{}).(TenantDispatchScope)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	repo.queryCalls[scope]++
	events := append([]*OutboxEvent(nil), queue[scope]...)
	delete(queue, scope)

	return events
}

func (repo *activityCountingRepo) setScopes(scopes ...TenantDispatchScope) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	repo.scopes = append([]TenantDispatchScope(nil), scopes...)
}

func (repo *activityCountingRepo) enqueue(
	scope TenantDispatchScope,
	queue map[TenantDispatchScope][]*OutboxEvent,
	event *OutboxEvent,
) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	queue[scope] = append(queue[scope], event)
}

func (repo *activityCountingRepo) counts() (int, map[TenantDispatchScope]int) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	queryCalls := make(map[TenantDispatchScope]int, len(repo.queryCalls))
	for scope, count := range repo.queryCalls {
		queryCalls[scope] = count
	}

	return repo.topologyCalls, queryCalls
}

func newActivityDispatcher(
	t *testing.T,
	repo *activityCountingRepo,
	clock *activityClock,
	coldInterval time.Duration,
) *Dispatcher {
	t.Helper()

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return nil
	}))

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithDispatchInterval(2*time.Second),
		WithColdDispatchInterval(coldInterval),
		WithPublishMaxAttempts(1),
	)
	require.NoError(t, err)
	dispatcher.now = clock.Now

	return dispatcher
}

func TestDispatcher_ColdScopeBackoff_ReducesIdleQueriesAndKeepsTopologyFresh(t *testing.T) {
	t.Parallel()

	generic := TenantDispatchScope{TenantID: "tenant-a"}
	module := TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"}
	repo := newActivityCountingRepo(generic, module)
	clock := &activityClock{now: time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)}
	dispatcher := newActivityDispatcher(t, repo, clock, time.Minute)

	dispatcher.dispatchAcrossTenants(context.Background())
	dispatcher.dispatchAcrossTenants(context.Background())
	clock.Advance(59 * time.Second)
	dispatcher.dispatchAcrossTenants(context.Background())

	topologyCalls, queryCalls := repo.counts()
	require.Equal(t, 3, topologyCalls)
	require.Equal(t, 3, queryCalls[generic])
	require.Equal(t, 3, queryCalls[module])

	clock.Advance(time.Second)
	dispatcher.dispatchAcrossTenants(context.Background())

	topologyCalls, queryCalls = repo.counts()
	require.Equal(t, 4, topologyCalls)
	require.Equal(t, 6, queryCalls[generic])
	require.Equal(t, 6, queryCalls[module])
}

func TestDispatcher_ColdScopeBackoff_DiscoversWorkWithinBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enqueue func(*activityCountingRepo, TenantDispatchScope, *OutboxEvent)
	}{
		{
			name: "new pending work",
			enqueue: func(repo *activityCountingRepo, scope TenantDispatchScope, event *OutboxEvent) {
				repo.enqueue(scope, repo.pending, event)
			},
		},
		{
			name: "retry eligible work",
			enqueue: func(repo *activityCountingRepo, scope TenantDispatchScope, event *OutboxEvent) {
				repo.enqueue(scope, repo.failed, event)
			},
		},
		{
			name: "stuck processing work",
			enqueue: func(repo *activityCountingRepo, scope TenantDispatchScope, event *OutboxEvent) {
				repo.enqueue(scope, repo.stuck, event)
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scope := TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"}
			repo := newActivityCountingRepo(scope)
			clock := &activityClock{now: time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)}
			dispatcher := newActivityDispatcher(t, repo, clock, time.Minute)

			dispatcher.dispatchAcrossTenants(context.Background())
			event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}
			test.enqueue(repo, scope, event)

			clock.Advance(59 * time.Second)
			dispatcher.dispatchAcrossTenants(context.Background())
			require.Empty(t, repo.markedPub)

			clock.Advance(time.Second)
			dispatcher.dispatchAcrossTenants(context.Background())
			require.Equal(t, []uuid.UUID{event.ID}, repo.markedPub)
		})
	}
}

func TestDispatcher_ColdScopeBackoff_KeepsRecentlyActiveScopeHot(t *testing.T) {
	t.Parallel()

	scope := TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"}
	repo := newActivityCountingRepo(scope)
	clock := &activityClock{now: time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)}
	dispatcher := newActivityDispatcher(t, repo, clock, time.Minute)
	event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}
	repo.enqueue(scope, repo.pending, event)

	dispatcher.dispatchAcrossTenants(context.Background())
	clock.Advance(2 * time.Second)
	dispatcher.dispatchAcrossTenants(context.Background())

	_, queryCalls := repo.counts()
	require.Equal(t, 6, queryCalls[scope])
}

func TestDispatcher_ColdScopeBackoff_EvictsRemovedScopeState(t *testing.T) {
	t.Parallel()

	scope := TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"}
	repo := newActivityCountingRepo(scope)
	clock := &activityClock{now: time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)}
	dispatcher := newActivityDispatcher(t, repo, clock, time.Minute)

	dispatcher.dispatchAcrossTenants(context.Background())
	repo.setScopes()
	clock.Advance(2 * time.Second)
	dispatcher.dispatchAcrossTenants(context.Background())
	repo.setScopes(scope)
	dispatcher.dispatchAcrossTenants(context.Background())

	_, queryCalls := repo.counts()
	require.Equal(t, 6, queryCalls[scope])
}

// fallbackActivityRepo has no discovered tenants and lets dispatch fall back to
// the default scope.
type fallbackActivityRepo struct {
	*activityCountingRepo
}

func (fallbackActivityRepo) RequiresTenant() bool {
	return false
}

func TestDispatcher_FallbackScopeBackoff_SkipsIdleTicksAndResumesOnEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  context.Context
	}{
		{name: "default scope", ctx: context.Background()},
		{name: "tenant from context", ctx: ContextWithTenantID(context.Background(), "tenant-a")},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			repo := newActivityCountingRepo()
			clock := &activityClock{now: time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)}
			dispatcher := newActivityDispatcher(t, repo, clock, time.Minute)
			dispatcher.repo = fallbackActivityRepo{activityCountingRepo: repo}

			dispatcher.dispatchTick(test.ctx)
			clock.Advance(2 * time.Second)
			dispatcher.dispatchTick(test.ctx)

			topologyCalls, queryCalls := repo.counts()
			require.Equal(t, 1, topologyCalls, "an idle tick must not list tenants")
			require.Equal(t, 3, queryCalls[fallbackDispatchScope], "an idle tick must not query the outbox")

			event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}
			repo.enqueue(fallbackDispatchScope, repo.pending, event)

			clock.Advance(57 * time.Second)
			dispatcher.dispatchTick(test.ctx)
			require.Empty(t, repo.markedPub)

			clock.Advance(time.Second)
			dispatcher.dispatchTick(test.ctx)
			require.Equal(t, []uuid.UUID{event.ID}, repo.markedPub)

			// Work keeps the scope hot, so the next tick polls again.
			clock.Advance(2 * time.Second)
			dispatcher.dispatchTick(test.ctx)

			topologyCalls, queryCalls = repo.counts()
			require.Equal(t, 3, topologyCalls)
			require.Equal(t, 9, queryCalls[fallbackDispatchScope])
		})
	}
}

func TestDispatcher_ReconcileScopeActivity_KeepsFallbackScope(t *testing.T) {
	t.Parallel()

	discovered := TenantDispatchScope{TenantID: "tenant-a"}
	repo := newActivityCountingRepo()
	clock := &activityClock{now: time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)}
	dispatcher := newActivityDispatcher(t, repo, clock, time.Minute)

	dispatcher.recordScopeActivity(fallbackDispatchScope, DispatchResult{Processed: 1}, clock.Now())
	dispatcher.recordScopeActivity(discovered, DispatchResult{Processed: 1}, clock.Now())

	dispatcher.reconcileScopeActivity(nil)

	require.Contains(t, dispatcher.scopeActivity, fallbackDispatchScope)
	require.NotContains(t, dispatcher.scopeActivity, discovered)
	require.Equal(t, clock.Now(), dispatcher.scopeActivity[fallbackDispatchScope].lastWorkAt)
}

func TestDispatcher_FallbackScopeBackoff_ColdTickExportsNoSpan(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	repo := newActivityCountingRepo()
	clock := &activityClock{now: time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)}
	dispatcher := newActivityDispatcher(t, repo, clock, time.Minute)
	dispatcher.repo = fallbackActivityRepo{activityCountingRepo: repo}
	dispatcher.tracer = provider.Tracer("test")

	dispatcher.dispatchTick(context.Background())
	warmSpans := len(recorder.Ended())
	require.Positive(t, warmSpans, "the first tick must be traced")

	clock.Advance(2 * time.Second)
	dispatcher.dispatchTick(context.Background())

	require.Len(t, recorder.Ended(), warmSpans, "a cold tick must not open a span")
}

// slowListingRepo advances the clock during tenant listing, as a real
// discovery query does.
type slowListingRepo struct {
	fallbackActivityRepo
	clock *activityClock
}

func (repo slowListingRepo) ListTenantDispatchScopes(ctx context.Context) ([]TenantDispatchScope, error) {
	repo.clock.Advance(10 * time.Millisecond)

	return repo.fallbackActivityRepo.ListTenantDispatchScopes(ctx)
}

func TestDispatcher_FallbackScopeBackoff_HotScopePollsEveryTick(t *testing.T) {
	t.Parallel()

	repo := newActivityCountingRepo()
	clock := &activityClock{now: time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)}
	dispatcher := newActivityDispatcher(t, repo, clock, time.Minute)
	dispatcher.repo = slowListingRepo{fallbackActivityRepo: fallbackActivityRepo{activityCountingRepo: repo}, clock: clock}

	first := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}
	repo.enqueue(fallbackDispatchScope, repo.pending, first)
	dispatcher.dispatchTick(context.Background())

	// The next tick fires one DispatchInterval after the previous one started,
	// which is before the previous dispatch time plus DispatchInterval.
	clock.Advance(2*time.Second - 10*time.Millisecond)

	second := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}
	repo.enqueue(fallbackDispatchScope, repo.pending, second)
	dispatcher.dispatchTick(context.Background())

	require.Equal(t, []uuid.UUID{first.ID, second.ID}, repo.markedPub)
}
