package commons

import (
	"context"
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

type customContextKey string

var CustomContextKey = customContextKey("custom_context")

// LoggerKey is the context key for the logger
var LoggerKey = customContextKey("logger")

type CustomContextKeyValue struct {
	HeaderID string
	Tracer   trace.Tracer
	Logger   log.Logger
}

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

// ContextWithHeaderID returns a context within a HeaderID in "headerID" value.
func ContextWithHeaderID(ctx context.Context, headerID string) context.Context {
	values, _ := ctx.Value(CustomContextKey).(*CustomContextKeyValue)
	if values == nil {
		values = &CustomContextKeyValue{}
	}

	values.HeaderID = headerID

	return context.WithValue(ctx, CustomContextKey, values)
}

// NewHeaderIDFromContext returns a HeaderID from the context.
func NewHeaderIDFromContext(ctx context.Context) string {
	if customContext, ok := ctx.Value(CustomContextKey).(*CustomContextKeyValue); ok &&
		!IsNilOrEmpty(&customContext.HeaderID) {
		return customContext.HeaderID
	}

	return uuid.New().String()
}
