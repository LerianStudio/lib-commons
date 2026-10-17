//go:build unit

package outbox

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// waitingFakeRepo is a claim-scoped repository with both waiting-row capabilities.
type waitingFakeRepo struct {
	*fakeRepo
	requeueErr   error
	requeued     []uuid.UUID
	requeueScope []string
	oldest       map[string]time.Time // by tenant; absent means nothing waits
	oldestErr    error
}

func (repo *waitingFakeRepo) ScopesClaimsByEventType() {}

func (repo *waitingFakeRepo) Requeue(ctx context.Context, id uuid.UUID) error {
	repo.requeueScope, _ = ClaimEventTypesFromContext(ctx)
	repo.requeued = append(repo.requeued, id)

	return repo.requeueErr
}

func (repo *waitingFakeRepo) OldestWaitingCreatedAt(ctx context.Context) (time.Time, bool, error) {
	tenantID, _ := TenantIDFromContext(ctx)
	oldest, found := repo.oldest[tenantID]

	return oldest, found, repo.oldestErr
}

func TestDispatcher_Requeue(t *testing.T) {
	t.Parallel()

	var nilDispatcher *Dispatcher
	require.ErrorIs(t, nilDispatcher.Requeue(context.Background(), uuid.New()), ErrOutboxDispatcherRequired)

	unsupported, err := NewDispatcher(&fakeRepo{}, NewHandlerRegistry(), nil, nil)
	require.NoError(t, err)
	require.ErrorIs(t, unsupported.Requeue(context.Background(), uuid.New()), ErrOutboxRequeueUnsupported)

	repo := &waitingFakeRepo{fakeRepo: &fakeRepo{}}
	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.settled", func(context.Context, *OutboxEvent) error { return nil }))

	dispatcher, err := NewDispatcher(repo, handlers, nil, nil, WithClaimScopedToHandlers())
	require.NoError(t, err)

	ctx := ContextWithTenantID(context.Background(), "tenant-a")
	id := uuid.New()
	require.NoError(t, dispatcher.Requeue(ctx, id))
	require.Equal(t, []uuid.UUID{id}, repo.requeued)
	require.Equal(t, []string{"payment.settled"}, repo.requeueScope, "the claim scope applies to the requeue")

	repo.requeueErr = fmt.Errorf("requeueing event: %w", ErrOutboxEventNotRequeueable)
	require.ErrorIs(t, dispatcher.Requeue(ctx, uuid.New()), ErrOutboxEventNotRequeueable)
}

// The gauge is the largest age across scopes sharing a series: tenant-a's 0 never hides
// tenant-b's fact, even on passes where tenant-b is cold or the read fails, and it falls to
// 0 once tenant-b's fact is published.
func TestDispatcher_RecordsOldestWaitingAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	active := &OutboxEvent{ID: uuid.New(), EventType: "payment.settled", Payload: []byte(`{}`)}
	repo := &waitingFakeRepo{
		fakeRepo: &fakeRepo{tenants: []string{"tenant-a", "tenant-b"}, pendingByTenant: map[string][]*OutboxEvent{"tenant-a": {active}}},
		oldest:   map[string]time.Time{"tenant-b": now.Add(-72 * time.Hour)},
	}
	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.settled", func(context.Context, *OutboxEvent) error { return nil }))

	reader := sdkmetric.NewManualReader()
	dispatcher, err := NewDispatcher(repo, handlers, nil, nil, WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	require.NoError(t, err)

	dispatcher.now = func() time.Time { return now }
	gauge := func() int64 {
		dispatcher.dispatchAcrossTenants(context.Background())

		metricData := findOutboxMetric(collectOutboxMetrics(t, reader), "outbox.queue.oldest_age")
		require.NotNil(t, metricData)
		points := metricData.Data.(metricdata.Gauge[int64]).DataPoints
		require.Len(t, points, 1, "tenant attributes are off: one series")

		return points[0].Value
	}

	threeDays := int64(72 * time.Hour / time.Second)
	require.Equal(t, threeDays, gauge(), "both scopes dispatched")
	require.Equal(t, threeDays, gauge(), "tenant-b cold, tenant-a dispatched with nothing waiting")

	repo.oldestErr = errors.New("database unavailable")
	require.Equal(t, threeDays, gauge(), "a failed read keeps the last samples")

	repo.oldestErr = nil
	delete(repo.oldest, "tenant-b")
	now = now.Add(time.Minute)
	require.Zero(t, gauge(), "tenant-b due again, its fact published")
}
