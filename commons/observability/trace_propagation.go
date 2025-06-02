package observability

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/LerianStudio/lib-commons/commons/log"
)

// TracePropagationConfig defines configuration for distributed tracing context propagation
type TracePropagationConfig struct {
	// Propagation settings
	EnableTextMapPropagation bool `json:"enable_text_map_propagation"`
	EnableBaggagePropagation bool `json:"enable_baggage_propagation"`
	EnableW3CTraceContext    bool `json:"enable_w3c_trace_context"`
	EnableB3Propagation      bool `json:"enable_b3_propagation"`
	EnableJaegerPropagation  bool `json:"enable_jaeger_propagation"`

	// Context enrichment
	AutoExtractSpanContext bool              `json:"auto_extract_span_context"`
	AutoInjectSpanContext  bool              `json:"auto_inject_span_context"`
	CustomHeaders          []string          `json:"custom_headers"`
	ServiceMetadata        map[string]string `json:"service_metadata"`

	// Span settings
	DefaultSpanKind   string   `json:"default_span_kind"`
	EnableSpanEvents  bool     `json:"enable_span_events"`
	EnableSpanLinks   bool     `json:"enable_span_links"`
	SpanAttributeKeys []string `json:"span_attribute_keys"`

	// Performance settings
	MaxBaggageItems          int  `json:"max_baggage_items"`
	MaxBaggageSize           int  `json:"max_baggage_size"`
	EnablePerformanceMetrics bool `json:"enable_performance_metrics"`

	// Error handling
	ContinueOnPropagationError bool `json:"continue_on_propagation_error"`
	LogPropagationErrors       bool `json:"log_propagation_errors"`
}

// DefaultTracePropagationConfig returns a production-ready trace propagation configuration
func DefaultTracePropagationConfig() TracePropagationConfig {
	return TracePropagationConfig{
		// Enable standard propagation formats
		EnableTextMapPropagation: true,
		EnableBaggagePropagation: true,
		EnableW3CTraceContext:    true,
		EnableB3Propagation:      true,
		EnableJaegerPropagation:  true,

		// Enable automatic context handling
		AutoExtractSpanContext: true,
		AutoInjectSpanContext:  true,
		CustomHeaders:          []string{"X-Request-ID", "X-Correlation-ID", "X-User-ID"},
		ServiceMetadata: map[string]string{
			"service.name":    "lib-commons",
			"service.version": "1.0.0",
			"deployment.env":  "production",
		},

		// Span configuration
		DefaultSpanKind:  "internal",
		EnableSpanEvents: true,
		EnableSpanLinks:  true,
		SpanAttributeKeys: []string{
			"http.method",
			"http.url",
			"http.status_code",
			"user.id",
			"request.id",
		},

		// Performance limits
		MaxBaggageItems:          50,
		MaxBaggageSize:           8192, // 8KB
		EnablePerformanceMetrics: true,

		// Error handling
		ContinueOnPropagationError: true,
		LogPropagationErrors:       true,
	}
}

// TracePropagator handles distributed tracing context propagation across service boundaries
type TracePropagator struct {
	config     TracePropagationConfig
	logger     log.Logger
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator

	// Performance tracking
	propagationMetrics TracePropagationMetrics
	metricsMutex       sync.RWMutex
}

// TracePropagationMetrics contains metrics for trace propagation operations
type TracePropagationMetrics struct {
	TotalExtractions       int64  `json:"total_extractions"`
	SuccessfulExtractions  int64  `json:"successful_extractions"`
	FailedExtractions      int64  `json:"failed_extractions"`
	TotalInjections        int64  `json:"total_injections"`
	SuccessfulInjections   int64  `json:"successful_injections"`
	FailedInjections       int64  `json:"failed_injections"`
	BaggageItemsExtracted  int64  `json:"baggage_items_extracted"`
	BaggageItemsInjected   int64  `json:"baggage_items_injected"`
	CustomHeadersProcessed int64  `json:"custom_headers_processed"`
	LastErrorTime          int64  `json:"last_error_time"`
	LastErrorMessage       string `json:"last_error_message"`
}

// NewTracePropagator creates a new trace propagation manager
func NewTracePropagator(config TracePropagationConfig, logger log.Logger) *TracePropagator {
	// Create composite propagator based on configuration
	var propagators []propagation.TextMapPropagator

	if config.EnableW3CTraceContext {
		propagators = append(propagators, propagation.TraceContext{})
	}

	if config.EnableBaggagePropagation {
		propagators = append(propagators, propagation.Baggage{})
	}

	if config.EnableB3Propagation {
		// Note: B3 propagator would need to be imported from appropriate package
		// propagators = append(propagators, b3.New())
	}

	if config.EnableJaegerPropagation {
		// Note: Jaeger propagator would need to be imported from appropriate package
		// propagators = append(propagators, jaeger.Jaeger{})
	}

	// Create composite propagator
	compositePropagator := propagation.NewCompositeTextMapPropagator(propagators...)

	// Set global propagator
	otel.SetTextMapPropagator(compositePropagator)

	return &TracePropagator{
		config:             config,
		logger:             logger,
		tracer:             otel.Tracer("lib-commons/observability"),
		propagator:         compositePropagator,
		propagationMetrics: TracePropagationMetrics{},
	}
}

// HTTPHeaderCarrier implements propagation.TextMapCarrier for HTTP headers
type HTTPHeaderCarrier struct {
	headers http.Header
}

// Get retrieves a value for a key
func (c HTTPHeaderCarrier) Get(key string) string {
	return c.headers.Get(key)
}

// Set stores a key-value pair
func (c HTTPHeaderCarrier) Set(key, value string) {
	c.headers.Set(key, value)
}

// Keys returns all keys
func (c HTTPHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c.headers))
	for k := range c.headers {
		keys = append(keys, k)
	}
	return keys
}

// FiberHeaderCarrier implements propagation.TextMapCarrier for Fiber headers
type FiberHeaderCarrier struct {
	ctx *fiber.Ctx
}

// Get retrieves a value for a key
func (c FiberHeaderCarrier) Get(key string) string {
	return c.ctx.Get(key)
}

// Set stores a key-value pair
func (c FiberHeaderCarrier) Set(key, value string) {
	c.ctx.Set(key, value)
}

// Keys returns all keys
func (c FiberHeaderCarrier) Keys() []string {
	var keys []string
	c.ctx.Request().Header.VisitAll(func(key, _ []byte) {
		keys = append(keys, string(key))
	})
	return keys
}

// ExtractTraceContext extracts trace context from HTTP headers
func (tp *TracePropagator) ExtractTraceContext(
	ctx context.Context,
	headers http.Header,
) context.Context {
	tp.metricsMutex.Lock()
	tp.propagationMetrics.TotalExtractions++
	tp.metricsMutex.Unlock()

	defer func() {
		if r := recover(); r != nil {
			tp.recordExtractionError(fmt.Errorf("panic during extraction: %v", r))
		}
	}()

	if !tp.config.AutoExtractSpanContext {
		return ctx
	}

	carrier := HTTPHeaderCarrier{headers: headers}
	extractedCtx := tp.propagator.Extract(ctx, carrier)

	// Extract custom headers as baggage
	if tp.config.EnableBaggagePropagation {
		extractedCtx = tp.extractCustomHeaders(extractedCtx, carrier)
	}

	// Add service metadata as span attributes
	extractedCtx = tp.enrichContextWithMetadata(extractedCtx)

	tp.metricsMutex.Lock()
	tp.propagationMetrics.SuccessfulExtractions++
	tp.metricsMutex.Unlock()

	return extractedCtx
}

// ExtractTraceContextFromFiber extracts trace context from Fiber request
func (tp *TracePropagator) ExtractTraceContextFromFiber(
	ctx context.Context,
	fiberCtx *fiber.Ctx,
) context.Context {
	tp.metricsMutex.Lock()
	tp.propagationMetrics.TotalExtractions++
	tp.metricsMutex.Unlock()

	defer func() {
		if r := recover(); r != nil {
			tp.recordExtractionError(fmt.Errorf("panic during fiber extraction: %v", r))
		}
	}()

	if !tp.config.AutoExtractSpanContext {
		return ctx
	}

	carrier := FiberHeaderCarrier{ctx: fiberCtx}
	extractedCtx := tp.propagator.Extract(ctx, carrier)

	// Extract custom headers as baggage
	if tp.config.EnableBaggagePropagation {
		extractedCtx = tp.extractCustomHeadersFromFiber(extractedCtx, fiberCtx)
	}

	// Add service metadata and request information
	extractedCtx = tp.enrichContextWithMetadata(extractedCtx)
	extractedCtx = tp.enrichContextWithRequestInfo(extractedCtx, fiberCtx)

	tp.metricsMutex.Lock()
	tp.propagationMetrics.SuccessfulExtractions++
	tp.metricsMutex.Unlock()

	return extractedCtx
}

// InjectTraceContext injects trace context into HTTP headers
func (tp *TracePropagator) InjectTraceContext(ctx context.Context, headers http.Header) error {
	tp.metricsMutex.Lock()
	tp.propagationMetrics.TotalInjections++
	tp.metricsMutex.Unlock()

	defer func() {
		if r := recover(); r != nil {
			tp.recordInjectionError(fmt.Errorf("panic during injection: %v", r))
		}
	}()

	if !tp.config.AutoInjectSpanContext {
		return nil
	}

	carrier := HTTPHeaderCarrier{headers: headers}
	tp.propagator.Inject(ctx, carrier)

	// Inject custom headers from baggage
	if tp.config.EnableBaggagePropagation {
		tp.injectCustomHeaders(ctx, carrier)
	}

	tp.metricsMutex.Lock()
	tp.propagationMetrics.SuccessfulInjections++
	tp.metricsMutex.Unlock()

	return nil
}

// InjectTraceContextToFiber injects trace context into Fiber response
func (tp *TracePropagator) InjectTraceContextToFiber(
	ctx context.Context,
	fiberCtx *fiber.Ctx,
) error {
	tp.metricsMutex.Lock()
	tp.propagationMetrics.TotalInjections++
	tp.metricsMutex.Unlock()

	defer func() {
		if r := recover(); r != nil {
			tp.recordInjectionError(fmt.Errorf("panic during fiber injection: %v", r))
		}
	}()

	if !tp.config.AutoInjectSpanContext {
		return nil
	}

	carrier := FiberHeaderCarrier{ctx: fiberCtx}
	tp.propagator.Inject(ctx, carrier)

	// Inject custom headers from baggage
	if tp.config.EnableBaggagePropagation {
		tp.injectCustomHeadersToFiber(ctx, fiberCtx)
	}

	tp.metricsMutex.Lock()
	tp.propagationMetrics.SuccessfulInjections++
	tp.metricsMutex.Unlock()

	return nil
}

// CreateSpanWithPropagation creates a new span with proper context propagation
func (tp *TracePropagator) CreateSpanWithPropagation(
	ctx context.Context,
	spanName string,
	options ...trace.SpanStartOption,
) (context.Context, trace.Span) {
	// Set default span kind if not specified
	spanKind := trace.SpanKindInternal
	switch strings.ToLower(tp.config.DefaultSpanKind) {
	case "server":
		spanKind = trace.SpanKindServer
	case "client":
		spanKind = trace.SpanKindClient
	case "producer":
		spanKind = trace.SpanKindProducer
	case "consumer":
		spanKind = trace.SpanKindConsumer
	}

	// Add default options
	defaultOptions := []trace.SpanStartOption{
		trace.WithSpanKind(spanKind),
	}

	// Add service metadata as attributes
	for key, value := range tp.config.ServiceMetadata {
		defaultOptions = append(defaultOptions, trace.WithAttributes(
			attribute.String(key, value),
		))
	}

	// Merge with provided options
	allOptions := append(defaultOptions, options...)

	spanCtx, span := tp.tracer.Start(ctx, spanName, allOptions...)

	// Add span events if enabled
	if tp.config.EnableSpanEvents {
		span.AddEvent("span.created", trace.WithAttributes(
			attribute.String("span.name", spanName),
			attribute.String("span.kind", spanKind.String()),
		))
	}

	return spanCtx, span
}

// CreateLinkedSpan creates a span with links to other spans
func (tp *TracePropagator) CreateLinkedSpan(
	ctx context.Context,
	spanName string,
	links []trace.Link,
	options ...trace.SpanStartOption,
) (context.Context, trace.Span) {
	if tp.config.EnableSpanLinks && len(links) > 0 {
		options = append(options, trace.WithLinks(links...))
	}

	return tp.CreateSpanWithPropagation(ctx, spanName, options...)
}

// PropagateError records an error in the current span and updates context
func (tp *TracePropagator) PropagateError(ctx context.Context, err error, description string) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, description)

		if tp.config.EnableSpanEvents {
			span.AddEvent("error.occurred", trace.WithAttributes(
				attribute.String("error.message", err.Error()),
				attribute.String("error.description", description),
			))
		}
	}
}

// extractCustomHeaders extracts custom headers and adds them to baggage
func (tp *TracePropagator) extractCustomHeaders(
	ctx context.Context,
	carrier HTTPHeaderCarrier,
) context.Context {
	if len(tp.config.CustomHeaders) == 0 {
		return ctx
	}

	var baggageMembers []baggage.Member
	for _, headerName := range tp.config.CustomHeaders {
		if value := carrier.Get(headerName); value != "" {
			member, err := baggage.NewMember(strings.ToLower(headerName), value)
			if err == nil {
				baggageMembers = append(baggageMembers, member)
				tp.metricsMutex.Lock()
				tp.propagationMetrics.BaggageItemsExtracted++
				tp.propagationMetrics.CustomHeadersProcessed++
				tp.metricsMutex.Unlock()
			}
		}
	}

	if len(baggageMembers) > 0 {
		existingBaggage := baggage.FromContext(ctx)
		newBaggage, err := baggage.New(baggageMembers...)
		if err == nil {
			// Merge with existing baggage
			for _, member := range existingBaggage.Members() {
				if newMember, addErr := baggage.NewMember(member.Key(), member.Value()); addErr == nil {
					if mergedBaggage, mergeErr := newBaggage.SetMember(newMember); mergeErr == nil {
						newBaggage = mergedBaggage
					}
				}
			}
			ctx = baggage.ContextWithBaggage(ctx, newBaggage)
		}
	}

	return ctx
}

// extractCustomHeadersFromFiber extracts custom headers from Fiber context
func (tp *TracePropagator) extractCustomHeadersFromFiber(
	ctx context.Context,
	fiberCtx *fiber.Ctx,
) context.Context {
	if len(tp.config.CustomHeaders) == 0 {
		return ctx
	}

	var baggageMembers []baggage.Member
	for _, headerName := range tp.config.CustomHeaders {
		if value := fiberCtx.Get(headerName); value != "" {
			member, err := baggage.NewMember(strings.ToLower(headerName), value)
			if err == nil {
				baggageMembers = append(baggageMembers, member)
				tp.metricsMutex.Lock()
				tp.propagationMetrics.BaggageItemsExtracted++
				tp.propagationMetrics.CustomHeadersProcessed++
				tp.metricsMutex.Unlock()
			}
		}
	}

	if len(baggageMembers) > 0 {
		existingBaggage := baggage.FromContext(ctx)
		newBaggage, err := baggage.New(baggageMembers...)
		if err == nil {
			// Merge with existing baggage
			for _, member := range existingBaggage.Members() {
				if newMember, addErr := baggage.NewMember(member.Key(), member.Value()); addErr == nil {
					if mergedBaggage, mergeErr := newBaggage.SetMember(newMember); mergeErr == nil {
						newBaggage = mergedBaggage
					}
				}
			}
			ctx = baggage.ContextWithBaggage(ctx, newBaggage)
		}
	}

	return ctx
}

// injectCustomHeaders injects custom headers from baggage
func (tp *TracePropagator) injectCustomHeaders(ctx context.Context, carrier HTTPHeaderCarrier) {
	bag := baggage.FromContext(ctx)
	for _, member := range bag.Members() {
		// Convert baggage key back to header format
		headerName := tp.baggageKeyToHeader(member.Key())
		if tp.isCustomHeader(headerName) {
			carrier.Set(headerName, member.Value())
			tp.metricsMutex.Lock()
			tp.propagationMetrics.BaggageItemsInjected++
			tp.metricsMutex.Unlock()
		}
	}
}

// injectCustomHeadersToFiber injects custom headers to Fiber context
func (tp *TracePropagator) injectCustomHeadersToFiber(ctx context.Context, fiberCtx *fiber.Ctx) {
	bag := baggage.FromContext(ctx)
	for _, member := range bag.Members() {
		// Convert baggage key back to header format
		headerName := tp.baggageKeyToHeader(member.Key())
		if tp.isCustomHeader(headerName) {
			fiberCtx.Set(headerName, member.Value())
			tp.metricsMutex.Lock()
			tp.propagationMetrics.BaggageItemsInjected++
			tp.metricsMutex.Unlock()
		}
	}
}

// enrichContextWithMetadata adds service metadata to the context
func (tp *TracePropagator) enrichContextWithMetadata(ctx context.Context) context.Context {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		for key, value := range tp.config.ServiceMetadata {
			span.SetAttributes(attribute.String(key, value))
		}
	}
	return ctx
}

// enrichContextWithRequestInfo adds request information to the context
func (tp *TracePropagator) enrichContextWithRequestInfo(
	ctx context.Context,
	fiberCtx *fiber.Ctx,
) context.Context {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("http.method", fiberCtx.Method()),
			attribute.String("http.url", fiberCtx.OriginalURL()),
			attribute.String("http.scheme", fiberCtx.Protocol()),
			attribute.String("http.host", fiberCtx.Hostname()),
			attribute.String("http.user_agent", fiberCtx.Get("User-Agent")),
			attribute.String("net.peer.ip", fiberCtx.IP()),
		)

		// Add custom attributes based on configuration
		for _, attrKey := range tp.config.SpanAttributeKeys {
			if value := fiberCtx.Get(attrKey); value != "" {
				span.SetAttributes(attribute.String(attrKey, value))
			}
		}
	}
	return ctx
}

// baggageKeyToHeader converts a baggage key back to header format
func (tp *TracePropagator) baggageKeyToHeader(key string) string {
	// Simple conversion: lowercase baggage key to header case
	parts := strings.Split(key, "-")
	for i, part := range parts {
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "-")
}

// isCustomHeader checks if a header is in the custom headers list
func (tp *TracePropagator) isCustomHeader(headerName string) bool {
	for _, customHeader := range tp.config.CustomHeaders {
		if strings.EqualFold(headerName, customHeader) {
			return true
		}
	}
	return false
}

// recordExtractionError records an extraction error
func (tp *TracePropagator) recordExtractionError(err error) {
	tp.metricsMutex.Lock()
	tp.propagationMetrics.FailedExtractions++
	tp.propagationMetrics.LastErrorTime = time.Now().Unix()
	tp.propagationMetrics.LastErrorMessage = err.Error()
	tp.metricsMutex.Unlock()

	if tp.config.LogPropagationErrors {
		tp.logger.Error("Trace context extraction failed",
			zap.Error(err),
			zap.Int64("total_extractions", tp.propagationMetrics.TotalExtractions),
			zap.Int64("failed_extractions", tp.propagationMetrics.FailedExtractions),
		)
	}
}

// recordInjectionError records an injection error
func (tp *TracePropagator) recordInjectionError(err error) {
	tp.metricsMutex.Lock()
	tp.propagationMetrics.FailedInjections++
	tp.propagationMetrics.LastErrorTime = time.Now().Unix()
	tp.propagationMetrics.LastErrorMessage = err.Error()
	tp.metricsMutex.Unlock()

	if tp.config.LogPropagationErrors {
		tp.logger.Error("Trace context injection failed",
			zap.Error(err),
			zap.Int64("total_injections", tp.propagationMetrics.TotalInjections),
			zap.Int64("failed_injections", tp.propagationMetrics.FailedInjections),
		)
	}
}

// GetPropagationMetrics returns current propagation metrics
func (tp *TracePropagator) GetPropagationMetrics() TracePropagationMetrics {
	tp.metricsMutex.RLock()
	defer tp.metricsMutex.RUnlock()
	return tp.propagationMetrics
}

// Middleware returns a Fiber middleware for automatic trace context propagation
func (tp *TracePropagator) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Extract trace context from request
		ctx := tp.ExtractTraceContextFromFiber(c.UserContext(), c)

		// Create a span for the request
		spanCtx, span := tp.CreateSpanWithPropagation(
			ctx,
			fmt.Sprintf("%s %s", c.Method(), c.Path()),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", c.Method()),
				attribute.String("http.route", c.Route().Path),
			),
		)
		defer span.End()

		// Update request context
		c.SetUserContext(spanCtx)

		// Process request
		err := c.Next()

		// Record response information
		span.SetAttributes(
			attribute.Int("http.status_code", c.Response().StatusCode()),
			attribute.Int("http.response_size", len(c.Response().Body())),
		)

		// Inject trace context into response headers
		if err := tp.InjectTraceContextToFiber(spanCtx, c); err != nil &&
			tp.config.LogPropagationErrors {
			tp.logger.Warn("Failed to inject trace context to response", zap.Error(err))
		}

		// Handle errors
		if err != nil {
			tp.PropagateError(spanCtx, err, "Request processing failed")
		}

		return err
	}
}
