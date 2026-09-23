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
	"go.opentelemetry.io/otel/trace"
)

const integrationTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// addTraceContextColumn applies the 000002_outbox_trace_context migration to the
// fixture's table.
func addTraceContextColumn(t *testing.T, fixture *integrationRepoFixture) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := fixture.primaryDB.ExecContext(ctx, fmt.Sprintf(
		"ALTER TABLE %s ADD COLUMN IF NOT EXISTS trace_context JSONB NULL",
		quoteIdentifier(fixture.tableName),
	))
	require.NoError(t, err)
}

// traceAwareRepository builds a repository over the fixture's table with the
// optional trace context column enabled.
func traceAwareRepository(t *testing.T, fixture *integrationRepoFixture) *Repository {
	t.Helper()

	resolver, err := NewColumnResolver(
		fixture.client,
		WithColumnResolverTableName(fixture.tableName),
		WithColumnResolverTenantColumn("tenant_id"),
	)
	require.NoError(t, err)

	repo, err := NewRepository(
		fixture.client,
		resolver,
		resolver,
		WithTableName(fixture.tableName),
		WithTenantColumn("tenant_id"),
		WithTraceContextColumn(DefaultTraceContextColumn),
	)
	require.NoError(t, err)

	return repo
}

func TestIntegration_TraceContextColumn_RoundTrip(t *testing.T) {
	fixture := newIntegrationRepoFixture(t)
	addTraceContextColumn(t, fixture)

	repo := traceAwareRepository(t, fixture)
	carrier := map[string]string{
		outbox.TraceContextTraceparent: integrationTraceparent,
		outbox.TraceContextTracestate:  "vendor=1",
	}

	event, err := outbox.NewOutboxEvent(
		fixture.tenantCtx,
		"payment.created",
		uuid.New(),
		[]byte(`{"amount":100}`),
		outbox.WithTraceCarrier(carrier),
	)
	require.NoError(t, err)

	created, err := repo.Create(fixture.tenantCtx, event)
	require.NoError(t, err)
	require.Equal(t, carrier, created.TraceContext)

	fetched, err := repo.GetByID(fixture.tenantCtx, created.ID)
	require.NoError(t, err)
	require.Equal(t, carrier, fetched.TraceContext)

	pending, err := repo.ListPending(fixture.tenantCtx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, carrier, pending[0].TraceContext)

	restored, ok := outbox.RestoreTraceContext(context.Background(), pending[0].TraceContext)
	require.True(t, ok)
	require.Equal(
		t,
		"4bf92f3577b34da6a3ce929d0e0e4736",
		trace.SpanContextFromContext(restored).TraceID().String(),
	)
}

func TestIntegration_TraceContextColumn_NullWhenEventHasNoCarrier(t *testing.T) {
	fixture := newIntegrationRepoFixture(t)
	addTraceContextColumn(t, fixture)

	repo := traceAwareRepository(t, fixture)

	event, err := outbox.NewOutboxEvent(
		fixture.tenantCtx,
		"payment.created",
		uuid.New(),
		[]byte(`{"amount":100}`),
	)
	require.NoError(t, err)

	created, err := repo.Create(fixture.tenantCtx, event)
	require.NoError(t, err)
	require.Nil(t, created.TraceContext)

	fetched, err := repo.GetByID(fixture.tenantCtx, created.ID)
	require.NoError(t, err)
	require.Nil(t, fetched.TraceContext)

	var isNull bool

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, fixture.primaryDB.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT trace_context IS NULL FROM %s WHERE id = $1",
		quoteIdentifier(fixture.tableName),
	), created.ID).Scan(&isNull))
	require.True(t, isNull)
}

func TestIntegration_TraceContextColumn_BatchAndIdempotentWrites(t *testing.T) {
	fixture := newIntegrationRepoFixture(t)
	addTraceContextColumn(t, fixture)

	repo := traceAwareRepository(t, fixture)
	carrier := map[string]string{outbox.TraceContextTraceparent: integrationTraceparent}

	batch := make([]*outbox.OutboxEvent, 0, 2)

	for range 2 {
		event, err := outbox.NewOutboxEvent(
			fixture.tenantCtx,
			"payment.created",
			uuid.New(),
			[]byte(`{"amount":100}`),
			outbox.WithTraceCarrier(carrier),
		)
		require.NoError(t, err)

		batch = append(batch, event)
	}

	created, err := repo.CreateManyWithTx(fixture.tenantCtx, nil, batch)
	require.NoError(t, err)
	require.Len(t, created, 2)

	for _, event := range created {
		require.Equal(t, carrier, event.TraceContext)
	}

	idempotent, err := outbox.NewOutboxEvent(
		fixture.tenantCtx,
		"payment.created",
		uuid.New(),
		[]byte(`{"amount":200}`),
		outbox.WithTraceCarrier(carrier),
	)
	require.NoError(t, err)

	stored, err := repo.CreateIdempotentWithTx(fixture.tenantCtx, nil, idempotent)
	require.NoError(t, err)
	require.Equal(t, carrier, stored.TraceContext)

	replayed, err := repo.CreateIdempotentWithTx(fixture.tenantCtx, nil, idempotent)
	require.NoError(t, err)
	require.Equal(t, carrier, replayed.TraceContext)
}

// A repository left on the legacy column list must keep working against a table
// that already has the trace context column: the column is optional in both
// directions.
func TestIntegration_TraceContextColumn_IgnoredByUnconfiguredRepository(t *testing.T) {
	fixture := newIntegrationRepoFixture(t)
	addTraceContextColumn(t, fixture)

	event, err := outbox.NewOutboxEvent(
		fixture.tenantCtx,
		"payment.created",
		uuid.New(),
		[]byte(`{"amount":100}`),
		outbox.WithTraceCarrier(map[string]string{outbox.TraceContextTraceparent: integrationTraceparent}),
	)
	require.NoError(t, err)

	created, err := fixture.repo.Create(fixture.tenantCtx, event)
	require.NoError(t, err)
	require.Nil(t, created.TraceContext)

	pending, err := fixture.repo.ListPending(fixture.tenantCtx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Nil(t, pending[0].TraceContext)
}
