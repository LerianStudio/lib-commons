//go:build unit

package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace/noop"
)

func newRetentionDispatcher(
	t *testing.T,
	repo OutboxRepository,
	clock *activityClock,
	logger obs.Logger,
	opts ...DispatcherOption,
) *Dispatcher {
	t.Helper()

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return nil
	}))

	base := []DispatcherOption{
		WithDispatchInterval(2 * time.Second),
		WithColdDispatchInterval(time.Minute),
		WithPublishMaxAttempts(1),
	}

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		logger,
		noop.NewTracerProvider().Tracer("test"),
		append(base, opts...)...,
	)
	require.NoError(t, err)
	dispatcher.now = clock.Now

	return dispatcher
}

func retentionClock() *activityClock {
	return &activityClock{now: time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)}
}

func TestDispatcherRetention_DisabledByDefault(t *testing.T) {
	t.Parallel()

	scope := TenantDispatchScope{TenantID: "tenant-a"}
	repo := newActivityCountingRepo(scope)
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil)

	for range 3 {
		dispatcher.dispatchAcrossTenants(context.Background())
		clock.Advance(2 * time.Hour)
	}

	require.Empty(t, repo.deletePublishedCallLog())
}

func TestDispatcherRetention_SweepsEachScopeOncePerInterval(t *testing.T) {
	t.Parallel()

	generic := TenantDispatchScope{TenantID: "tenant-a"}
	module := TenantDispatchScope{TenantID: "tenant-a", PoolKey: "consignado"}
	repo := newActivityCountingRepo(generic, module)
	clock := retentionClock()
	start := clock.Now()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil,
		WithRetentionPublished(24*time.Hour),
		WithRetentionSweepInterval(time.Hour),
		WithRetentionBatchSize(250),
		WithRetentionKeepEventTypes(" leilao.solicitado ", "", "margem.solicitada"),
	)

	dispatcher.dispatchAcrossTenants(context.Background())

	calls := repo.deletePublishedCallLog()
	require.Len(t, calls, 2)

	for i, want := range []TenantDispatchScope{generic, module} {
		require.Equal(t, want, calls[i].scope)
		require.Equal(t, "tenant-a", calls[i].tenantID)
		require.Equal(t, start.Add(-24*time.Hour), calls[i].before)
		require.Equal(t, []string{"leilao.solicitado", "margem.solicitada"}, calls[i].keep)
		require.Equal(t, 250, calls[i].limit)
	}

	// Every dispatch tick inside the sweep interval leaves retention alone.
	for range 59 {
		clock.Advance(time.Minute)
		dispatcher.dispatchAcrossTenants(context.Background())
	}

	require.Len(t, repo.deletePublishedCallLog(), 2)

	clock.Advance(time.Minute)
	dispatcher.dispatchAcrossTenants(context.Background())

	calls = repo.deletePublishedCallLog()
	require.Len(t, calls, 4)
	require.Equal(t, start.Add(time.Hour).Add(-24*time.Hour), calls[3].before)
}

func TestDispatcherRetention_DefaultsWhenOnlyRetentionIsSet(t *testing.T) {
	t.Parallel()

	scope := TenantDispatchScope{TenantID: "tenant-a"}
	repo := newActivityCountingRepo(scope)
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil, WithRetentionPublished(time.Hour))

	dispatcher.dispatchAcrossTenants(context.Background())
	clock.Advance(59 * time.Minute)
	dispatcher.dispatchAcrossTenants(context.Background())

	calls := repo.deletePublishedCallLog()
	require.Len(t, calls, 1)
	require.Equal(t, 500, calls[0].limit)
	require.Empty(t, calls[0].keep)

	clock.Advance(time.Minute)
	dispatcher.dispatchAcrossTenants(context.Background())
	require.Len(t, repo.deletePublishedCallLog(), 2)
}

func TestDispatcherRetention_FailureWarnsAndDispatchStillRuns(t *testing.T) {
	t.Parallel()

	scope := TenantDispatchScope{TenantID: "tenant-a"}
	repo := newActivityCountingRepo(scope)
	repo.deletePublishedErr = errors.New("permission denied for table outbox_events")
	event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte("ok")}
	repo.enqueue(scope, repo.pending, event)

	logger := &recordingLogger{}
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, logger, WithRetentionPublished(time.Hour))

	dispatcher.dispatchAcrossTenants(context.Background())

	require.Equal(t, []uuid.UUID{event.ID}, repo.markedPub)
	require.Len(t, repo.deletePublishedCallLog(), 1)
	require.True(t, logger.hasMessage(obs.LevelWarn, "outbox retention sweep failed"))

	// A failed sweep waits for the next interval instead of retrying every tick.
	clock.Advance(time.Minute)
	dispatcher.dispatchAcrossTenants(context.Background())
	require.Len(t, repo.deletePublishedCallLog(), 1)
}

func TestDispatcherRetention_LogsSweepAtDebugAndCountsPurgedEvents(t *testing.T) {
	t.Parallel()

	scope := TenantDispatchScope{TenantID: "tenant-a"}
	repo := newActivityCountingRepo(scope)
	repo.deletePublishedResult = 42

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	logger := &recordingLogger{}
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, logger,
		WithRetentionPublished(time.Hour),
		WithMeterProvider(provider),
		WithTenantMetricAttributes(true),
	)

	dispatcher.dispatchAcrossTenants(context.Background())

	require.True(t, logger.hasMessage(obs.LevelDebug, "outbox retention sweep completed"))
	requireIntMetricValue(t, collectOutboxMetrics(t, reader), "outbox.events.purged", "tenant-a", 42)
}

func TestDispatcherRetention_SingleTenantPathSweeps(t *testing.T) {
	t.Parallel()

	repo := &tenantAwareFakeRepo{fakeRepo: &fakeRepo{}, requiresTenant: false}
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil, WithRetentionPublished(time.Hour))

	dispatcher.dispatchAcrossTenants(context.Background())
	clock.Advance(30 * time.Minute)
	dispatcher.dispatchAcrossTenants(context.Background())
	require.Len(t, repo.deletePublishedCallLog(), 1)

	clock.Advance(30 * time.Minute)
	dispatcher.dispatchAcrossTenants(context.Background())
	require.Len(t, repo.deletePublishedCallLog(), 2)
}

func TestDispatcherRetention_ContextTenantPathSweeps(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil, WithRetentionPublished(time.Hour))
	ctx := ContextWithTenantID(context.Background(), "tenant-a")

	dispatcher.dispatchAcrossTenants(ctx)
	dispatcher.dispatchAcrossTenants(ctx)

	calls := repo.deletePublishedCallLog()
	require.Len(t, calls, 1)
	require.Equal(t, "tenant-a", calls[0].tenantID)
}

func TestDispatcherRetention_RequiredTenantWithoutTenantsDoesNotSweep(t *testing.T) {
	t.Parallel()

	repo := &tenantAwareFakeRepo{fakeRepo: &fakeRepo{}, requiresTenant: true}
	dispatcher := newRetentionDispatcher(t, repo, retentionClock(), nil, WithRetentionPublished(time.Hour))

	dispatcher.dispatchAcrossTenants(context.Background())

	require.Empty(t, repo.deletePublishedCallLog())
}

type publishedListingRepo struct {
	*tenantAwareFakeRepo
	listed    []string
	listCalls []time.Time
}

func (repo *publishedListingRepo) ListTenantsWithPublishedBefore(_ context.Context, before time.Time, _ []string) ([]string, error) {
	repo.listCalls = append(repo.listCalls, before)

	return repo.listed, nil
}

func TestDispatcherRetention_SweepsListedTenantsOncePerInterval(t *testing.T) {
	t.Parallel()

	repo := &publishedListingRepo{
		tenantAwareFakeRepo: &tenantAwareFakeRepo{fakeRepo: &fakeRepo{tenants: []string{"tenant-busy"}}, requiresTenant: true},
		listed:              []string{"tenant-busy", "tenant-idle"},
	}
	clock := retentionClock()
	start := clock.Now()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil,
		WithRetentionPublished(24*time.Hour),
		WithRetentionSweepInterval(time.Hour),
	)

	dispatcher.dispatchAcrossTenants(context.Background())
	clock.Advance(59 * time.Minute)
	dispatcher.dispatchAcrossTenants(context.Background())

	require.Equal(t, []time.Time{start.Add(-24 * time.Hour)}, repo.listCalls)

	calls := repo.deletePublishedCallLog()
	require.Len(t, calls, 2, "a tenant both discoveries return is swept once")
	require.Equal(t, "tenant-busy", calls[0].tenantID)
	require.Equal(t, "tenant-idle", calls[1].tenantID)

	clock.Advance(time.Minute)
	dispatcher.dispatchAcrossTenants(context.Background())

	require.Len(t, repo.listCalls, 2)
	require.Len(t, repo.deletePublishedCallLog(), 4)
}

func TestNewDispatcher_RejectsInvalidRetentionConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    []DispatcherOption
		wantErr bool
	}{
		{name: "negative retention", opts: []DispatcherOption{WithRetentionPublished(-time.Hour)}, wantErr: true},
		{
			name:    "negative batch size with retention",
			opts:    []DispatcherOption{WithRetentionPublished(time.Hour), WithRetentionBatchSize(-1)},
			wantErr: true,
		},
		{name: "negative batch size ignored while disabled", opts: []DispatcherOption{WithRetentionBatchSize(-1)}},
		{name: "enabled", opts: []DispatcherOption{WithRetentionPublished(time.Hour), WithRetentionBatchSize(10)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dispatcher, err := NewDispatcher(&fakeRepo{}, NewHandlerRegistry(), nil, nil, test.opts...)
			if test.wantErr {
				require.ErrorIs(t, err, ErrOutboxRetentionConfigInvalid)
				require.Nil(t, dispatcher)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, dispatcher)
		})
	}
}

func TestDispatcherConfigNormalize_RetentionDefaults(t *testing.T) {
	t.Parallel()

	enabled := DispatcherConfig{RetentionPublished: time.Hour, RetentionSweepInterval: -time.Second}
	enabled.normalize()
	require.Equal(t, time.Hour, enabled.RetentionSweepInterval)
	require.Equal(t, 500, enabled.RetentionBatchSize)

	custom := DispatcherConfig{RetentionPublished: time.Hour, RetentionSweepInterval: time.Minute, RetentionBatchSize: 7}
	custom.normalize()
	require.Equal(t, time.Minute, custom.RetentionSweepInterval)
	require.Equal(t, 7, custom.RetentionBatchSize)

	disabled := DispatcherConfig{}
	disabled.normalize()
	require.Zero(t, disabled.RetentionPublished)
	require.Zero(t, disabled.RetentionSweepInterval)
	require.Zero(t, disabled.RetentionBatchSize)
}

// nonPurgingRepo exposes only OutboxRepository: the embedded interface does
// not promote the PublishedPurger capability of the value it wraps.
type nonPurgingRepo struct {
	OutboxRepository
}

func TestNewDispatcher_RetentionRequiresPurgerCapability(t *testing.T) {
	t.Parallel()

	repo := nonPurgingRepo{OutboxRepository: &fakeRepo{}}

	dispatcher, err := NewDispatcher(repo, NewHandlerRegistry(), nil, nil, WithRetentionPublished(time.Hour))
	require.ErrorIs(t, err, ErrOutboxRetentionUnsupported)
	require.Nil(t, dispatcher)

	dispatcher, err = NewDispatcher(repo, NewHandlerRegistry(), nil, nil)
	require.NoError(t, err, "a repository without the capability is fine while retention is disabled")
	require.NotNil(t, dispatcher)
}

func TestDispatcherRetention_FlickeringScopeSweptOncePerInterval(t *testing.T) {
	t.Parallel()

	scopeA := TenantDispatchScope{TenantID: "tenant-a"}
	scopeB := TenantDispatchScope{TenantID: "tenant-b"}
	repo := newActivityCountingRepo(scopeA, scopeB)
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil,
		WithRetentionPublished(24*time.Hour),
		WithRetentionSweepInterval(time.Hour),
	)

	sweepsOf := func(scope TenantDispatchScope) int {
		count := 0

		for _, call := range repo.deletePublishedCallLog() {
			if call.scope == scope {
				count++
			}
		}

		return count
	}

	// Column-per-tenant and mongo discovery list only tenants with outstanding
	// work, so tenant-a drops in and out of discovery between ticks.
	for tick := range 30 {
		repo.mu.Lock()
		if tick%2 == 0 {
			repo.scopes = []TenantDispatchScope{scopeA, scopeB}
		} else {
			repo.scopes = []TenantDispatchScope{scopeB}
		}
		repo.mu.Unlock()

		dispatcher.dispatchAcrossTenants(context.Background())
		clock.Advance(time.Minute)
	}

	require.Equal(t, 1, sweepsOf(scopeA), "a scope flickering out of discovery must keep its sweep time")
	require.Equal(t, 1, sweepsOf(scopeB))

	// Past the interval the entry ages out and the scope is due again.
	clock.Advance(time.Hour)

	repo.mu.Lock()
	repo.scopes = []TenantDispatchScope{scopeA, scopeB}
	repo.mu.Unlock()

	dispatcher.dispatchAcrossTenants(context.Background())
	require.Equal(t, 2, sweepsOf(scopeA))
	require.Equal(t, 2, sweepsOf(scopeB))
}

func TestDispatcherRetention_SweepMemoryPrunedByAge(t *testing.T) {
	t.Parallel()

	scopeA := TenantDispatchScope{TenantID: "tenant-a"}
	scopeB := TenantDispatchScope{TenantID: "tenant-b"}
	repo := newActivityCountingRepo(scopeA, scopeB)
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil,
		WithRetentionPublished(24*time.Hour),
		WithRetentionSweepInterval(time.Hour),
	)

	dispatcher.dispatchAcrossTenants(context.Background())

	repo.mu.Lock()
	repo.scopes = []TenantDispatchScope{scopeB}
	repo.mu.Unlock()

	clock.Advance(time.Hour)
	dispatcher.dispatchAcrossTenants(context.Background())

	dispatcher.scopeActivityMu.Lock()
	_, keptA := dispatcher.retentionSweptAt[scopeA]
	_, keptB := dispatcher.retentionSweptAt[scopeB]
	size := len(dispatcher.retentionSweptAt)
	dispatcher.scopeActivityMu.Unlock()

	require.False(t, keptA, "an entry older than the interval is pruned")
	require.True(t, keptB)
	require.Equal(t, 1, size)
}

func TestDispatcherRetention_PurgedMetricWithoutTenantAttribute(t *testing.T) {
	t.Parallel()

	scope := TenantDispatchScope{TenantID: "tenant-a"}
	repo := newActivityCountingRepo(scope)
	repo.deletePublishedResult = 42

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	dispatcher := newRetentionDispatcher(t, repo, retentionClock(), nil,
		WithRetentionPublished(time.Hour),
		WithMeterProvider(provider),
	)

	dispatcher.dispatchAcrossTenants(context.Background())

	metricData := findOutboxMetric(collectOutboxMetrics(t, reader), "outbox.events.purged")
	require.NotNil(t, metricData)

	sum, ok := metricData.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	require.Equal(t, int64(42), sum.DataPoints[0].Value)

	_, hasTenant := sum.DataPoints[0].Attributes.Value(attribute.Key("tenant"))
	require.False(t, hasTenant, "tenant attribute must be absent while tenant metrics are off")
}

func TestDispatcherRetention_CancelledContextSkipsSweep(t *testing.T) {
	t.Parallel()

	scope := TenantDispatchScope{TenantID: "tenant-a"}
	repo := newActivityCountingRepo(scope)
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil, WithRetentionPublished(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dispatcher.sweepRetention(ctx, nil, scope, clock.Now())
	require.Empty(t, repo.deletePublishedCallLog())

	// The skipped sweep did not consume the interval.
	dispatcher.sweepRetention(context.Background(), nil, scope, clock.Now())
	require.Len(t, repo.deletePublishedCallLog(), 1)
}

func TestDispatcherRetention_SweepMemoryPrunedOncePerInterval(t *testing.T) {
	t.Parallel()

	scopeA := TenantDispatchScope{TenantID: "tenant-a"}
	scopeB := TenantDispatchScope{TenantID: "tenant-b"}
	scopeC := TenantDispatchScope{TenantID: "tenant-c"}
	scopeD := TenantDispatchScope{TenantID: "tenant-d"}
	repo := newActivityCountingRepo(scopeA)
	clock := retentionClock()
	dispatcher := newRetentionDispatcher(t, repo, clock, nil,
		WithRetentionPublished(24*time.Hour),
		WithRetentionSweepInterval(time.Hour),
	)

	sweepOnly := func(scope TenantDispatchScope) {
		repo.mu.Lock()
		repo.scopes = []TenantDispatchScope{scope}
		repo.mu.Unlock()

		dispatcher.dispatchAcrossTenants(context.Background())
	}

	// t0: A. t0+30m: B. t0+1h: C, and the interval since the last prune has
	// passed, so A (1 h old) goes and B (30 min) stays. t0+1h30m: D, and only
	// 30 min have passed since that prune, so B stays although it is now 1 h
	// old: pruning is paid once per interval, not on every granted sweep.
	sweepOnly(scopeA)
	clock.Advance(30 * time.Minute)
	sweepOnly(scopeB)
	clock.Advance(30 * time.Minute)
	sweepOnly(scopeC)
	clock.Advance(30 * time.Minute)
	sweepOnly(scopeD)

	dispatcher.scopeActivityMu.Lock()
	_, keptA := dispatcher.retentionSweptAt[scopeA]
	_, keptB := dispatcher.retentionSweptAt[scopeB]
	_, keptC := dispatcher.retentionSweptAt[scopeC]
	_, keptD := dispatcher.retentionSweptAt[scopeD]
	size := len(dispatcher.retentionSweptAt)
	dispatcher.scopeActivityMu.Unlock()

	require.False(t, keptA, "pruned at the interval boundary")
	require.True(t, keptB, "an aged entry waits for the next interval boundary")
	require.True(t, keptC)
	require.True(t, keptD)
	require.Equal(t, 3, size)
}
