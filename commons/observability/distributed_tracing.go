package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// DistributedTracingHelper helps with distributed tracing across services
type DistributedTracingHelper struct {
	propagator propagation.TextMapPropagator
}

// NewDistributedTracingHelper creates a new distributed tracing helper
func NewDistributedTracingHelper() *DistributedTracingHelper {
	return &DistributedTracingHelper{
		propagator: otel.GetTextMapPropagator(),
	}
}

// PropagateServiceCall wraps a service call with distributed tracing
func (h *DistributedTracingHelper) PropagateServiceCall(
	ctx context.Context,
	tracer trace.Tracer,
	serviceName string,
	operationName string,
	call func(context.Context) error,
) error {
	// Start a new span for the service call
	ctx, span := tracer.Start(ctx, fmt.Sprintf("call_%s_%s", serviceName, operationName),
		trace.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("operation.name", operationName),
			attribute.String("span.kind", "client"),
		),
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()

	// Execute the call
	err := call(ctx)

	// Record the result
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("error", true))
	} else {
		span.SetStatus(codes.Ok, "Success")
	}

	return err
}

// InjectContext injects trace context into a carrier for propagation
func (h *DistributedTracingHelper) InjectContext(
	ctx context.Context,
	carrier propagation.TextMapCarrier,
) {
	h.propagator.Inject(ctx, carrier)
}

// ExtractContext extracts trace context from a carrier
func (h *DistributedTracingHelper) ExtractContext(
	ctx context.Context,
	carrier propagation.TextMapCarrier,
) context.Context {
	return h.propagator.Extract(ctx, carrier)
}

// CreateChildSpan creates a child span from the current context
func (h *DistributedTracingHelper) CreateChildSpan(
	ctx context.Context,
	tracer trace.Tracer,
	spanName string,
	opts ...trace.SpanStartOption,
) (context.Context, trace.Span) {
	return tracer.Start(ctx, spanName, opts...)
}

// MapCarrier is a simple implementation of TextMapCarrier for headers
type MapCarrier map[string]string

// Get returns the value associated with the passed key
func (c MapCarrier) Get(key string) string {
	return c[key]
}

// Set stores the key-value pair
func (c MapCarrier) Set(key string, value string) {
	c[key] = value
}

// Keys lists the keys stored in this carrier
func (c MapCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}

	return keys
}

// TraceContextCarrier wraps any type that has Get/Set methods for headers
type TraceContextCarrier struct {
	headers map[string]string
}

// NewTraceContextCarrier creates a new trace context carrier
func NewTraceContextCarrier() *TraceContextCarrier {
	return &TraceContextCarrier{
		headers: make(map[string]string),
	}
}

// Get returns the value for a key
func (c *TraceContextCarrier) Get(key string) string {
	return c.headers[key]
}

// Set sets a key-value pair
func (c *TraceContextCarrier) Set(key string, value string) {
	c.headers[key] = value
}

// Keys returns all keys
func (c *TraceContextCarrier) Keys() []string {
	keys := make([]string, 0, len(c.headers))
	for k := range c.headers {
		keys = append(keys, k)
	}

	return keys
}

// GetHeaders returns all headers as a map
func (c *TraceContextCarrier) GetHeaders() map[string]string {
	return c.headers
}

// SpanProcessor is a helper for processing spans with common operations
type SpanProcessor struct {
	provider Provider
}

// NewSpanProcessor creates a new span processor
func NewSpanProcessor(provider Provider) *SpanProcessor {
	return &SpanProcessor{provider: provider}
}

// ProcessWithSpan processes a function within a span with automatic error handling
func (p *SpanProcessor) ProcessWithSpan(
	ctx context.Context,
	spanName string,
	fn func(context.Context) error,
	opts ...trace.SpanStartOption,
) error {
	if !p.provider.IsEnabled() {
		return fn(ctx)
	}

	ctx, span := p.provider.Tracer().Start(ctx, spanName, opts...)
	defer span.End()

	// Add context to logger
	logger := p.provider.Logger().WithSpan(span)

	// Log span start
	logger.Debugf("Starting span: %s", spanName)

	// Execute function
	err := fn(ctx)

	// Handle result
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		logger.Errorf("Span %s failed: %v", spanName, err)
	} else {
		span.SetStatus(codes.Ok, "Success")
		logger.Debugf("Span %s completed successfully", spanName)
	}

	return err
}

// ProcessWithSpanAndResult processes a function that returns a result and error
func (p *SpanProcessor) ProcessWithSpanAndResult(
	ctx context.Context,
	spanName string,
	fn func(context.Context) (any, error),
	opts ...trace.SpanStartOption,
) (any, error) {
	if !p.provider.IsEnabled() {
		return fn(ctx)
	}

	ctx, span := p.provider.Tracer().Start(ctx, spanName, opts...)
	defer span.End()

	// Execute function
	result, err := fn(ctx)

	// Handle result
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "Success")
	}

	return result, err
}

// AsyncSpanProcessor helps with processing asynchronous operations with spans
type AsyncSpanProcessor struct {
	provider Provider
}

// NewAsyncSpanProcessor creates a new async span processor
func NewAsyncSpanProcessor(provider Provider) *AsyncSpanProcessor {
	return &AsyncSpanProcessor{provider: provider}
}

// ProcessAsync processes an async operation with a span
func (p *AsyncSpanProcessor) ProcessAsync(
	ctx context.Context,
	spanName string,
	fn func(context.Context) error,
	opts ...trace.SpanStartOption,
) <-chan error {
	errChan := make(chan error, 1)

	go func() {
		if !p.provider.IsEnabled() {
			errChan <- fn(ctx)
			return
		}

		spanCtx, span := p.provider.Tracer().Start(ctx, spanName, opts...)
		defer span.End()

		err := fn(spanCtx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "Success")
		}

		errChan <- err
	}()

	return errChan
}

// Link creates a link between spans
func Link(
	_ context.Context,
	linkedSpanContext trace.SpanContext,
	attributes ...attribute.KeyValue,
) trace.Link {
	return trace.Link{
		SpanContext: linkedSpanContext,
		Attributes:  attributes,
	}
}

// GetCurrentSpan returns the current span from context
func GetCurrentSpan(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}

// SetSpanError sets error on the current span
func SetSpanError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() && err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("error", true))
	}
}

// SetSpanSuccess sets success status on the current span
func SetSpanSuccess(ctx context.Context) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetStatus(codes.Ok, "Success")
	}
}
