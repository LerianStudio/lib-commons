package main

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

func balanced(ctx context.Context, tracer trace.Tracer) {
	ctx, span := tracer.Start(ctx, "Balanced.Op")
	defer span.End()
	_ = ctx
}

func leaky(ctx context.Context, tracer trace.Tracer) {
	_, span := tracer.Start(ctx, "Leaky.Op")
	_ = span
}

func main() {}
