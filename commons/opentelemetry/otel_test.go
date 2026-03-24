//go:build unit

package opentelemetry

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"
)

func unsetEnvVar(t *testing.T, key string) {
	t.Helper()

	original, present := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		if present {
			require.NoError(t, os.Setenv(key, original))
			return
		}

		require.NoError(t, os.Unsetenv(key))
	})
}

// ===========================================================================
// 1. NewTelemetry validation
// ===========================================================================

func TestNewTelemetry_NilLogger(t *testing.T) {
	t.Parallel()

	tl, err := NewTelemetry(TelemetryConfig{
		EnableTelemetry: false,
	})
	require.ErrorIs(t, err, ErrNilTelemetryLogger)
	assert.Nil(t, tl)
}

func TestNewTelemetry_EnabledEmptyEndpoint(t *testing.T) {
	t.Parallel()

	tl, err := NewTelemetry(TelemetryConfig{
		EnableTelemetry: true,
		LibraryName:     "test-lib",
		Logger:          log.NewNop(),
	})
	require.ErrorIs(t, err, ErrEmptyEndpoint)
	require.NotNil(t, tl, "must return noop Telemetry to prevent goroutine leaks")
	assert.NotNil(t, tl.TracerProvider)
	assert.NotNil(t, tl.MeterProvider)
	assert.NotNil(t, tl.LoggerProvider)
	assert.NotNil(t, tl.MetricsFactory)
}

func TestNewTelemetry_EnabledWhitespaceEndpoint(t *testing.T) {
	t.Parallel()

	tl, err := NewTelemetry(TelemetryConfig{
		EnableTelemetry:           true,
		CollectorExporterEndpoint: "   ",
		LibraryName:               "test-lib",
		Logger:                    log.NewNop(),
	})
	require.ErrorIs(t, err, ErrEmptyEndpoint)
	require.NotNil(t, tl, "must return noop Telemetry to prevent goroutine leaks")
	assert.NotNil(t, tl.TracerProvider)
	assert.NotNil(t, tl.MeterProvider)
	assert.NotNil(t, tl.LoggerProvider)
	assert.NotNil(t, tl.MetricsFactory)
}

func TestNewTelemetry_EnabledEmptyEndpoint_SetsGlobalNoopProviders(t *testing.T) {
	// Not parallel: mutates global OTEL providers.
	prevTP := otel.GetTracerProvider()
	prevMP := otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetMeterProvider(prevMP)
	})

	tl, err := NewTelemetry(TelemetryConfig{
		EnableTelemetry: true,
		LibraryName:     "test-lib",
		Logger:          log.NewNop(),
	})
	require.ErrorIs(t, err, ErrEmptyEndpoint)
	require.NotNil(t, tl)

	// Verify that noop providers were installed as globals, preventing
	// downstream libraries from spawning real gRPC exporters.
	assert.Same(t, tl.TracerProvider, otel.GetTracerProvider(),
		"global tracer provider must be the noop instance")
	assert.Same(t, tl.MeterProvider, otel.GetMeterProvider(),
		"global meter provider must be the noop instance")
}

func TestNewTelemetry_DisabledReturnsNoopProviders(t *testing.T) {
	t.Parallel()

	tl, err := NewTelemetry(TelemetryConfig{
		LibraryName:     "test-lib",
		ServiceName:     "test-svc",
		ServiceVersion:  "0.1.0",
		DeploymentEnv:   "test",
		EnableTelemetry: false,
		Logger:          log.NewNop(),
	})
	require.NoError(t, err)
	require.NotNil(t, tl)
	assert.NotNil(t, tl.TracerProvider)
	assert.NotNil(t, tl.MeterProvider)
	assert.NotNil(t, tl.LoggerProvider)
	assert.NotNil(t, tl.MetricsFactory)
	assert.NotNil(t, tl.Redactor)
	assert.NotNil(t, tl.Propagator)
}

func TestNewTelemetry_DefaultPropagatorAndRedactor(t *testing.T) {
	t.Parallel()

	tl, err := NewTelemetry(TelemetryConfig{
		LibraryName:     "test-lib",
		EnableTelemetry: false,
		Logger:          log.NewNop(),
	})
	require.NoError(t, err)
	assert.NotNil(t, tl.Propagator, "default propagator should be set")
	assert.NotNil(t, tl.Redactor, "default redactor should be set")
}

func TestNewTelemetry_DeploymentEnvControlsSecurityPolicy(t *testing.T) {
	t.Run("explicit production env blocks insecure exporter even when ambient env is permissive", func(t *testing.T) {
		t.Setenv("ENV_NAME", commons.Local.String())
		t.Setenv("ENV", "")
		t.Setenv("GO_ENV", "")
		t.Setenv(commons.EnvSecurityTier, "")
		t.Setenv(commons.EnvSecurityEnforcement, "true")
		unsetEnvVar(t, commons.EnvAllowInsecureOTEL)

		tl, err := NewTelemetry(TelemetryConfig{
			LibraryName:               "test-lib",
			EnableTelemetry:           true,
			DeploymentEnv:             "production",
			CollectorExporterEndpoint: "http://collector:4317",
			Logger:                    log.NewNop(),
		})
		require.Error(t, err)
		assert.Nil(t, tl)
		assert.ErrorIs(t, err, commons.ErrSecurityViolation)
	})

	t.Run("explicit local env allows insecure exporter even when ambient env is strict", func(t *testing.T) {
		t.Setenv("ENV_NAME", commons.Production.String())
		t.Setenv("ENV", "")
		t.Setenv("GO_ENV", "")
		t.Setenv(commons.EnvSecurityTier, "")
		t.Setenv(commons.EnvSecurityEnforcement, "true")
		unsetEnvVar(t, commons.EnvAllowInsecureOTEL)

		tl, err := NewTelemetry(TelemetryConfig{
			LibraryName:               "test-lib",
			EnableTelemetry:           true,
			DeploymentEnv:             "local",
			CollectorExporterEndpoint: "http://collector:4317",
			Logger:                    log.NewNop(),
		})
		require.NoError(t, err)
		require.NotNil(t, tl)
		require.NotNil(t, tl.shutdownCtx)
		_ = tl.ShutdownTelemetryWithContext(context.Background())
	})
}

// ===========================================================================
// 1b. Endpoint normalization
// ===========================================================================

func TestNewTelemetry_EndpointNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		endpoint         string
		wantEndpoint     string
		wantInsecure     bool
		insecureOverride bool // initial InsecureExporter value
	}{
		{
			name:         "http scheme stripped and insecure inferred",
			endpoint:     "http://otel-collector:4317",
			wantEndpoint: "otel-collector:4317",
			wantInsecure: true,
		},
		{
			name:         "https scheme stripped and insecure stays false",
			endpoint:     "https://otel-collector:4317",
			wantEndpoint: "otel-collector:4317",
			wantInsecure: false,
		},
		{
			name:         "no scheme defaults to insecure",
			endpoint:     "otel-collector:4317",
			wantEndpoint: "otel-collector:4317",
			wantInsecure: true,
		},
		{
			name:             "https with explicit insecure override preserved",
			endpoint:         "https://otel-collector:4317",
			insecureOverride: true,
			wantEndpoint:     "otel-collector:4317",
			wantInsecure:     true,
		},
		{
			name:         "http with trailing slash",
			endpoint:     "http://otel-collector:4317/",
			wantEndpoint: "otel-collector:4317/",
			wantInsecure: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Use telemetry disabled so we don't need a real collector.
			tl, err := NewTelemetry(TelemetryConfig{
				LibraryName:               "test-lib",
				EnableTelemetry:           false,
				CollectorExporterEndpoint: tt.endpoint,
				InsecureExporter:          tt.insecureOverride,
				Logger:                    log.NewNop(),
			})
			require.NoError(t, err)
			require.NotNil(t, tl)
			assert.Equal(t, tt.wantEndpoint, tl.CollectorExporterEndpoint,
				"endpoint should be normalized")
			assert.Equal(t, tt.wantInsecure, tl.InsecureExporter,
				"InsecureExporter should be inferred from scheme")
		})
	}
}

// ===========================================================================
// 1c. Endpoint environment variable normalization
// ===========================================================================

func TestNormalizeEndpointEnvVars(t *testing.T) {
	envKeys := []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	}

	tests := []struct {
		name     string
		value    string
		set      bool
		expected string
	}{
		{
			name:     "bare host:port gets http scheme",
			value:    "10.10.0.202:4317",
			set:      true,
			expected: "http://10.10.0.202:4317",
		},
		{
			name:     "hostname:port gets http scheme",
			value:    "otel-collector:4317",
			set:      true,
			expected: "http://otel-collector:4317",
		},
		{
			name:     "http scheme preserved",
			value:    "http://otel-collector:4317",
			set:      true,
			expected: "http://otel-collector:4317",
		},
		{
			name:     "https scheme preserved",
			value:    "https://otel-collector:4317",
			set:      true,
			expected: "https://otel-collector:4317",
		},
		{
			name:     "whitespace trimmed before adding scheme",
			value:    "  10.10.0.202:4317  ",
			set:      true,
			expected: "http://10.10.0.202:4317",
		},
		{
			name:     "empty value skipped",
			value:    "",
			set:      true,
			expected: "",
		},
		{
			name:     "whitespace-only value skipped",
			value:    "   ",
			set:      true,
			expected: "   ",
		},
		{
			name:     "unset env var skipped",
			value:    "",
			set:      false,
			expected: "",
		},
	}

	for _, tt := range tests {
		for _, key := range envKeys {
			t.Run(tt.name+"/"+key, func(t *testing.T) {
				if tt.set {
					t.Setenv(key, tt.value)
				}

				normalizeEndpointEnvVars()

				got := os.Getenv(key)
				assert.Equal(t, tt.expected, got)
			})
		}
	}
}

// ===========================================================================
// 2. Telemetry methods on nil receiver
// ===========================================================================

func TestTelemetry_ApplyGlobals_NilReceiver(t *testing.T) {
	t.Parallel()

	var tl *Telemetry
	err := tl.ApplyGlobals()
	require.ErrorIs(t, err, ErrNilTelemetry)
}

func TestTelemetry_Tracer_NilReceiver(t *testing.T) {
	t.Parallel()

	var tl *Telemetry
	tr, err := tl.Tracer("test")
	require.ErrorIs(t, err, ErrNilTelemetry)
	assert.Nil(t, tr)
}

func TestTelemetry_Meter_NilReceiver(t *testing.T) {
	t.Parallel()

	var tl *Telemetry
	m, err := tl.Meter("test")
	require.ErrorIs(t, err, ErrNilTelemetry)
	assert.Nil(t, m)
}

func TestTelemetry_ShutdownTelemetry_NilReceiver(t *testing.T) {
	t.Parallel()

	var tl *Telemetry
	assert.NotPanics(t, func() { tl.ShutdownTelemetry() })
}

func TestTelemetry_ShutdownTelemetryWithContext_NilReceiver(t *testing.T) {
	t.Parallel()

	var tl *Telemetry
	err := tl.ShutdownTelemetryWithContext(context.Background())
	require.ErrorIs(t, err, ErrNilTelemetry)
}

// ===========================================================================
// 3. Telemetry with disabled telemetry — provider access
// ===========================================================================

func newDisabledTelemetry(t *testing.T) *Telemetry {
	t.Helper()

	tl, err := NewTelemetry(TelemetryConfig{
		LibraryName:     "test-lib",
		ServiceName:     "test-svc",
		ServiceVersion:  "0.1.0",
		EnableTelemetry: false,
		Logger:          log.NewNop(),
	})
	require.NoError(t, err)

	return tl
}

func TestTelemetry_Disabled_Tracer(t *testing.T) {
	t.Parallel()

	tl := newDisabledTelemetry(t)
	tr, err := tl.Tracer("test-tracer")
	require.NoError(t, err)
	assert.NotNil(t, tr)
}

func TestTelemetry_Disabled_Meter(t *testing.T) {
	t.Parallel()

	tl := newDisabledTelemetry(t)
	m, err := tl.Meter("test-meter")
	require.NoError(t, err)
	assert.NotNil(t, m)
}

func TestTelemetry_Disabled_ShutdownWithContext(t *testing.T) {
	t.Parallel()

	tl := newDisabledTelemetry(t)
	err := tl.ShutdownTelemetryWithContext(context.Background())
	require.NoError(t, err)
}

func TestTelemetry_Disabled_ShutdownTelemetry(t *testing.T) {
	t.Parallel()

	tl := newDisabledTelemetry(t)
	assert.NotPanics(t, func() { tl.ShutdownTelemetry() })
}

func TestTelemetry_Disabled_ApplyGlobals(t *testing.T) {
	prevTP := otel.GetTracerProvider()
	prevMP := otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetMeterProvider(prevMP)
	})

	tl := newDisabledTelemetry(t)
	require.NoError(t, tl.ApplyGlobals())
	assert.Same(t, tl.TracerProvider, otel.GetTracerProvider())
	assert.Same(t, tl.MeterProvider, otel.GetMeterProvider())
}

// ===========================================================================
// 4. ShutdownTelemetryWithContext — nil shutdown functions
// ===========================================================================

func TestTelemetry_ShutdownWithContext_NilShutdownFuncs(t *testing.T) {
	t.Parallel()

	tl := &Telemetry{
		TelemetryConfig: TelemetryConfig{Logger: log.NewNop()},
		shutdown:        nil,
		shutdownCtx:     nil,
	}

	err := tl.ShutdownTelemetryWithContext(context.Background())
	require.ErrorIs(t, err, ErrNilShutdown)
}

func TestTelemetry_ShutdownWithContext_FallbackToShutdown(t *testing.T) {
	t.Parallel()

	called := false
	tl := &Telemetry{
		TelemetryConfig: TelemetryConfig{Logger: log.NewNop()},
		shutdown:        func() { called = true },
		shutdownCtx:     nil,
	}

	err := tl.ShutdownTelemetryWithContext(context.Background())
	require.NoError(t, err)
	assert.True(t, called, "fallback shutdown should have been invoked")
}

// ===========================================================================
// 5. Context propagation helpers — nil/empty edge cases
// ===========================================================================

func TestInjectTraceContext_NilCarrier(t *testing.T) {
	t.Parallel()
	assert.NotPanics(t, func() { InjectTraceContext(context.Background(), nil) })
}

func TestExtractTraceContext_NilCarrier(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	result := ExtractTraceContext(ctx, nil)
	assert.Equal(t, ctx, result)
}

func TestInjectHTTPContext_NilHeaders(t *testing.T) {
	t.Parallel()
	assert.NotPanics(t, func() { InjectHTTPContext(context.Background(), nil) })
}

func TestInjectGRPCContext_NilMD(t *testing.T) {
	t.Parallel()

	md := InjectGRPCContext(context.Background(), nil)
	require.NotNil(t, md, "nil md should produce a new metadata.MD")
}

func TestExtractGRPCContext_NilMD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	result := ExtractGRPCContext(ctx, nil)
	assert.Equal(t, ctx, result)
}

func TestExtractGRPCContext_WithTraceparentKey(t *testing.T) {
	t.Parallel()

	md := metadata.MD{
		"traceparent": {"00-00112233445566778899aabbccddeeff-0123456789abcdef-01"},
	}
	ctx := ExtractGRPCContext(context.Background(), md)
	assert.NotNil(t, ctx)

	span := trace.SpanFromContext(ctx)
	assert.Equal(t, "00112233445566778899aabbccddeeff", span.SpanContext().TraceID().String())
}

func TestInjectQueueTraceContext_ReturnsMap(t *testing.T) {
	t.Parallel()

	headers := InjectQueueTraceContext(context.Background())
	require.NotNil(t, headers)
}

func TestExtractQueueTraceContext_NilHeaders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	result := ExtractQueueTraceContext(ctx, nil)
	assert.Equal(t, ctx, result)
}

func TestPrepareQueueHeaders_MergesHeaders(t *testing.T) {
	t.Parallel()

	base := map[string]any{"routing_key": "my.queue"}
	result := PrepareQueueHeaders(context.Background(), base)
	require.NotNil(t, result)
	assert.Equal(t, "my.queue", result["routing_key"])
}

func TestPrepareQueueHeaders_DoesNotMutateBase(t *testing.T) {
	t.Parallel()

	base := map[string]any{"key": "val"}
	result := PrepareQueueHeaders(context.Background(), base)
	assert.Len(t, base, 1)
	assert.NotSame(t, &base, &result)
}

func TestInjectTraceHeadersIntoQueue_NilPointer(t *testing.T) {
	t.Parallel()
	assert.NotPanics(t, func() { InjectTraceHeadersIntoQueue(context.Background(), nil) })
}

func TestInjectTraceHeadersIntoQueue_NilMap(t *testing.T) {
	t.Parallel()

	var headers map[string]any
	InjectTraceHeadersIntoQueue(context.Background(), &headers)
	require.NotNil(t, headers, "nil *map should be initialized")
}

func TestInjectTraceHeadersIntoQueue_ValidMap(t *testing.T) {
	t.Parallel()

	headers := map[string]any{"existing": "value"}
	InjectTraceHeadersIntoQueue(context.Background(), &headers)
	assert.Equal(t, "value", headers["existing"])
}

func TestExtractTraceContextFromQueueHeaders_EmptyHeaders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	result := ExtractTraceContextFromQueueHeaders(ctx, nil)
	assert.Equal(t, ctx, result)

	result = ExtractTraceContextFromQueueHeaders(ctx, map[string]any{})
	assert.Equal(t, ctx, result)
}

func TestExtractTraceContextFromQueueHeaders_NonStringValues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	headers := map[string]any{
		"traceparent": 12345,
		"other":       true,
	}
	result := ExtractTraceContextFromQueueHeaders(ctx, headers)
	assert.Equal(t, ctx, result, "non-string values should be skipped, returning original ctx")
}

func TestExtractTraceContextFromQueueHeaders_ValidHeaders(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
	otel.SetTextMapPropagator(propagation.TraceContext{})

	headers := map[string]any{
		"traceparent": "00-00112233445566778899aabbccddeeff-0123456789abcdef-01",
	}
	ctx := ExtractTraceContextFromQueueHeaders(context.Background(), headers)
	span := trace.SpanFromContext(ctx)
	assert.Equal(t, "00112233445566778899aabbccddeeff", span.SpanContext().TraceID().String())
}

// ===========================================================================
// 6. GetTraceIDFromContext / GetTraceStateFromContext
// ===========================================================================

func TestGetTraceIDFromContext_NoActiveSpan(t *testing.T) {
	t.Parallel()
	assert.Empty(t, GetTraceIDFromContext(context.Background()))
}

func TestGetTraceStateFromContext_NoActiveSpan(t *testing.T) {
	t.Parallel()
	assert.Empty(t, GetTraceStateFromContext(context.Background()))
}

func TestGetTraceIDFromContext_WithSpan(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	traceID := GetTraceIDFromContext(ctx)
	assert.NotEmpty(t, traceID)
	assert.Len(t, traceID, 32) // hex-encoded 16-byte trace ID
}

func TestGetTraceStateFromContext_WithSpan(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	// SDK-created spans have empty tracestate by default, which is valid.
	state := GetTraceStateFromContext(ctx)
	assert.NotNil(t, state) // zero-value string is fine
}

// ===========================================================================
// 7. flattenAttributes via BodyToSpanAttributes / BuildAttributesFromValue
// ===========================================================================

func TestFlattenAttributes_NestedMap(t *testing.T) {
	t.Parallel()

	attrs, err := BuildAttributesFromValue("root", map[string]any{
		"user": map[string]any{
			"name": "alice",
			"age":  float64(30),
		},
		"active": true,
	}, nil)
	require.NoError(t, err)

	m := attrsToMap(attrs)
	assert.Equal(t, "alice", m["root.user.name"])
	assert.Contains(t, m, "root.user.age")
	assert.Contains(t, m, "root.active")
}

func TestFlattenAttributes_Array(t *testing.T) {
	t.Parallel()

	attrs, err := BuildAttributesFromValue("items", map[string]any{
		"list": []any{"a", "b"},
	}, nil)
	require.NoError(t, err)

	m := attrsToMap(attrs)
	assert.Equal(t, "a", m["items.list.0"])
	assert.Equal(t, "b", m["items.list.1"])
}

func TestFlattenAttributes_NilValue(t *testing.T) {
	t.Parallel()

	attrs, err := BuildAttributesFromValue("prefix", nil, nil)
	require.NoError(t, err)
	assert.Nil(t, attrs)
}

func TestFlattenAttributes_StringTruncation(t *testing.T) {
	t.Parallel()

	longStr := strings.Repeat("x", maxSpanAttributeStringLength+500)
	attrs, err := BuildAttributesFromValue("k", map[string]any{"v": longStr}, nil)
	require.NoError(t, err)
	require.Len(t, attrs, 1)
	assert.Len(t, attrs[0].Value.AsString(), maxSpanAttributeStringLength)
}

func TestFlattenAttributes_DepthLimit(t *testing.T) {
	t.Parallel()

	// Build a deeply nested map exceeding maxAttributeDepth
	nested := map[string]any{"leaf": "value"}
	for i := 0; i < maxAttributeDepth+5; i++ {
		nested = map[string]any{"level": nested}
	}

	var attrs []attribute.KeyValue
	flattenAttributes(&attrs, "root", nested, 0)

	// The leaf should never appear because depth is exceeded
	for _, a := range attrs {
		assert.NotContains(t, string(a.Key), "leaf")
	}
}

func TestFlattenAttributes_CountLimit(t *testing.T) {
	t.Parallel()

	// Build a flat map with more than maxAttributeCount entries
	wide := make(map[string]any, maxAttributeCount+50)
	for i := 0; i < maxAttributeCount+50; i++ {
		wide[strings.Repeat("k", 3)+strings.Repeat("0", 4)+string(rune('a'+i%26))+strings.Repeat("0", 3)] = "v"
	}

	var attrs []attribute.KeyValue
	flattenAttributes(&attrs, "root", wide, 0)

	assert.LessOrEqual(t, len(attrs), maxAttributeCount)
}

func TestFlattenAttributes_JsonNumber(t *testing.T) {
	t.Parallel()

	// json.Number is produced when using a Decoder with UseNumber()
	attrs, err := BuildAttributesFromValue("n", map[string]any{
		"count": float64(42),
	}, nil)
	require.NoError(t, err)

	m := attrsToMap(attrs)
	assert.Contains(t, m, "n.count")
}

func TestFlattenAttributes_BoolValues(t *testing.T) {
	t.Parallel()

	attrs, err := BuildAttributesFromValue("cfg", map[string]any{
		"enabled": true,
		"debug":   false,
	}, nil)
	require.NoError(t, err)
	assert.Len(t, attrs, 2)
}

// ===========================================================================
// 8. sanitizeUTF8String
// ===========================================================================

func TestSanitizeUTF8String_ValidString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "hello world", sanitizeUTF8String("hello world"))
}

func TestSanitizeUTF8String_InvalidUTF8(t *testing.T) {
	t.Parallel()

	invalid := "hello\x80world"
	result := sanitizeUTF8String(invalid)
	assert.NotContains(t, result, "\x80")
	assert.Contains(t, result, "hello")
	assert.Contains(t, result, "world")
}

func TestSanitizeUTF8String_EmptyString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", sanitizeUTF8String(""))
}

func TestSanitizeUTF8String_Unicode(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "日本語テスト", sanitizeUTF8String("日本語テスト"))
}

// ===========================================================================
// 9. HandleSpan helpers
// ===========================================================================

func TestHandleSpanBusinessErrorEvent_NilSpan(t *testing.T) {
	t.Parallel()
	assert.NotPanics(t, func() { HandleSpanBusinessErrorEvent(nil, "evt", assert.AnError) })
}

func TestHandleSpanBusinessErrorEvent_NilError(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	assert.NotPanics(t, func() { HandleSpanBusinessErrorEvent(span, "evt", nil) })
}

func TestHandleSpanEvent_NilSpan(t *testing.T) {
	t.Parallel()
	assert.NotPanics(t, func() { HandleSpanEvent(nil, "evt") })
}

func TestHandleSpanError_NilSpan(t *testing.T) {
	t.Parallel()
	assert.NotPanics(t, func() { HandleSpanError(nil, "msg", assert.AnError) })
}

func TestHandleSpanError_NilError(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	assert.NotPanics(t, func() { HandleSpanError(span, "msg", nil) })
}

// ===========================================================================
// 10. SetSpanAttributesFromValue
// ===========================================================================

func TestSetSpanAttributesFromValue_NilSpan(t *testing.T) {
	t.Parallel()
	err := SetSpanAttributesFromValue(nil, "prefix", map[string]any{"k": "v"}, nil)
	assert.NoError(t, err)
}

func TestSetSpanAttributesFromValue_NilValue(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	err := SetSpanAttributesFromValue(span, "prefix", nil, nil)
	assert.NoError(t, err)
}

// ===========================================================================
// 11. BuildAttributesFromValue with redactor
// ===========================================================================

func TestBuildAttributesFromValue_WithRedactor(t *testing.T) {
	t.Parallel()

	r := NewDefaultRedactor()
	attrs, err := BuildAttributesFromValue("req", map[string]any{
		"username": "alice",
		"password": "secret123",
	}, r)
	require.NoError(t, err)

	m := attrsToMap(attrs)
	assert.Equal(t, "alice", m["req.username"])
	assert.NotEqual(t, "secret123", m["req.password"], "password should be redacted")
}

func TestBuildAttributesFromValue_StructInput(t *testing.T) {
	t.Parallel()

	type payload struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	attrs, err := BuildAttributesFromValue("obj", payload{ID: "123", Name: "test"}, nil)
	require.NoError(t, err)

	m := attrsToMap(attrs)
	assert.Equal(t, "123", m["obj.id"])
	assert.Equal(t, "test", m["obj.name"])
}

// ===========================================================================
// 12. isNilShutdownable
// ===========================================================================

func TestIsNilShutdownable_UntypedNil(t *testing.T) {
	t.Parallel()
	assert.True(t, isNilShutdownable(nil))
}

func TestIsNilShutdownable_TypedNil(t *testing.T) {
	t.Parallel()

	var tp *sdktrace.TracerProvider
	assert.True(t, isNilShutdownable(tp))
}

func TestIsNilShutdownable_ValidValue(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	assert.False(t, isNilShutdownable(tp))
}

// ===========================================================================
// 13. InjectGRPCContext key normalization
// ===========================================================================

func TestInjectGRPCContext_TraceparentKeyNormalization(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
	otel.SetTextMapPropagator(propagation.TraceContext{})

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	md := InjectGRPCContext(ctx, nil)
	// The function should normalize "Traceparent" -> "traceparent"
	assert.NotEmpty(t, md.Get("traceparent"), "traceparent key should be lowercase")
}

// ===========================================================================
// 14. Propagation round-trip
// ===========================================================================

func TestQueuePropagation_RoundTrip(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	prevTP := otel.GetTracerProvider()
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prev)
		otel.SetTracerProvider(prevTP)
	})

	otel.SetTextMapPropagator(propagation.TraceContext{})
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)

	ctx, span := tp.Tracer("test").Start(context.Background(), "producer")
	defer span.End()

	originalTraceID := span.SpanContext().TraceID().String()

	// Inject into queue headers
	queueHeaders := InjectQueueTraceContext(ctx)
	assert.NotEmpty(t, queueHeaders)

	// Extract on consumer side
	consumerCtx := ExtractQueueTraceContext(context.Background(), queueHeaders)
	extractedTraceID := GetTraceIDFromContext(consumerCtx)
	assert.Equal(t, originalTraceID, extractedTraceID)

	_ = tp.Shutdown(context.Background())
}

func TestHTTPPropagation_InjectAndVerify(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	prevTP := otel.GetTracerProvider()
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prev)
		otel.SetTracerProvider(prevTP)
	})

	otel.SetTextMapPropagator(propagation.TraceContext{})
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)

	ctx, span := tp.Tracer("test").Start(context.Background(), "http-req")
	defer span.End()

	headers := make(map[string][]string)
	InjectHTTPContext(ctx, headers)
	assert.NotEmpty(t, headers["Traceparent"])

	_ = tp.Shutdown(context.Background())
}

// ===========================================================================
// 15. buildShutdownHandlers
// ===========================================================================

func TestBuildShutdownHandlers_NoComponents(t *testing.T) {
	t.Parallel()

	shutdown, shutdownCtx := buildShutdownHandlers(log.NewNop())
	assert.NotPanics(t, func() { shutdown() })

	err := shutdownCtx(context.Background())
	assert.NoError(t, err)
}

func TestBuildShutdownHandlers_WithProviders(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	shutdown, shutdownCtx := buildShutdownHandlers(log.NewNop(), tp)

	err := shutdownCtx(context.Background())
	assert.NoError(t, err)

	// Second shutdown may error (already shut down), but should not panic
	assert.NotPanics(t, func() { shutdown() })
}

func TestBuildShutdownHandlers_NilComponents(t *testing.T) {
	t.Parallel()

	shutdown, shutdownCtx := buildShutdownHandlers(log.NewNop(), nil)
	assert.NotPanics(t, func() { shutdown() })

	err := shutdownCtx(context.Background())
	assert.NoError(t, err)
}

func TestBuildShutdownHandlers_TypedNilProvider(t *testing.T) {
	t.Parallel()

	var tp *sdktrace.TracerProvider
	shutdown, shutdownCtx := buildShutdownHandlers(log.NewNop(), tp)
	assert.NotPanics(t, func() { shutdown() })

	err := shutdownCtx(context.Background())
	assert.NoError(t, err)
}

// ===========================================================================
// 16. HandleSpan helpers with real spans
// ===========================================================================

func TestHandleSpanBusinessErrorEvent_WithSpan(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "op")

	HandleSpanBusinessErrorEvent(span, "business_error", assert.AnError)
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	require.NotEmpty(t, spans[0].Events, "business error event must be recorded")
	assert.Equal(t, "business_error", spans[0].Events[0].Name)
	// Status should remain OK (business errors don't set ERROR status)
	assert.Equal(t, codes.Unset, spans[0].Status.Code, "business error must not set ERROR status")
}

func TestHandleSpanEvent_WithSpan(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "op")

	HandleSpanEvent(span, "my_event", attribute.String("key", "value"))
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	require.NotEmpty(t, spans[0].Events, "event must be recorded on span")
	assert.Equal(t, "my_event", spans[0].Events[0].Name)
}

func TestHandleSpanError_WithSpan(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "op")

	HandleSpanError(span, "something failed", assert.AnError)
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code, "HandleSpanError must set ERROR status")
	assert.Contains(t, spans[0].Status.Description, "something failed")
}

func TestHandleSpanError_EmptyMessage(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "op")

	HandleSpanError(span, "", assert.AnError)
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	// With empty message, status should be just the error text (no leading ": ")
	assert.False(t, strings.HasPrefix(spans[0].Status.Description, ": "),
		"empty message must not produce leading ': ' in status description")
}

// ===========================================================================
// 17. ShutdownTelemetry (non-nil) exercises error branch
// ===========================================================================

func TestTelemetry_ShutdownTelemetry_NonNil(t *testing.T) {
	t.Parallel()

	tl := newDisabledTelemetry(t)
	assert.NotPanics(t, func() { tl.ShutdownTelemetry() })
}

// ===========================================================================
// 18. InjectGRPCContext / ExtractGRPCContext tracestate normalization
// ===========================================================================

func TestInjectGRPCContext_TracestateNormalization(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
	otel.SetTextMapPropagator(propagation.TraceContext{})

	traceID, _ := trace.TraceIDFromHex("00112233445566778899aabbccddeeff")
	spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
	ts := trace.TraceState{}
	ts, _ = ts.Insert("vendor", "val")

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		TraceState: ts,
		Remote:     true,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	md := InjectGRPCContext(ctx, nil)
	assert.NotEmpty(t, md.Get("traceparent"))
	assert.NotEmpty(t, md.Get("tracestate"))
	// Verify PascalCase keys are removed
	_, hasPascal := md["Traceparent"]
	assert.False(t, hasPascal)
}

func TestExtractGRPCContext_TracestateNormalization(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
	otel.SetTextMapPropagator(propagation.TraceContext{})

	md := metadata.MD{
		"traceparent": {"00-00112233445566778899aabbccddeeff-0123456789abcdef-01"},
		"tracestate":  {"vendor=val"},
	}
	ctx := ExtractGRPCContext(context.Background(), md)
	span := trace.SpanFromContext(ctx)
	assert.Equal(t, "00112233445566778899aabbccddeeff", span.SpanContext().TraceID().String())
}

// ===========================================================================
// 19. Processor OnStart/OnEnd via tracer pipeline
// ===========================================================================

func TestAttrBagSpanProcessor_OnStartOnEnd_WithTracer(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(AttrBagSpanProcessor{}))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	assert.NotNil(t, ctx)
}

func TestRedactingAttrBagSpanProcessor_OnStartOnEnd_WithTracer(t *testing.T) {
	t.Parallel()

	p := RedactingAttrBagSpanProcessor{Redactor: NewDefaultRedactor()}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	assert.NotNil(t, ctx)
}

func TestAttrBagSpanProcessor_OnStart_WithContextAttributes(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(AttrBagSpanProcessor{}),
		sdktrace.WithSyncer(exporter),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx := commons.ContextWithSpanAttributes(context.Background(), attribute.String("app.request.id", "r1"))
	_, span := tp.Tracer("test").Start(ctx, "op")
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	// Verify the context attribute was applied to the span
	found := false
	for _, a := range spans[0].Attributes {
		if a.Key == "app.request.id" && a.Value.AsString() == "r1" {
			found = true
		}
	}
	assert.True(t, found, "span must contain app.request.id=r1 from context bag")
}

func TestRedactingAttrBagSpanProcessor_OnStart_WithContextAttributes(t *testing.T) {
	t.Parallel()

	p := RedactingAttrBagSpanProcessor{Redactor: NewDefaultRedactor()}
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(p),
		sdktrace.WithSyncer(exporter),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx := commons.ContextWithSpanAttributes(context.Background(),
		attribute.String("app.request.id", "r1"),
		attribute.String("user.password", "secret"),
	)
	_, span := tp.Tracer("test").Start(ctx, "op")
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	// Verify the request ID is present and password is redacted
	for _, a := range spans[0].Attributes {
		if a.Key == "app.request.id" {
			assert.Equal(t, "r1", a.Value.AsString(), "non-sensitive field should pass through")
		}
		if a.Key == "user.password" {
			assert.NotEqual(t, "secret", a.Value.AsString(), "sensitive field should be redacted")
		}
	}
}

func TestRedactingAttrBagSpanProcessor_OnStart_NilRedactor(t *testing.T) {
	t.Parallel()

	p := RedactingAttrBagSpanProcessor{Redactor: nil}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	assert.NotNil(t, ctx)
}

// ===========================================================================
// 20. flattenAttributes edge case: default branch (non-primitive type)
// ===========================================================================

func TestFlattenAttributes_DefaultBranch(t *testing.T) {
	t.Parallel()

	// After JSON round-trip, custom types become primitives. Test directly
	// with a type that isn't map/slice/string/float64/bool/json.Number/nil.
	type custom struct{ X int }
	var attrs []attribute.KeyValue
	flattenAttributes(&attrs, "key", custom{X: 42}, 0)
	require.Len(t, attrs, 1)
	assert.Equal(t, "key", string(attrs[0].Key))
	assert.Contains(t, attrs[0].Value.AsString(), "42")
}

// ===========================================================================
// 21. newResource coverage
// ===========================================================================

func TestNewResource(t *testing.T) {
	t.Parallel()

	cfg := &TelemetryConfig{
		ServiceName:    "svc",
		ServiceVersion: "1.0",
		DeploymentEnv:  "test",
	}
	r := cfg.newResource()
	assert.NotNil(t, r)
}

// ===========================================================================
// 22. BuildAttributesFromValue error path
// ===========================================================================

func TestBuildAttributesFromValue_UnmarshalableValue(t *testing.T) {
	t.Parallel()

	// A channel cannot be JSON-marshaled
	ch := make(chan int)
	attrs, err := BuildAttributesFromValue("prefix", ch, nil)
	assert.Error(t, err)
	assert.Nil(t, attrs)
}

// ===========================================================================
// helpers
// ===========================================================================

func attrsToMap(attrs []attribute.KeyValue) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[string(a.Key)] = a.Value.Emit()
	}

	return m
}
