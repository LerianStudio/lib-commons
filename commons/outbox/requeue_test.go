//go:build unit

package outbox

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// waitingFakeRepo is a claim-scoped InvalidRequeuer.
type waitingFakeRepo struct {
	*fakeRepo
	requeueErr   error
	requeued     []uuid.UUID
	requeueScope []string
}

func (repo *waitingFakeRepo) ScopesClaimsByEventType() {}

func (repo *waitingFakeRepo) RequeueInvalid(ctx context.Context, id uuid.UUID) error {
	repo.requeueScope, _ = ClaimEventTypesFromContext(ctx)
	repo.requeued = append(repo.requeued, id)

	return repo.requeueErr
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
