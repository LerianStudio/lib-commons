package commons

import (
	"context"
	"strings"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/LerianStudio/lib-commons/v2/commons/opentelemetry/metrics"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ---- Context container ----

type customContextKey string

var CustomContextKey = customContextKey("custom_context")

// CustomContextKeyValue holds all request-scoped facilities we attach to context.
type CustomContextKeyValue struct {
	HeaderID      string
	Tracer        trace.Tracer
	Logger        log.Logger
	MetricFactory *metrics.MetricsFactory

	// AttrBag holds request-wide attributes to be applied to every span.
	// Keep low/medium cardinality attributes here (tenant.id, plan, region, request_id, route).
	AttrBag []attribute.KeyValue
}

// ---- Logger helpers ----

// Deprecated: use NewTrackingFromContext instead
//
// NewLoggerFromContext extract the Logger from "logger" value inside context
//
//nolint:ireturn
func NewLoggerFromContext(ctx context.Context) log.Logger {
	if customContext, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue); ok &&
		customContext.Logger != nil {
		return customContext.Logger
	}

	return &log.NoneLogger{}
}

// ContextWithLogger returns a context within a Logger in "logger" value.
func ContextWithLogger(ctx context.Context, logger log.Logger) context.Context {
	values, _ := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if values == nil {
		values = &CustomContextKeyValue{}
	}

	values.Logger = logger

	return context.WithValue(ctx, CustomContextKey, values)
}

// ---- Tracer helpers ----

// Deprecated: use NewTrackingFromContext instead
//
// NewTracerFromContext returns a new tracer from the context.
//
//nolint:ireturn
func NewTracerFromContext(ctx context.Context) trace.Tracer {
	if customContext, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue); ok &&
		customContext.Tracer != nil {
		return customContext.Tracer
	}

	return otel.Tracer("default")
}

// ContextWithTracer returns a context within a trace.Tracer in "tracer" value.
func ContextWithTracer(ctx context.Context, tracer trace.Tracer) context.Context {
	values, _ := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if values == nil {
		values = &CustomContextKeyValue{}
	}

	values.Tracer = tracer

	return context.WithValue(ctx, CustomContextKey, values)
}

// ---- Metrics helpers ----

// Deprecated: use NewTrackingFromContext instead
//
// NewMetricFactoryFromContext returns a new metric factory from the context.
//
//nolint:ireturn
func NewMetricFactoryFromContext(ctx context.Context) *metrics.MetricsFactory {
	if customContext, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue); ok &&
		customContext.MetricFactory != nil {
		return customContext.MetricFactory
	}

	return metrics.NewMetricsFactory(otel.GetMeterProvider().Meter("default"), &log.NoneLogger{})
}

// ContextWithMetricFactory returns a context within a MetricsFactory in "metricFactory" value.
func ContextWithMetricFactory(ctx context.Context, metricFactory *metrics.MetricsFactory) context.Context {
	values, _ := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if values == nil {
		values = &CustomContextKeyValue{}
	}

	values.MetricFactory = metricFactory

	return context.WithValue(ctx, CustomContextKey, values)
}

// ---- Correlation / HeaderID helpers ----

// ContextWithHeaderID returns a context within a HeaderID in "headerID" value.
func ContextWithHeaderID(ctx context.Context, headerID string) context.Context {
	values, _ := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if values == nil {
		values = &CustomContextKeyValue{}
	}

	values.HeaderID = headerID

	return context.WithValue(ctx, CustomContextKey, values)
}

// Deprecated: use NewTrackingFromContext instead
//
// NewHeaderIDFromContext returns a HeaderID from the context.
func NewHeaderIDFromContext(ctx context.Context) string {
	customContext, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if !ok {
		return uuid.New().String()
	}

	if customContext != nil && strings.TrimSpace(customContext.HeaderID) != "" {
		return customContext.HeaderID
	}

	return uuid.New().String()
}

// ---- Tracking bundle (convenience) ----

// TrackingComponents represents the complete set of tracking components extracted from context.
// This struct encapsulates all telemetry-related dependencies in a single, cohesive unit.
type TrackingComponents struct {
	Logger        log.Logger
	Tracer        trace.Tracer
	HeaderID      string
	MetricFactory *metrics.MetricsFactory
}

// NewTrackingFromContext extracts tracking components from context with intelligent fallback.
// It follows the fail-safe principle: preserve valid components, provide sensible defaults for invalid ones.
//
//nolint:ireturn
func NewTrackingFromContext(ctx context.Context) (log.Logger, trace.Tracer, string, *metrics.MetricsFactory) {
	components := extractTrackingComponents(ctx)
	return components.Logger, components.Tracer, components.HeaderID, components.MetricFactory
}

// extractTrackingComponents performs the core extraction logic with comprehensive fallback strategy.
func extractTrackingComponents(ctx context.Context) TrackingComponents {
	customContext, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if !ok || customContext == nil {
		return newDefaultTrackingComponents()
	}

	return TrackingComponents{
		Logger:        resolveLogger(customContext.Logger),
		Tracer:        resolveTracer(customContext.Tracer),
		HeaderID:      resolveHeaderID(customContext.HeaderID),
		MetricFactory: resolveMetricFactory(customContext.MetricFactory),
	}
}

// resolveLogger applies the Null Object Pattern for logger resolution.
// Returns a functional logger instance in all cases, eliminating nil checks downstream.
func resolveLogger(logger log.Logger) log.Logger {
	if logger != nil {
		return logger
	}

	return &log.NoneLogger{} // Null Object Pattern - always functional
}

// resolveTracer ensures a valid tracer is always available using OpenTelemetry best practices.
// The default tracer maintains observability even when context is incomplete.
func resolveTracer(tracer trace.Tracer) trace.Tracer {
	if tracer != nil {
		return tracer
	}

	return otel.Tracer("commons.default") // Descriptive tracer name for debugging
}

// resolveHeaderID implements the correlation ID pattern with UUID fallback.
// Ensures every request has a unique identifier for distributed tracing.
func resolveHeaderID(headerID string) string {
	if trimmed := strings.TrimSpace(headerID); trimmed != "" {
		return trimmed
	}

	return uuid.New().String() // Generate unique correlation ID
}

// resolveMetricFactory ensures a valid metrics factory is always available following the fail-safe pattern.
// Provides a default factory when none exists, maintaining consistency with logger and tracer resolution.
func resolveMetricFactory(factory *metrics.MetricsFactory) *metrics.MetricsFactory {
	if factory != nil {
		return factory
	}

	return metrics.NewMetricsFactory(otel.GetMeterProvider().Meter("commons.default"), &log.NoneLogger{})
}

// newDefaultTrackingComponents creates a complete set of default components.
// Used when context extraction fails entirely - ensures system remains operational.
func newDefaultTrackingComponents() TrackingComponents {
	return TrackingComponents{
		Logger:        &log.NoneLogger{},
		Tracer:        otel.Tracer("commons.default"),
		HeaderID:      uuid.New().String(),
		MetricFactory: metrics.NewMetricsFactory(otel.GetMeterProvider().Meter("commons.default"), &log.NoneLogger{}),
	}
}

// ---- Attribute Bag (request-wide span attributes) ----

// ContextWithSpanAttributes appends one or more attributes to the request's AttrBag.
// Call this once at the ingress (HTTP/gRPC middleware) and avoid per-layer duplication.
// Example keys: tenant.id, enduser.id, request.route, region, plan.
func ContextWithSpanAttributes(ctx context.Context, kv ...attribute.KeyValue) context.Context {
	if len(kv) == 0 {
		return ctx
	}

	values, _ := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if values == nil {
		values = &CustomContextKeyValue{}
	}
	// Append (preserve order; low-cost).
	values.AttrBag = append(values.AttrBag, kv...)

	return context.WithValue(ctx, CustomContextKey, values)
}

// AttributesFromContext returns a shallow copy of the AttrBag slice, safe to reuse by processors.
func AttributesFromContext(ctx context.Context) []attribute.KeyValue {
	if values, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue); ok && len(values.AttrBag) > 0 {
		out := make([]attribute.KeyValue, len(values.AttrBag))
		copy(out, values.AttrBag)

		return out
	}

	return nil
}

// ReplaceAttributes resets the current AttrBag with a new set (rarely needed; provided for completeness).
func ReplaceAttributes(ctx context.Context, kv ...attribute.KeyValue) context.Context {
	values, _ := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if values == nil {
		values = &CustomContextKeyValue{}
	}

	values.AttrBag = append(values.AttrBag[:0], kv...)

	return context.WithValue(ctx, CustomContextKey, values)
}
