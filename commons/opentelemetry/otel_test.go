package opentelemetry

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/stretchr/testify/assert"
)

func TestInitializeTelemetryWithError_TelemetryDisabled(t *testing.T) {
	cfg := &TelemetryConfig{
		LibraryName:     "test-lib",
		ServiceName:     "test-service",
		ServiceVersion:  "1.0.0",
		DeploymentEnv:   "test",
		EnableTelemetry: false,
		Logger:          &log.NoneLogger{},
	}

	telemetry, err := InitializeTelemetryWithError(cfg)

	assert.NoError(t, err)
	assert.NotNil(t, telemetry)
	assert.NotNil(t, telemetry.TracerProvider)
	assert.NotNil(t, telemetry.MetricProvider)
	assert.NotNil(t, telemetry.LoggerProvider)
	assert.NotNil(t, telemetry.MetricsFactory)
}

func TestInitializeTelemetry_TelemetryDisabled(t *testing.T) {
	cfg := &TelemetryConfig{
		LibraryName:     "test-lib",
		ServiceName:     "test-service",
		ServiceVersion:  "1.0.0",
		DeploymentEnv:   "test",
		EnableTelemetry: false,
		Logger:          &log.NoneLogger{},
	}

	telemetry := InitializeTelemetry(cfg)

	assert.NotNil(t, telemetry)
	assert.NotNil(t, telemetry.TracerProvider)
	assert.NotNil(t, telemetry.MetricProvider)
	assert.NotNil(t, telemetry.LoggerProvider)
}

func TestInitializeTelemetryWithError_InvalidEndpoint(t *testing.T) {
	cfg := &TelemetryConfig{
		LibraryName:               "test-lib",
		ServiceName:               "test-service",
		ServiceVersion:            "1.0.0",
		DeploymentEnv:             "test",
		CollectorExporterEndpoint: "invalid-endpoint-that-does-not-exist:4317",
		EnableTelemetry:           true,
		Logger:                    &log.NoneLogger{},
	}

	// Note: The exporter creation might not fail immediately since gRPC uses lazy connection.
	// This test verifies the function handles the configuration correctly.
	telemetry, err := InitializeTelemetryWithError(cfg)

	// With gRPC lazy connection, this might succeed initially
	// The actual connection error would happen when trying to export
	if err != nil {
		assert.Nil(t, telemetry)
		assert.Contains(t, err.Error(), "can't initialize")
	} else {
		assert.NotNil(t, telemetry)
		// Clean up
		telemetry.ShutdownTelemetry()
	}
}
