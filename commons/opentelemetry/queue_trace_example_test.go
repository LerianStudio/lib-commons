//go:build unit

package opentelemetry_test

import (
	"context"
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func ExamplePrepareQueueHeaders() {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(prev)

	traceID, _ := trace.TraceIDFromHex("00112233445566778899aabbccddeeff")
	spanID, _ := trace.SpanIDFromHex("0123456789abcdef")

	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}))

	headers := opentelemetry.PrepareQueueHeaders(ctx, map[string]any{"message_type": "transaction.created"})
	traceParent, ok := headers["traceparent"]
	if !ok {
		traceParent = headers["Traceparent"]
	}

	extracted := opentelemetry.ExtractTraceContextFromQueueHeaders(context.Background(), headers)

	fmt.Println(headers["message_type"])
	fmt.Println(traceParent)
	fmt.Println(opentelemetry.GetTraceIDFromContext(extracted))

	// Output:
	// transaction.created
	// 00-00112233445566778899aabbccddeeff-0123456789abcdef-01
	// 00112233445566778899aabbccddeeff
}
