package commons

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-observability/metrics"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ErrNilParentContext indicates that a nil parent context was provided
var ErrNilParentContext = errors.New("cannot create context from nil parent")

// ---- Context container ----

type customContextKey string

// CustomContextKey is the context key used to store CustomContextKeyValue.
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

// NewLoggerFromContext extract the Logger from "logger" value inside context.
// A nil ctx is normalized to context.Background() so callers never trigger a nil-pointer dereference.
//
// Deprecated: use NewLoggerFromContext from github.com/LerianStudio/lib-observability.
//
//nolint:ireturn
func NewLoggerFromContext(ctx context.Context) log.Logger {
	if ctx == nil {
		ctx = context.Background()
	}

	if customContext, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue); ok &&
		customContext.Logger != nil {
		return customContext.Logger
	}

	return &log.NopLogger{}
}

// cloneContextValues returns a shallow copy of the CustomContextKeyValue from ctx.
// This prevents concurrent mutation of a shared struct when multiple goroutines
// derive child contexts from the same parent.
// The AttrBag slice is deep-copied to avoid aliasing the underlying array.
func cloneContextValues(ctx context.Context) *CustomContextKeyValue {
	existing, _ := ctx.Value(CustomContextKey).(*CustomContextKeyValue)

	clone := &CustomContextKeyValue{}
	if existing != nil {
		*clone = *existing

		// Deep-copy the slice to avoid aliasing the backing array.
		if len(existing.AttrBag) > 0 {
			clone.AttrBag = make([]attribute.KeyValue, len(existing.AttrBag))
			copy(clone.AttrBag, existing.AttrBag)
		}
	}

	return clone
}

// ContextWithLogger returns a context within a Logger in "logger" value.
//
// Deprecated: use ContextWithLogger from github.com/LerianStudio/lib-observability.
func ContextWithLogger(ctx context.Context, logger log.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	values := cloneContextValues(ctx)
	values.Logger = logger

	return context.WithValue(ctx, CustomContextKey, values)
}

// ---- Tracer helpers ----

// ContextWithTracer returns a context within a trace.Tracer in "tracer" value.
//
// Deprecated: use ContextWithTracer from github.com/LerianStudio/lib-observability.
func ContextWithTracer(ctx context.Context, tracer trace.Tracer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	values := cloneContextValues(ctx)
	values.Tracer = tracer

	return context.WithValue(ctx, CustomContextKey, values)
}

// ---- Metrics helpers ----

// ContextWithMetricFactory returns a context within a MetricsFactory in "metricFactory" value.
//
// Deprecated: use ContextWithMetricFactory from github.com/LerianStudio/lib-observability.
func ContextWithMetricFactory(ctx context.Context, metricFactory *metrics.MetricsFactory) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	values := cloneContextValues(ctx)
	values.MetricFactory = metricFactory

	return context.WithValue(ctx, CustomContextKey, values)
}

// ---- Correlation / HeaderID helpers ----

// ContextWithHeaderID returns a context within a HeaderID in "headerID" value.
//
// Deprecated: use ContextWithHeaderID from github.com/LerianStudio/lib-observability.
func ContextWithHeaderID(ctx context.Context, headerID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	values := cloneContextValues(ctx)
	values.HeaderID = headerID

	return context.WithValue(ctx, CustomContextKey, values)
}

// ---- Tracking bundle (convenience) ----

// TrackingComponents represents the complete set of tracking components extracted from context.
// This struct encapsulates all telemetry-related dependencies in a single, cohesive unit.
//
// Deprecated: use TrackingComponents from github.com/LerianStudio/lib-observability.
type TrackingComponents struct {
	Logger        log.Logger
	Tracer        trace.Tracer
	HeaderID      string
	MetricFactory *metrics.MetricsFactory
}

// NewTrackingFromContext extracts tracking components from context with intelligent fallback.
// It follows the fail-safe principle: preserve valid components, provide sensible defaults for invalid ones.
//
// Deprecated: use NewTrackingFromContext from github.com/LerianStudio/lib-observability.
//
//nolint:ireturn
func NewTrackingFromContext(ctx context.Context) (log.Logger, trace.Tracer, string, *metrics.MetricsFactory) {
	if ctx == nil {
		ctx = context.Background()
	}

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

	return &log.NopLogger{} // Null Object Pattern - always functional
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
//
// IMPORTANT: When no HeaderID is present in context, a new UUID is generated on
// every call to NewTrackingFromContext. Ingress middleware (HTTP/gRPC) MUST persist
// the generated ID back into context via ContextWithHeaderID so that downstream
// extractions within the same request return a stable correlation ID.
func resolveHeaderID(headerID string) string {
	if trimmed := strings.TrimSpace(headerID); trimmed != "" {
		return trimmed
	}

	return uuid.New().String() // Generate unique correlation ID
}

var (
	defaultFactoryOnce sync.Once
	defaultFactory     *metrics.MetricsFactory
)

func getDefaultMetricsFactory() *metrics.MetricsFactory {
	defaultFactoryOnce.Do(func() {
		meter := otel.GetMeterProvider().Meter("commons.default")

		f, err := metrics.NewMetricsFactory(meter, &log.NopLogger{})
		if err != nil {
			defaultFactory = metrics.NewNopFactory()
			return
		}

		defaultFactory = f
	})

	return defaultFactory
}

// resolveMetricFactory ensures a valid metrics factory is always available following the fail-safe pattern.
// Provides a cached default factory when none exists, initialized once via sync.Once.
// Never returns nil: if factory creation fails, it falls back to a no-op factory.
func resolveMetricFactory(factory *metrics.MetricsFactory) *metrics.MetricsFactory {
	if factory != nil {
		return factory
	}

	return getDefaultMetricsFactory()
}

// newDefaultTrackingComponents creates a complete set of default components.
// Used when context extraction fails entirely - ensures system remains operational.
func newDefaultTrackingComponents() TrackingComponents {
	return TrackingComponents{
		Logger:        &log.NopLogger{},
		Tracer:        otel.Tracer("commons.default"),
		HeaderID:      uuid.New().String(),
		MetricFactory: resolveMetricFactory(nil),
	}
}

// ---- Attribute Bag (request-wide span attributes) ----

// ContextWithSpanAttributes appends one or more attributes to the request's AttrBag.
// Call this once at the ingress (HTTP/gRPC middleware) and avoid per-layer duplication.
// Example keys: tenant.id, enduser.id, request.route, region, plan.
//
// Deprecated: use ContextWithSpanAttributes from github.com/LerianStudio/lib-observability.
func ContextWithSpanAttributes(ctx context.Context, kv ...attribute.KeyValue) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	if len(kv) == 0 {
		return ctx
	}

	values := cloneContextValues(ctx)
	// Append to the cloned (independent) slice.
	values.AttrBag = append(values.AttrBag, kv...)

	return context.WithValue(ctx, CustomContextKey, values)
}

// AttributesFromContext returns a shallow copy of the AttrBag slice, safe to reuse by processors.
//
// Deprecated: use AttributesFromContext from github.com/LerianStudio/lib-observability.
func AttributesFromContext(ctx context.Context) []attribute.KeyValue {
	if ctx == nil {
		return nil
	}

	if values, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue); ok && values != nil && len(values.AttrBag) > 0 {
		out := make([]attribute.KeyValue, len(values.AttrBag))
		copy(out, values.AttrBag)

		return out
	}

	return nil
}

// ReplaceAttributes resets the current AttrBag with a new set (rarely needed; provided for completeness).
//
// Deprecated: use ReplaceAttributes from github.com/LerianStudio/lib-observability.
func ReplaceAttributes(ctx context.Context, kv ...attribute.KeyValue) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	values := cloneContextValues(ctx)
	// Replace with a fresh slice -- the clone already has an independent copy.
	values.AttrBag = make([]attribute.KeyValue, len(kv))
	copy(values.AttrBag, kv)

	return context.WithValue(ctx, CustomContextKey, values)
}

// ---- Deadline Management ----

// WithTimeoutSafe creates a context with the specified timeout, but respects
// any existing deadline in the parent context. Returns an error if parent is nil.
//
// The function returns three values (context, cancel, error) for explicit nil-parent error handling.
//
// Note: When the parent's deadline is shorter than the requested timeout, this function
// returns a cancellable context that inherits the parent's deadline rather than creating
// a new deadline. The returned context's Deadline() will return the parent's deadline.
func WithTimeoutSafe(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, nil, ErrNilParentContext
	}

	if deadline, ok := parent.Deadline(); ok {
		timeUntilDeadline := time.Until(deadline)

		if timeUntilDeadline < timeout {
			ctx, cancel := context.WithCancel(parent) // #nosec G118 -- cancel is intentionally returned to the caller for lifecycle management
			return ctx, cancel, nil
		}
	}

	ctx, cancel := context.WithTimeout(parent, timeout) // #nosec G118 -- cancel is intentionally returned to the caller for lifecycle management

	return ctx, cancel, nil
}
