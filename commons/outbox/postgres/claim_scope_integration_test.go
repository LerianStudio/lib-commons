//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
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

// A scoped dispatcher on a shared table requeues only its own INVALID events: another
// service's INVALID event is refused and left INVALID for that service's operator.
func TestIntegration_Dispatcher_RequeueStaysInClaimScope(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	owned, foreign := createFixtureEvent(t, fx, "payment.owned"), createFixtureEvent(t, fx, "payment.foreign")
	updateFixtureEventState(t, fx, owned.ID, outbox.OutboxStatusInvalid, 3, time.Now().UTC())
	updateFixtureEventState(t, fx, foreign.ID, outbox.OutboxStatusInvalid, 3, time.Now().UTC())

	dispatcher, err := outbox.NewDispatcher(fx.repo, claimScopeHandlers(t, "payment.owned"), nil,
		noop.NewTracerProvider().Tracer("test"), outbox.WithClaimScopedToHandlers())
	require.NoError(t, err)

	require.ErrorIs(t, dispatcher.Requeue(fx.tenantCtx, foreign.ID), outbox.ErrOutboxEventNotRequeueable)
	require.NoError(t, dispatcher.Requeue(fx.tenantCtx, owned.ID))
	require.Equal(t, 1, dispatcher.DispatchOnceResult(fx.tenantCtx).Published)

	for id, want := range map[uuid.UUID]string{owned.ID: outbox.OutboxStatusPublished, foreign.ID: outbox.OutboxStatusInvalid} {
		stored, err := fx.repo.GetByID(fx.tenantCtx, id)
		require.NoError(t, err)
		require.Equal(t, want, stored.Status)
	}
}

// On a shared table a scoped dispatcher's oldest-age gauge reads only its own waiting events:
// another service's older FAILED backlog never raises it.
func TestIntegration_Dispatcher_OldestWaitingAgeStaysInClaimScope(t *testing.T) {
	fx := newIntegrationRepoFixture(t)
	owned, foreign := createFixtureEvent(t, fx, "payment.owned"), createFixtureEvent(t, fx, "payment.foreign")
	now := time.Now().UTC()

	// FAILED inside the retry window, so the cycle leaves both waiting.
	for id, age := range map[uuid.UUID]time.Duration{owned.ID: 2 * time.Hour, foreign.ID: 5 * time.Hour} {
		updateFixtureEventState(t, fx, id, outbox.OutboxStatusFailed, 1, now)
		_, err := fx.primaryDB.ExecContext(fx.ctx,
			fmt.Sprintf("UPDATE %s SET created_at = $1 WHERE id = $2", quoteIdentifier(fx.tableName)), now.Add(-age), id)
		require.NoError(t, err)
	}

	// A second handled type sorts first, so the owned rows are probed through the second type parameter.
	handlers := claimScopeHandlers(t, "payment.owned")
	require.NoError(t, handlers.Register("payment.other", func(context.Context, *outbox.OutboxEvent) error { return nil }))

	reader := sdkmetric.NewManualReader()
	dispatcher, err := outbox.NewDispatcher(fx.repo, handlers, nil, noop.NewTracerProvider().Tracer("test"),
		outbox.WithClaimScopedToHandlers(), outbox.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(fx.ctx)
	done := make(chan error, 1)

	go func() { done <- dispatcher.RunContext(runCtx, nil) }()

	var age int64

	require.Eventually(t, func() bool {
		var rm metricdata.ResourceMetrics
		if reader.Collect(fx.ctx, &rm) != nil {
			return false
		}

		for _, scope := range rm.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if gauge, ok := metric.Data.(metricdata.Gauge[int64]); ok && metric.Name == "outbox.queue.oldest_age" && len(gauge.DataPoints) == 1 {
					age = gauge.DataPoints[0].Value

					return true
				}
			}
		}

		return false
	}, 10*time.Second, 20*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
	require.InDelta(t, (2 * time.Hour).Seconds(), float64(age), 60, "the foreign 5h backlog is outside the claim scope")
}
