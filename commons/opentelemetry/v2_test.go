//go:build unit

package opentelemetry

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"
)

func TestNewTelemetry_Disabled(t *testing.T) {
	tl, err := NewTelemetry(TelemetryConfig{
		LibraryName:     "test-lib",
		ServiceName:     "test-service",
		ServiceVersion:  "1.0.0",
		DeploymentEnv:   "test",
		EnableTelemetry: false,
		Logger:          &log.NopLogger{},
	})
	require.NoError(t, err)
	require.NotNil(t, tl)
	assert.NotNil(t, tl.TracerProvider)
	assert.NotNil(t, tl.MeterProvider)
	assert.NotNil(t, tl.MetricsFactory)
}

func TestSetSpanAttributesFromValue_FlattensAndRedacts(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(trace.WithSyncer(exporter))
	tracer := tp.Tracer("test")

	_, span := tracer.Start(context.Background(), "test-span")
	err := SetSpanAttributesFromValue(span, "request", map[string]any{
		"user": map[string]any{
			"id":       "u1",
			"password": "top-secret",
		},
		"amount": 12.3,
	}, NewDefaultRedactor())
	require.NoError(t, err)
	span.End()

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	attrs := spans[0].Attributes
	find := func(key string) string {
		for _, a := range attrs {
			if string(a.Key) == key {
				return a.Value.AsString()
			}
		}
		return ""
	}

	assert.Equal(t, "u1", find("request.user.id"))
	assert.NotEmpty(t, find("request.user.password"))
	assert.NotEqual(t, "top-secret", find("request.user.password"))

	if err := tp.Shutdown(context.Background()); err != nil {
		t.Errorf("tp.Shutdown failed: %v", err)
	}
}

func TestPropagation_HTTP_GRPC_Queue(t *testing.T) {
	prevPropagator := otel.GetTextMapPropagator()
	prevTracerProvider := otel.GetTracerProvider()
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prevPropagator)
		otel.SetTracerProvider(prevTracerProvider)
	})

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	tracer := tp.Tracer("test")

	ctx, span := tracer.Start(context.Background(), "root")
	defer span.End()

	headers := map[string][]string{}
	InjectHTTPContext(ctx, headers)
	assert.NotEmpty(t, headers["Traceparent"])

	md := InjectGRPCContext(ctx, nil)
	assert.NotEmpty(t, md.Get("traceparent"))

	queueHeaders := InjectQueueTraceContext(ctx)
	extracted := ExtractQueueTraceContext(context.Background(), queueHeaders)
	assert.Equal(t, span.SpanContext().TraceID().String(), oteltrace.SpanFromContext(extracted).SpanContext().TraceID().String())

	if err := tp.Shutdown(context.Background()); err != nil {
		t.Errorf("tp.Shutdown failed: %v", err)
	}
}

func TestApplyGlobalsRestoresProviders(t *testing.T) {
	prevPropagator := otel.GetTextMapPropagator()
	prevTracerProvider := otel.GetTracerProvider()
	prevMeterProvider := otel.GetMeterProvider()
	prevLoggerProvider := global.GetLoggerProvider()
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prevPropagator)
		otel.SetTracerProvider(prevTracerProvider)
		otel.SetMeterProvider(prevMeterProvider)
		global.SetLoggerProvider(prevLoggerProvider)
	})

	tl, err := NewTelemetry(TelemetryConfig{
		LibraryName:     "test-lib",
		ServiceName:     "test-service",
		ServiceVersion:  "1.0.0",
		DeploymentEnv:   "test",
		EnableTelemetry: false,
		Logger:          &log.NopLogger{},
	})
	require.NoError(t, err)

	require.NoError(t, tl.ApplyGlobals())

	assert.Same(t, tl.TracerProvider, otel.GetTracerProvider())
	assert.Same(t, tl.MeterProvider, otel.GetMeterProvider())
	assert.Same(t, tl.LoggerProvider, global.GetLoggerProvider())
}

func TestObfuscateStruct_Actions(t *testing.T) {
	redactor, err := NewRedactor([]RedactionRule{
		{FieldPattern: `(?i)^password$`, Action: RedactionMask},
		{FieldPattern: `(?i)^document$`, Action: RedactionHash},
		{PathPattern: `(?i)^session\.token$`, FieldPattern: `(?i)^token$`, Action: RedactionDrop},
	}, "***")
	require.NoError(t, err)

	payload := map[string]any{
		"password": "secret",
		"document": "123456789",
		"session":  map[string]any{"token": "tok_abc"},
	}

	obfuscated, err := ObfuscateStruct(payload, redactor)
	require.NoError(t, err)

	b, err := json.Marshal(obfuscated)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, "***", decoded["password"])
	assert.Contains(t, decoded["document"], "sha256:")
	assert.NotContains(t, decoded["session"], "token")
}

func TestHandleSpanHelpers_NoPanicsOnNil(t *testing.T) {
	var span oteltrace.Span
	assert.NotPanics(t, func() {
		HandleSpanEvent(span, "event", attribute.String("k", "v"))
		HandleSpanBusinessErrorEvent(span, "event", assert.AnError)
		HandleSpanError(span, "msg", assert.AnError)
	})
	assert.NotPanics(t, func() {
		_ = ExtractGRPCContext(context.Background(), metadata.MD{})
	})
}
