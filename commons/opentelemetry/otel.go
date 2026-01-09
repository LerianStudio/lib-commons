package opentelemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/LerianStudio/lib-commons/v2/commons"
	constant "github.com/LerianStudio/lib-commons/v2/commons/constants"
	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/LerianStudio/lib-commons/v2/commons/opentelemetry/metrics"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"
)

var (
	// ErrNilTelemetryConfig indicates that nil config was provided to InitializeTelemetryWithError
	ErrNilTelemetryConfig = errors.New("telemetry config cannot be nil")
	// ErrNilTelemetryLogger indicates that config.Logger is nil
	ErrNilTelemetryLogger = errors.New("telemetry config logger cannot be nil")
)

type TelemetryConfig struct {
	LibraryName               string
	ServiceName               string
	ServiceVersion            string
	DeploymentEnv             string
	CollectorExporterEndpoint string
	EnableTelemetry           bool
	Logger                    log.Logger
}

type Telemetry struct {
	TelemetryConfig
	TracerProvider *sdktrace.TracerProvider
	MetricProvider *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
	MetricsFactory *metrics.MetricsFactory
	shutdown       func()
}

// NewResource creates a new resource with custom attributes.
func (tl *TelemetryConfig) newResource() *sdkresource.Resource {
	// Create a resource with only our custom attributes to avoid schema URL conflicts
	r := sdkresource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(tl.ServiceName),
		semconv.ServiceVersion(tl.ServiceVersion),
		semconv.DeploymentEnvironmentName(tl.DeploymentEnv),
		semconv.TelemetrySDKName(constant.TelemetrySDKName),
		semconv.TelemetrySDKLanguageGo,
	)

	return r
}

// NewLoggerExporter creates a new logger exporter that writes to stdout.
func (tl *TelemetryConfig) newLoggerExporter(ctx context.Context) (*otlploggrpc.Exporter, error) {
	exporter, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpoint(tl.CollectorExporterEndpoint), otlploggrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return exporter, nil
}

// newMetricExporter creates a new metric exporter that writes to stdout.
func (tl *TelemetryConfig) newMetricExporter(ctx context.Context) (*otlpmetricgrpc.Exporter, error) {
	exp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpoint(tl.CollectorExporterEndpoint), otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return exp, nil
}

// newTracerExporter creates a new tracer exporter that writes to stdout.
func (tl *TelemetryConfig) newTracerExporter(ctx context.Context) (*otlptrace.Exporter, error) {
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(tl.CollectorExporterEndpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return exporter, nil
}

// newLoggerProvider creates a new logger provider with stdout exporter and default resource.
func (tl *TelemetryConfig) newLoggerProvider(rsc *sdkresource.Resource, exp *otlploggrpc.Exporter) *sdklog.LoggerProvider {
	bp := sdklog.NewBatchProcessor(exp)
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(rsc), sdklog.WithProcessor(bp))

	return lp
}

// newMeterProvider creates a new meter provider with stdout exporter and default resource.
func (tl *TelemetryConfig) newMeterProvider(res *sdkresource.Resource, exp *otlpmetricgrpc.Exporter) *sdkmetric.MeterProvider {
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
	)

	return mp
}

// newTracerProvider creates a new tracer provider with stdout exporter and default resource.
func (tl *TelemetryConfig) newTracerProvider(rsc *sdkresource.Resource, exp *otlptrace.Exporter) *sdktrace.TracerProvider {
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(rsc),
		sdktrace.WithSpanProcessor(AttrBagSpanProcessor{}),
	)

	return tp
}

// ShutdownTelemetry shuts down the telemetry providers and exporters.
func (tl *Telemetry) ShutdownTelemetry() {
	tl.shutdown()
}

// InitializeTelemetryWithError initializes the telemetry providers and sets them globally.
// Returns an error instead of calling Fatalf on failure.
func InitializeTelemetryWithError(cfg *TelemetryConfig) (*Telemetry, error) {
	if cfg == nil {
		return nil, ErrNilTelemetryConfig
	}

	if cfg.Logger == nil {
		return nil, ErrNilTelemetryLogger
	}

	ctx := context.Background()
	l := cfg.Logger

	if !cfg.EnableTelemetry {
		l.Warn("Telemetry turned off ⚠️ ")

		mp := sdkmetric.NewMeterProvider()
		tp := sdktrace.NewTracerProvider()
		lp := sdklog.NewLoggerProvider()

		metricsFactory := metrics.NewMetricsFactory(mp.Meter(cfg.LibraryName), l)

		return &Telemetry{
			TelemetryConfig: *cfg,
			TracerProvider:  tp,
			MetricProvider:  mp,
			LoggerProvider:  lp,
			MetricsFactory:  metricsFactory,
			shutdown:        func() {},
		}, nil
	}

	l.Infof("Initializing telemetry...")

	r := cfg.newResource()

	tExp, err := cfg.newTracerExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't initialize tracer exporter: %w", err)
	}

	mExp, err := cfg.newMetricExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't initialize metric exporter: %w", err)
	}

	lExp, err := cfg.newLoggerExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't initialize logger exporter: %w", err)
	}

	mp := cfg.newMeterProvider(r, mExp)
	otel.SetMeterProvider(mp)

	meter := mp.Meter(cfg.LibraryName)
	metricsFactory := metrics.NewMetricsFactory(meter, l)

	tp := cfg.newTracerProvider(r, tExp)
	otel.SetTracerProvider(tp)

	lp := cfg.newLoggerProvider(r, lExp)
	global.SetLoggerProvider(lp)

	shutdownHandler := func() {
		err := mp.Shutdown(ctx)
		if err != nil {
			l.Errorf("can't shutdown metric provider: %v", err)
		}

		err = tp.Shutdown(ctx)
		if err != nil {
			l.Errorf("can't shutdown tracer provider: %v", err)
		}

		err = lp.Shutdown(ctx)
		if err != nil {
			l.Errorf("can't shutdown logger provider: %v", err)
		}

		err = tExp.Shutdown(ctx)
		if err != nil {
			l.Errorf("can't shutdown tracer exporter: %v", err)
		}

		err = mExp.Shutdown(ctx)
		if err != nil {
			l.Errorf("can't shutdown metric exporter: %v", err)
		}

		err = lExp.Shutdown(ctx)
		if err != nil {
			l.Errorf("can't shutdown logger exporter: %v", err)
		}
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	l.Infof("Telemetry initialized ✅ ")

	return &Telemetry{
		TelemetryConfig: TelemetryConfig{
			LibraryName:               cfg.LibraryName,
			ServiceName:               cfg.ServiceName,
			ServiceVersion:            cfg.ServiceVersion,
			DeploymentEnv:             cfg.DeploymentEnv,
			CollectorExporterEndpoint: cfg.CollectorExporterEndpoint,
			EnableTelemetry:           cfg.EnableTelemetry,
			Logger:                    l,
		},
		TracerProvider: tp,
		MetricProvider: mp,
		LoggerProvider: lp,
		MetricsFactory: metricsFactory,
		shutdown:       shutdownHandler,
	}, nil
}

// Deprecated: Use InitializeTelemetryWithError for proper error handling.
// InitializeTelemetry initializes the telemetry providers and sets them globally.
func InitializeTelemetry(cfg *TelemetryConfig) *Telemetry {
	telemetry, err := InitializeTelemetryWithError(cfg)
	if err != nil {
		cfg.Logger.Fatalf("%v", err)
	}

	return telemetry
}

// SetSpanAttributesFromStruct converts a struct to a JSON string and sets it as an attribute on the span.
func SetSpanAttributesFromStruct(span *trace.Span, key string, valueStruct any) error {
	jsonByte, err := json.Marshal(valueStruct)
	if err != nil {
		return err
	}

	vStr := string(jsonByte)

	(*span).SetAttributes(attribute.KeyValue{
		Key:   attribute.Key(key),
		Value: attribute.StringValue(vStr),
	})

	return nil
}

// Deprecated: Use SetSpanAttributesFromStruct instead.
//
// SetSpanAttributesFromStructWithObfuscation converts a struct to a JSON string,
// obfuscates sensitive fields using the default obfuscator, and sets it as an attribute on the span.
func SetSpanAttributesFromStructWithObfuscation(span *trace.Span, key string, valueStruct any) error {
	return SetSpanAttributesFromStructWithCustomObfuscation(span, key, valueStruct, NewDefaultObfuscator())
}

// Deprecated: Use SetSpanAttributesFromStruct instead.
//
// SetSpanAttributesFromStructWithCustomObfuscation converts a struct to a JSON string,
// obfuscates sensitive fields using the custom obfuscator provided, and sets it as an attribute on the span.
func SetSpanAttributesFromStructWithCustomObfuscation(span *trace.Span, key string, valueStruct any, obfuscator FieldObfuscator) error {
	processedStruct, err := ObfuscateStruct(valueStruct, obfuscator)
	if err != nil {
		return err
	}

	jsonByte, err := json.Marshal(processedStruct)
	if err != nil {
		return err
	}

	(*span).SetAttributes(attribute.KeyValue{
		Key:   attribute.Key(sanitizeUTF8String(key)),
		Value: attribute.StringValue(sanitizeUTF8String(string(jsonByte))),
	})

	return nil
}

// SetSpanAttributeForParam sets a span attribute for a Fiber request parameter with consistent naming
// entityName is a snake_case string used to identify id name, for example the "organization" entity name will result in "app.request.organization_id"
// otherwise the path parameter "id" in a Fiber request for example "/v1/organizations/:id" will be parsed as "app.request.id"
func SetSpanAttributeForParam(c *fiber.Ctx, param, value, entityName string) {
	spanAttrKey := "app.request." + param

	if entityName != "" && param == "id" {
		spanAttrKey = "app.request." + entityName + "_id"
	}

	c.SetUserContext(commons.ContextWithSpanAttributes(c.UserContext(), attribute.String(spanAttrKey, value)))
}

// HandleSpanBusinessErrorEvent adds a business error event to the span.
func HandleSpanBusinessErrorEvent(span *trace.Span, eventName string, err error) {
	if span != nil && err != nil {
		(*span).AddEvent(eventName, trace.WithAttributes(attribute.String("error", err.Error())))
	}
}

// HandleSpanEvent adds an event to the span.
func HandleSpanEvent(span *trace.Span, eventName string, attributes ...attribute.KeyValue) {
	if span != nil {
		(*span).AddEvent(eventName, trace.WithAttributes(attributes...))
	}
}

// HandleSpanError sets the status of the span to error and records the error.
func HandleSpanError(span *trace.Span, message string, err error) {
	if span != nil && err != nil {
		(*span).SetStatus(codes.Error, message+": "+err.Error())
		(*span).RecordError(err)
	}
}

// InjectHTTPContext modifies HTTP headers for trace propagation in outgoing client requests
func InjectHTTPContext(headers *http.Header, ctx context.Context) {
	carrier := propagation.HeaderCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	for k, v := range carrier {
		if len(v) > 0 {
			headers.Set(k, v[0])
		}
	}
}

// ExtractHTTPContext extracts OpenTelemetry trace context from incoming HTTP headers
// and injects it into the context. It works with Fiber's HTTP context.
func ExtractHTTPContext(c *fiber.Ctx) context.Context {
	// Create a carrier from the HTTP headers
	carrier := propagation.HeaderCarrier{}

	// Extract headers that might contain trace information
	for key, value := range c.Request().Header.All() {
		carrier.Set(string(key), string(value))
	}

	// Extract the trace context
	return otel.GetTextMapPropagator().Extract(c.UserContext(), carrier)
}

// InjectGRPCContext injects OpenTelemetry trace context into outgoing gRPC metadata.
// It normalizes W3C trace headers to lowercase for gRPC compatibility.
func InjectGRPCContext(ctx context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	if md == nil {
		md = metadata.New(nil)
	}

	// Returns the canonical format of the MIME header key s.
	// The canonicalization converts the first letter and any letter
	// following a hyphen to upper case; the rest are converted to lowercase.
	// For example, the canonical key for "accept-encoding" is "Accept-Encoding".
	// MIME header keys are assumed to be ASCII only.
	// If s contains a space or invalid header field bytes, it is
	// returned without modifications.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(md))

	if traceparentValues, exists := md["Traceparent"]; exists && len(traceparentValues) > 0 {
		md[constant.MetadataTraceparent] = traceparentValues
		delete(md, "Traceparent")
	}

	if tracestateValues, exists := md["Tracestate"]; exists && len(tracestateValues) > 0 {
		md[constant.MetadataTracestate] = tracestateValues
		delete(md, "Tracestate")
	}

	return metadata.NewOutgoingContext(ctx, md)
}

// ExtractGRPCContext extracts OpenTelemetry trace context from incoming gRPC metadata
// and injects it into the context. It handles case normalization for W3C trace headers.
func ExtractGRPCContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || md == nil {
		return ctx
	}

	mdCopy := md.Copy()

	if traceparentValues, exists := mdCopy[constant.MetadataTraceparent]; exists && len(traceparentValues) > 0 {
		mdCopy["Traceparent"] = traceparentValues
		delete(mdCopy, constant.MetadataTraceparent)
	}

	if tracestateValues, exists := mdCopy[constant.MetadataTracestate]; exists && len(tracestateValues) > 0 {
		mdCopy["Tracestate"] = tracestateValues
		delete(mdCopy, constant.MetadataTracestate)
	}

	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(mdCopy))
}

// InjectQueueTraceContext injects OpenTelemetry trace context into RabbitMQ headers
// for distributed tracing across queue messages. Returns a map of headers to be
// added to the RabbitMQ message headers.
func InjectQueueTraceContext(ctx context.Context) map[string]string {
	carrier := propagation.HeaderCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	headers := make(map[string]string)

	for k, v := range carrier {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	return headers
}

// ExtractQueueTraceContext extracts OpenTelemetry trace context from RabbitMQ headers
// and returns a new context with the extracted trace information. This enables
// distributed tracing continuity across queue message boundaries.
func ExtractQueueTraceContext(ctx context.Context, headers map[string]string) context.Context {
	if headers == nil {
		return ctx
	}

	carrier := propagation.HeaderCarrier{}
	for k, v := range headers {
		carrier.Set(k, v)
	}

	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// GetTraceIDFromContext extracts the trace ID from the current span context
// Returns empty string if no active span or trace ID is found
func GetTraceIDFromContext(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if span == nil {
		return ""
	}

	spanContext := span.SpanContext()

	if !spanContext.IsValid() {
		return ""
	}

	return spanContext.TraceID().String()
}

// GetTraceStateFromContext extracts the trace state from the current span context
// Returns empty string if no active span or trace state is found
func GetTraceStateFromContext(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if span == nil {
		return ""
	}

	spanContext := span.SpanContext()

	if !spanContext.IsValid() {
		return ""
	}

	return spanContext.TraceState().String()
}

// PrepareQueueHeaders prepares RabbitMQ headers with trace context injection
// following W3C trace context standards. Returns a map suitable for amqp.Table.
func PrepareQueueHeaders(ctx context.Context, baseHeaders map[string]any) map[string]any {
	headers := make(map[string]any)

	// Copy base headers first
	maps.Copy(headers, baseHeaders)

	// Inject trace context using W3C standards
	traceHeaders := InjectQueueTraceContext(ctx)
	for k, v := range traceHeaders {
		headers[k] = v
	}

	return headers
}

// InjectTraceHeadersIntoQueue adds OpenTelemetry trace headers to existing RabbitMQ headers
// following W3C trace context standards. Modifies the headers map in place.
func InjectTraceHeadersIntoQueue(ctx context.Context, headers *map[string]any) {
	if headers == nil {
		return
	}

	// Inject trace context using W3C standards
	traceHeaders := InjectQueueTraceContext(ctx)
	for k, v := range traceHeaders {
		(*headers)[k] = v
	}
}

// ExtractTraceContextFromQueueHeaders extracts OpenTelemetry trace context from RabbitMQ amqp.Table headers
// and returns a new context with the extracted trace information. Handles type conversion automatically.
func ExtractTraceContextFromQueueHeaders(baseCtx context.Context, amqpHeaders map[string]any) context.Context {
	if len(amqpHeaders) == 0 {
		return baseCtx
	}

	// Convert amqp.Table headers to map[string]string for trace extraction
	traceHeaders := make(map[string]string)

	for k, v := range amqpHeaders {
		if str, ok := v.(string); ok {
			traceHeaders[k] = str
		}
	}

	if len(traceHeaders) == 0 {
		return baseCtx
	}

	// Extract trace context using existing function
	return ExtractQueueTraceContext(baseCtx, traceHeaders)
}

func (tl *Telemetry) EndTracingSpans(ctx context.Context) {
	trace.SpanFromContext(ctx).End()
}

// sanitizeUTF8String validates and sanitizes UTF-8 string.
// If the string contains invalid UTF-8 characters, they are replaced with the Unicode replacement character (�).
func sanitizeUTF8String(s string) string {
	if !utf8.ValidString(s) {
		return strings.ToValidUTF8(s, "�")
	}

	return s
}
