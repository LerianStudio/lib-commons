//go:build integration

package mongo

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

const integrationTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestIntegration_TraceContext_RoundTrip(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	carrier := map[string]string{
		outbox.TraceContextTraceparent: integrationTraceparent,
		outbox.TraceContextTracestate:  "vendor=1",
	}

	event, err := outbox.NewOutboxEvent(
		fx.tenantCtx,
		"payment.created",
		uuid.New(),
		[]byte(`{"amount":100}`),
		outbox.WithTraceCarrier(carrier),
	)
	require.NoError(t, err)

	created, err := fx.repo.Create(fx.tenantCtx, event)
	require.NoError(t, err)
	require.Equal(t, carrier, created.TraceContext)

	fetched, err := fx.repo.GetByID(fx.tenantCtx, created.ID)
	require.NoError(t, err)
	require.Equal(t, carrier, fetched.TraceContext)

	pending, err := fx.repo.ListPending(fx.tenantCtx, 10)
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

func TestIntegration_TraceContext_AbsentWhenEventHasNoCarrier(t *testing.T) {
	fx := newIntegrationRepoFixture(t)

	event, err := outbox.NewOutboxEvent(
		fx.tenantCtx,
		"payment.created",
		uuid.New(),
		[]byte(`{"amount":100}`),
	)
	require.NoError(t, err)

	created, err := fx.repo.Create(fx.tenantCtx, event)
	require.NoError(t, err)
	require.Nil(t, created.TraceContext)

	fetched, err := fx.repo.GetByID(fx.tenantCtx, created.ID)
	require.NoError(t, err)
	require.Nil(t, fetched.TraceContext)
}
