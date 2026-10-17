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

// Two deployments share one table: a dispatcher scoped to its handlers claims,
// retries and reclaims only its own event type; an unscoped one takes the rest.
func TestIntegration_Dispatcher_ClaimScopedToHandlersCoversEveryClaimDoor(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	stale := time.Now().UTC().Add(-time.Hour)

	seed := func(eventType string) claimScopeSeed {
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

	served, unserved := seed("payment.served"), seed("payment.unserved")

	requireState := func(id uuid.UUID, status string, attempts int) {
		t.Helper()

		stored, err := fx.repo.GetByID(fx.tenantCtx, id)
		require.NoError(t, err)
		require.Equal(t, status, stored.Status)
		require.Equal(t, attempts, stored.Attempts)
	}

	handlersFor := func(eventType string) *outbox.HandlerRegistry {
		handlers := outbox.NewHandlerRegistry()
		require.NoError(t, handlers.Register(eventType, func(context.Context, *outbox.OutboxEvent) error { return nil }))

		return handlers
	}

	dispatch := func(handlers *outbox.HandlerRegistry, opts ...outbox.DispatcherOption) outbox.DispatchResult {
		t.Helper()

		opts = append([]outbox.DispatcherOption{outbox.WithMaxDispatchAttempts(3), outbox.WithPublishMaxAttempts(1)}, opts...)
		dispatcher, err := outbox.NewDispatcher(fx.repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), opts...)
		require.NoError(t, err)

		return dispatcher.DispatchOnceResult(fx.tenantCtx)
	}

	scoped, servedHandlers := outbox.WithClaimScopedToHandlers(), handlersFor("payment.served")

	require.Zero(t, dispatch(outbox.NewHandlerRegistry(), scoped).Processed, "no handler claims nothing")
	require.Zero(t, dispatch(servedHandlers, scoped, outbox.WithPriorityEventTypes("payment.unserved")).Processed,
		"the priority door must not widen the claim scope")

	require.Equal(t, 3, dispatch(servedHandlers, scoped).Published)
	requireState(served.pending, outbox.OutboxStatusPublished, 0)
	requireState(served.failed, outbox.OutboxStatusPublished, 1)
	requireState(served.stuck, outbox.OutboxStatusPublished, 2)
	requireState(served.exhausted, outbox.OutboxStatusInvalid, 3)
	requireState(unserved.pending, outbox.OutboxStatusPending, 0)
	requireState(unserved.failed, outbox.OutboxStatusFailed, 1)
	requireState(unserved.stuck, outbox.OutboxStatusProcessing, 1)
	requireState(unserved.exhausted, outbox.OutboxStatusProcessing, 2)

	require.Equal(t, 3, dispatch(handlersFor("payment.unserved")).Published)
	requireState(unserved.pending, outbox.OutboxStatusPublished, 0)
	requireState(unserved.failed, outbox.OutboxStatusPublished, 1)
	requireState(unserved.stuck, outbox.OutboxStatusPublished, 2)
	requireState(unserved.exhausted, outbox.OutboxStatusInvalid, 3)
}
