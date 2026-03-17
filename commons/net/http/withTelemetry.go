package http

import (
	"context"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons"
	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// DefaultMetricsCollectionInterval is the default interval for collecting system metrics.
// Can be overridden via METRICS_COLLECTION_INTERVAL environment variable.
const DefaultMetricsCollectionInterval = 5 * time.Second

// TelemetryMiddleware wraps HTTP and gRPC handlers with tracing and metrics setup.
type TelemetryMiddleware struct {
	Telemetry *opentelemetry.Telemetry
}

// NewTelemetryMiddleware creates a new instance of TelemetryMiddleware.
func NewTelemetryMiddleware(tl *opentelemetry.Telemetry) *TelemetryMiddleware {
	return &TelemetryMiddleware{tl}
}

// WithTelemetry is a middleware that adds tracing to the context.
func (tm *TelemetryMiddleware) WithTelemetry(tl *opentelemetry.Telemetry, excludedRoutes ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		effectiveTelemetry := tl
		if effectiveTelemetry == nil && tm != nil {
			effectiveTelemetry = tm.Telemetry
		}

		if effectiveTelemetry == nil {
			return c.Next()
		}

		if len(excludedRoutes) > 0 && isRouteExcludedFromList(c, excludedRoutes) {
			return c.Next()
		}

		setRequestHeaderID(c)

		ctx := c.UserContext()
		_, _, reqId, _ := commons.NewTrackingFromContext(ctx)

		c.SetUserContext(commons.ContextWithSpanAttributes(ctx,
			attribute.String("app.request.request_id", reqId),
		))

		if effectiveTelemetry.TracerProvider == nil {
			return c.Next()
		}

		tracer := effectiveTelemetry.TracerProvider.Tracer(effectiveTelemetry.LibraryName)
		routePathWithMethod := c.Method() + " " + commons.ReplaceUUIDWithPlaceholder(c.Path())

		traceCtx := c.UserContext()
		// Compatibility note: trace extraction currently trusts the internal-service
		// User-Agent heuristic. This is an interoperability hint, not an authenticated
		// trust boundary, and is preserved to avoid changing existing caller behavior.
		if commons.IsInternalLerianService(c.Get(cn.HeaderUserAgent)) {
			traceCtx = opentelemetry.ExtractHTTPContext(traceCtx, c)
		}

		ctx, span := tracer.Start(traceCtx, routePathWithMethod, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		ctx = commons.ContextWithTracer(ctx, tracer)
		ctx = commons.ContextWithMetricFactory(ctx, effectiveTelemetry.MetricsFactory)
		c.SetUserContext(ctx)

		err := tm.collectMetrics(ctx)
		if err != nil {
			opentelemetry.HandleSpanError(span, "Failed to collect metrics", err)
		}

		err = c.Next()

		statusCode := c.Response().StatusCode()
		span.SetAttributes(
			attribute.String("http.request.method", c.Method()),
			attribute.String("url.path", sanitizeURL(c.OriginalURL())),
			attribute.String("http.route", c.Route().Path),
			attribute.String("url.scheme", c.Protocol()),
			attribute.String("server.address", c.Hostname()),
			attribute.String("user_agent.original", c.Get(cn.HeaderUserAgent)),
			attribute.Int("http.response.status_code", statusCode),
		)

		if err != nil {
			opentelemetry.HandleSpanError(span, "handler error", err)
		} else if statusCode >= 500 {
			span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", statusCode))
		}

		return err
	}
}

// EndTracingSpans is a middleware that ends the tracing spans.
func (tm *TelemetryMiddleware) EndTracingSpans(c *fiber.Ctx) error {
	if c == nil {
		return ErrContextNotFound
	}

	originalCtx := c.UserContext()
	err := c.Next()

	endCtx := c.UserContext()
	if endCtx == nil {
		endCtx = originalCtx
	}

	if endCtx != nil {
		trace.SpanFromContext(endCtx).End()
	}

	return err
}

// WithTelemetryInterceptor is a gRPC interceptor that adds tracing to the context.
func (tm *TelemetryMiddleware) WithTelemetryInterceptor(tl *opentelemetry.Telemetry) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		ctx = normalizeGRPCContext(ctx)

		effectiveTelemetry := tl
		if effectiveTelemetry == nil && tm != nil {
			effectiveTelemetry = tm.Telemetry
		}

		if effectiveTelemetry == nil {
			return handler(ctx, req)
		}

		requestID := resolveGRPCRequestID(ctx, req)
		ctx = commons.ContextWithHeaderID(ctx, requestID)

		if effectiveTelemetry.TracerProvider == nil {
			return handler(ctx, req)
		}

		tracer := effectiveTelemetry.TracerProvider.Tracer(effectiveTelemetry.LibraryName)

		methodName := "unknown"
		if info != nil {
			methodName = info.FullMethod
		}

		ctx = commons.ContextWithSpanAttributes(ctx,
			attribute.String("app.request.request_id", requestID),
			attribute.String("grpc.method", methodName),
		)

		traceCtx := ctx
		// Compatibility note: trace extraction currently trusts the internal-service
		// User-Agent heuristic. This is an interoperability hint, not an authenticated
		// trust boundary, and is preserved to avoid changing existing caller behavior.
		if commons.IsInternalLerianService(getGRPCUserAgent(ctx)) {
			md, _ := metadata.FromIncomingContext(ctx)
			traceCtx = opentelemetry.ExtractGRPCContext(ctx, md)
		}

		ctx, span := tracer.Start(traceCtx, methodName, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		ctx = commons.ContextWithTracer(ctx, tracer)
		ctx = commons.ContextWithMetricFactory(ctx, effectiveTelemetry.MetricsFactory)

		err := tm.collectMetrics(ctx)
		if err != nil {
			opentelemetry.HandleSpanError(span, "Failed to collect metrics", err)
		}

		resp, err := handler(ctx, req)

		grpcStatusCode := status.Code(err)
		span.SetAttributes(
			attribute.String("rpc.method", methodName),
			attribute.Int("rpc.grpc.status_code", int(grpcStatusCode)),
		)

		if err != nil {
			opentelemetry.HandleSpanError(span, "gRPC handler error", err)
		}

		return resp, err
	}
}

// EndTracingSpansInterceptor is a gRPC interceptor that ends the tracing spans.
func (tm *TelemetryMiddleware) EndTracingSpansInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		resp, err := handler(ctx, req)
		trace.SpanFromContext(ctx).End()

		return resp, err
	}
}
