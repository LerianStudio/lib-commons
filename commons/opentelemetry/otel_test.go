package opentelemetry

import (
	"errors"
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

func TestInitializeTelemetryWithError_NilConfig(t *testing.T) {
	telemetry, err := InitializeTelemetryWithError(nil)

	assert.Nil(t, telemetry)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrNilTelemetryConfig))
}

func TestInitializeTelemetryWithError_NilLogger(t *testing.T) {
	cfg := &TelemetryConfig{
		LibraryName:     "test-lib",
		ServiceName:     "test-service",
		ServiceVersion:  "1.0.0",
		DeploymentEnv:   "test",
		EnableTelemetry: false,
		Logger:          nil,
	}

	telemetry, err := InitializeTelemetryWithError(cfg)

	assert.Nil(t, telemetry)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrNilTelemetryLogger))
}

func TestInitializeTelemetryWithError_EnabledWithLazyConnection(t *testing.T) {
	// Note: gRPC uses lazy connection, so the exporter creation succeeds initially.
	// The actual connection error would happen when trying to export data.
	// This test verifies that InitializeTelemetryWithError handles valid configuration
	// without panicking and returns a functional Telemetry instance.
	cfg := &TelemetryConfig{
		LibraryName:               "test-lib",
		ServiceName:               "test-service",
		ServiceVersion:            "1.0.0",
		DeploymentEnv:             "test",
		CollectorExporterEndpoint: "localhost:4317",
		EnableTelemetry:           true,
		Logger:                    &log.NoneLogger{},
	}

	telemetry, err := InitializeTelemetryWithError(cfg)

	// With gRPC lazy connection, this should succeed
	assert.NoError(t, err)
	assert.NotNil(t, telemetry)
	assert.NotNil(t, telemetry.TracerProvider)
	assert.NotNil(t, telemetry.MetricProvider)
	assert.NotNil(t, telemetry.LoggerProvider)

	// Clean up
	telemetry.ShutdownTelemetry()
}
