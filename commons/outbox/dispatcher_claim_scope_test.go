//go:build unit

package outbox

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

type claimScopeCall struct {
	types []string
	ok    bool
}

// claimScopedFakeRepo records the claim scope ListPending receives.
type claimScopedFakeRepo struct {
	*fakeRepo
	calls []claimScopeCall
}

func (repo *claimScopedFakeRepo) ScopesClaimsByEventType() {}

// ListPendingByTypes makes the fake a MultiTypePendingRepository, which priority types scope.
func (repo *claimScopedFakeRepo) ListPendingByTypes(context.Context, []string, int) ([]*OutboxEvent, error) {
	return nil, nil
}

func (repo *claimScopedFakeRepo) ListPending(ctx context.Context, limit int) ([]*OutboxEvent, error) {
	types, ok := ClaimEventTypesFromContext(ctx)
	repo.calls = append(repo.calls, claimScopeCall{types: types, ok: ok})

	return repo.fakeRepo.ListPending(ctx, limit)
}

// A scoped claim needs a repository that honours it; a multi-type repository that does not
// would run the stuck, failed and pending claims over every other service's events.
func TestNewDispatcher_ClaimScopeRequiresCapability(t *testing.T) {
	t.Parallel()

	multiTypeOnly := &struct {
		*fakeRepo
		MultiTypePendingRepository
	}{fakeRepo: &fakeRepo{}}
	priority := WithPriorityEventTypes("payment.settled")

	tests := []struct {
		name    string
		repo    OutboxRepository
		opt     DispatcherOption
		wantErr error
	}{
		{"handler scope without the capability", &fakeRepo{}, WithClaimScopedToHandlers(), ErrOutboxClaimScopeUnsupported},
		{"handler scope on a capable repository", &claimScopedFakeRepo{fakeRepo: &fakeRepo{}}, WithClaimScopedToHandlers(), nil},
		{"priority types on a multi-type repository without the capability", multiTypeOnly, priority, ErrOutboxClaimScopeUnsupported},
		{"priority types on a capable multi-type repository", &claimScopedFakeRepo{fakeRepo: &fakeRepo{}}, priority, nil},
		{"priority types on a single-type repository", &fakeRepo{}, priority, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewDispatcher(tt.repo, NewHandlerRegistry(), nil, noop.NewTracerProvider().Tracer("test"), tt.opt)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// Handlers registered after construction widen the claim scope on the next cycle.
func TestDispatcher_ClaimScopeRereadsHandlersEachCycle(t *testing.T) {
	t.Parallel()

	repo := &claimScopedFakeRepo{fakeRepo: &fakeRepo{}}
	handlers := NewHandlerRegistry()

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"),
		WithClaimScopedToHandlers())
	require.NoError(t, err)

	dispatcher.DispatchOnceResult(context.Background())

	for _, eventType := range []string{"payment.settled", "payment.created"} {
		require.NoError(t, handlers.Register(eventType, func(context.Context, *OutboxEvent) error { return nil }))
	}

	dispatcher.DispatchOnceResult(context.Background())

	require.Equal(t, []claimScopeCall{
		{ok: true},
		{types: []string{"payment.created", "payment.settled"}, ok: true},
	}, repo.calls)
}

// On a claim-scoped multi-type repository the priority types scope every claim of the
// cycle, intersected with the handler types under WithClaimScopedToHandlers, and stuck
// and failed recovery still runs.
func TestDispatcher_PriorityTypesScopeRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []DispatcherOption
		want []string
	}{
		{"priority types", nil, []string{"payment.settled", "payment.created"}},
		{"intersected with handler types", []DispatcherOption{WithClaimScopedToHandlers()}, []string{"payment.settled"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &claimScopedFakeRepo{fakeRepo: &fakeRepo{}}
			handlers := NewHandlerRegistry()
			require.NoError(t, handlers.Register("payment.settled", func(context.Context, *OutboxEvent) error { return nil }))

			dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"),
				append(tt.opts, WithPriorityEventTypes("payment.settled", "payment.created"))...)
			require.NoError(t, err)

			dispatcher.DispatchOnceResult(context.Background())

			require.Equal(t, []claimScopeCall{{types: tt.want, ok: true}}, repo.calls)
			require.EqualValues(t, 1, atomic.LoadInt32(&repo.resetStuckCalls))
			require.EqualValues(t, 1, atomic.LoadInt32(&repo.resetForRetryCalls))
		})
	}
}
