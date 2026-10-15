//go:build unit

package postgres

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestManager_RevalidateSettings_StartsNoRootTrace pins that pool settings
// revalidation, which runs on context.Background once per check interval, per
// tenant, per manager, exports no span: neither get_tenant_config nor the
// outbound HTTP CLIENT span may become a root trace of its own.
//
// Mutates the global TracerProvider: no t.Parallel().
func TestManager_RevalidateSettings_StartsNoRootTrace(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = tp.Shutdown(context.Background())
	})

	var requests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id": "tenant-trace", "service": "ledger", "status": "active"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	manager := NewManager(mustNewTestClient(t, server.URL), "ledger", WithModule("onboarding"))

	manager.revalidatePoolSettings("tenant-trace")

	assert.Equal(t, int32(1), requests.Load(), "revalidation must still fetch the tenant config")
	assert.Empty(t, recorder.Ended(), "background revalidation must not start a root trace")
}
