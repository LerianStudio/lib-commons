//go:build unit

package rabbitmq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestRabbitMQConnection_HealthCheck_StartsNoSpan pins that a health check
// exports no span. HealthCheck runs on context.Background many times a minute,
// so any span it opened would be a root trace of its own.
//
// Mutates the global TracerProvider: no t.Parallel().
func TestRabbitMQConnection_HealthCheck_StartsNoSpan(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = tp.Shutdown(context.Background())
	})

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"status":"ok"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(healthServer.Close)

	conn := &RabbitMQConnection{HealthCheckURL: healthServer.URL, Logger: obs.Nop()}

	healthy, err := conn.HealthCheck()
	require.NoError(t, err)
	assert.True(t, healthy)

	assert.Empty(t, recorder.Ended(), "a health check must not export a span")
}
