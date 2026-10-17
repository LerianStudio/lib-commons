//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

type claimScopeSeed struct {
	pending, failed, stuck, exhausted uuid.UUID
}

// seedClaimScope stores one event of eventType in each claimable state, aged past every
// retry and processing window.
func seedClaimScope(t *testing.T, fx *integrationRepoFixture, eventType string) claimScopeSeed {
	t.Helper()

	stale := time.Now().UTC().Add(-time.Hour)
	ids := claimScopeSeed{
		pending:   createFixtureEvent(t, fx, eventType).ID,
		failed:    createFixtureEvent(t, fx, eventType).ID,
		stuck:     createFixtureEvent(t, fx, eventType).ID,
		exhausted: createFixtureEvent(t, fx, eventType).ID,
	}
	updateFixtureEventState(t, fx, ids.failed, outbox.OutboxStatusFailed, 1, stale)
	updateFixtureEventState(t, fx, ids.stuck, outbox.OutboxStatusProcessing, 1, stale)
	updateFixtureEventState(t, fx, ids.exhausted, outbox.OutboxStatusProcessing, 2, stale)

	return ids
}

// requireClaimScopeSeed asserts the seed is untouched, or fully dispatched: published, or
// INVALID once the reclaim spends the last attempt.
func requireClaimScopeSeed(t *testing.T, fx *integrationRepoFixture, seed claimScopeSeed, delivered bool) {
	t.Helper()

	type state struct {
		status   string
		attempts int
	}

	want := map[uuid.UUID]state{
		seed.pending:   {outbox.OutboxStatusPending, 0},
		seed.failed:    {outbox.OutboxStatusFailed, 1},
		seed.stuck:     {outbox.OutboxStatusProcessing, 1},
		seed.exhausted: {outbox.OutboxStatusProcessing, 2},
	}
	if delivered {
		want = map[uuid.UUID]state{
			seed.pending:   {outbox.OutboxStatusPublished, 0},
			seed.failed:    {outbox.OutboxStatusPublished, 1},
			seed.stuck:     {outbox.OutboxStatusPublished, 2},
			seed.exhausted: {outbox.OutboxStatusInvalid, 3},
		}
	}

	for id, expected := range want {
		stored, err := fx.repo.GetByID(fx.tenantCtx, id)
		require.NoError(t, err)
		require.Equal(t, expected, state{stored.Status, stored.Attempts})
	}
}

func claimScopeHandlers(t *testing.T, eventType string) *outbox.HandlerRegistry {
	t.Helper()

	handlers := outbox.NewHandlerRegistry()
	require.NoError(t, handlers.Register(eventType, func(context.Context, *outbox.OutboxEvent) error { return nil }))

	return handlers
}

func dispatchClaimScope(
	t *testing.T,
	fx *integrationRepoFixture,
	handlers *outbox.HandlerRegistry,
	opts ...outbox.DispatcherOption,
) outbox.DispatchResult {
	t.Helper()

	opts = append([]outbox.DispatcherOption{outbox.WithMaxDispatchAttempts(3), outbox.WithPublishMaxAttempts(1)}, opts...)
	dispatcher, err := outbox.NewDispatcher(fx.repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), opts...)
	require.NoError(t, err)

	return dispatcher.DispatchOnceResult(fx.tenantCtx)
}

// Two deployments share one table: a dispatcher scoped to its handlers claims,
// retries and reclaims only its own event type; an unscoped one takes the rest.
func TestIntegration_Dispatcher_ClaimScopedToHandlersCoversEveryClaimDoor(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	served, unserved := seedClaimScope(t, fx, "payment.served"), seedClaimScope(t, fx, "payment.unserved")
	scoped, servedHandlers := outbox.WithClaimScopedToHandlers(), claimScopeHandlers(t, "payment.served")

	require.Zero(t, dispatchClaimScope(t, fx, outbox.NewHandlerRegistry(), scoped).Processed, "no handler claims nothing")
	require.Zero(t, dispatchClaimScope(t, fx, servedHandlers, scoped, outbox.WithPriorityEventTypes("payment.unserved")).Processed,
		"the priority door must not widen the claim scope")

	require.Equal(t, 3, dispatchClaimScope(t, fx, servedHandlers, scoped).Published)
	requireClaimScopeSeed(t, fx, served, true)
	requireClaimScopeSeed(t, fx, unserved, false)

	require.Equal(t, 3, dispatchClaimScope(t, fx, claimScopeHandlers(t, "payment.unserved")).Published)
	requireClaimScopeSeed(t, fx, unserved, true)
}

// Priority types alone scope a dispatcher on a shared table: it still retries its FAILED
// events and reclaims its stuck ones, and never claims another type's events.
func TestIntegration_Dispatcher_PriorityTypesKeepRecoveryInScope(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	owned, foreign := seedClaimScope(t, fx, "payment.owned"), seedClaimScope(t, fx, "payment.foreign")

	result := dispatchClaimScope(t, fx, claimScopeHandlers(t, "payment.owned"), outbox.WithPriorityEventTypes("payment.owned"))
	require.Equal(t, 3, result.Published)
	requireClaimScopeSeed(t, fx, owned, true)
	requireClaimScopeSeed(t, fx, foreign, false)
}
