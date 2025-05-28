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
func (h *DistributedTracingHelper) InjectContext(ctx context.Context, carrier propagation.TextMapCarrier) {
	h.propagator.Inject(ctx, carrier)
}

// ExtractContext extracts trace context from a carrier
func (h *DistributedTracingHelper) ExtractContext(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
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

// MapCarrier is a simple implementation of TextMapCarrier for HTTP headers
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