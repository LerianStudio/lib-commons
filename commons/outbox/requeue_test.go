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
)

// waitingFakeRepo is a claim-scoped repository with both waiting-row capabilities.
type waitingFakeRepo struct {
	*fakeRepo
	requeueErr   error
	requeued     []uuid.UUID
	requeueScope []string
	oldest       time.Time
	found        bool
	oldestErr    error
}

func (repo *waitingFakeRepo) ScopesClaimsByEventType() {}

func (repo *waitingFakeRepo) RequeueInvalid(ctx context.Context, id uuid.UUID) error {
	repo.requeueScope, _ = ClaimEventTypesFromContext(ctx)
	repo.requeued = append(repo.requeued, id)

	return repo.requeueErr
}

func (repo *waitingFakeRepo) OldestWaitingCreatedAt(context.Context) (time.Time, bool, error) {
	return repo.oldest, repo.found, repo.oldestErr
}

func TestDispatcher_RequeueInvalid(t *testing.T) {
	t.Parallel()

	var nilDispatcher *Dispatcher
	require.ErrorIs(t, nilDispatcher.RequeueInvalid(context.Background(), uuid.New()), ErrOutboxDispatcherRequired)

	unsupported, err := NewDispatcher(&fakeRepo{}, NewHandlerRegistry(), nil, nil)
	require.NoError(t, err)
	require.ErrorIs(t, unsupported.RequeueInvalid(context.Background(), uuid.New()), ErrOutboxRequeueUnsupported)

	reader := sdkmetric.NewManualReader()
	repo := &waitingFakeRepo{fakeRepo: &fakeRepo{}}
	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.settled", func(context.Context, *OutboxEvent) error { return nil }))

	dispatcher, err := NewDispatcher(repo, handlers, nil, nil, WithClaimScopedToHandlers(), WithTenantMetricAttributes(true),
		WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	require.NoError(t, err)

	ctx := ContextWithTenantID(context.Background(), "tenant-a")
	id := uuid.New()
	require.NoError(t, dispatcher.RequeueInvalid(ctx, id))
	require.Equal(t, []uuid.UUID{id}, repo.requeued)
	require.Equal(t, []string{"payment.settled"}, repo.requeueScope, "the claim scope applies to the requeue")

	repo.requeueErr = fmt.Errorf("requeueing invalid event: %w", ErrOutboxEventNotInvalid)
	require.ErrorIs(t, dispatcher.RequeueInvalid(ctx, uuid.New()), ErrOutboxEventNotInvalid)

	requireIntMetricValue(t, collectOutboxMetrics(t, reader), "outbox.events.requeued", "tenant-a", 1)
}

// Each cycle sets the age of the oldest waiting event, 0 once none waits, and keeps the
// last value when the read fails.
func TestDispatcher_RecordsOldestWaitingAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	reader := sdkmetric.NewManualReader()
	repo := &waitingFakeRepo{fakeRepo: &fakeRepo{}, oldest: now.Add(-90 * time.Second), found: true}

	dispatcher, err := NewDispatcher(repo, NewHandlerRegistry(), nil, nil, WithTenantMetricAttributes(true),
		WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	require.NoError(t, err)

	dispatcher.now = func() time.Time { return now }
	ctx := ContextWithTenantID(context.Background(), "tenant-a")

	steps := []struct {
		found   bool
		readErr error
		want    int64
	}{
		{found: true, want: 90},
		{found: true, readErr: errors.New("database unavailable"), want: 90},
		{found: false, want: 0},
	}

	for _, step := range steps {
		repo.found, repo.oldestErr = step.found, step.readErr
		dispatcher.DispatchOnceResult(ctx)
		requireIntMetricValue(t, collectOutboxMetrics(t, reader), "outbox.queue.oldest_age", "tenant-a", step.want)
	}
}
