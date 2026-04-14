//go:build unit

package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTelemetry_UnmatchedRouteDoesNotPanic(t *testing.T) {
	t.Parallel()

	tp, spanRecorder := setupTestTracer()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	telemetry := &opentelemetry.Telemetry{
		TelemetryConfig: opentelemetry.TelemetryConfig{LibraryName: "test-library", EnableTelemetry: true},
		TracerProvider:  tp,
	}

	app := fiber.New()
	app.Use(NewTelemetryMiddleware(telemetry).WithTelemetry(telemetry))

	assert.NotPanics(t, func() {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/missing", nil))
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	assert.NotEmpty(t, spanRecorder.Ended())
}
