//go:build unit

package outbox

import (
	"context"
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

func (repo *claimScopedFakeRepo) ListPending(ctx context.Context, limit int) ([]*OutboxEvent, error) {
	types, ok := ClaimEventTypesFromContext(ctx)
	repo.calls = append(repo.calls, claimScopeCall{types: types, ok: ok})

	return repo.fakeRepo.ListPending(ctx, limit)
}

func TestNewDispatcher_ClaimScopedToHandlersRequiresCapability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repo    OutboxRepository
		wantErr error
	}{
		{"repository without the capability", &fakeRepo{}, ErrOutboxClaimScopeUnsupported},
		{"capable repository", &claimScopedFakeRepo{fakeRepo: &fakeRepo{}}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewDispatcher(tt.repo, NewHandlerRegistry(), nil, noop.NewTracerProvider().Tracer("test"),
				WithClaimScopedToHandlers())
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
