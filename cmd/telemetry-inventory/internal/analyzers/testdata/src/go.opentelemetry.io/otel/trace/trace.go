package trace

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type SpanStartOption interface{}

type Tracer interface {
	Start(context.Context, string, ...SpanStartOption) (context.Context, Span)
}

type Span interface {
	End()
	RecordError(error)
	SetStatus(codes.Code, string)
	SetAttributes(...attribute.KeyValue)
}

func WithAttributes(...attribute.KeyValue) SpanStartOption { return nil }
