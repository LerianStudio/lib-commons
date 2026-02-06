package http

import (
	"context"
	"net/url"
	"strings"

	"github.com/LerianStudio/lib-commons/v2/commons"
	cn "github.com/LerianStudio/lib-commons/v2/commons/constants"
	"github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v2/commons/security"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

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
		if len(excludedRoutes) > 0 && tm.isRouteExcluded(c, excludedRoutes) {
			return c.Next()
		}

		setRequestHeaderID(c)

		ctx := c.UserContext()

		_, _, reqId, _ := commons.NewTrackingFromContext(ctx)

		c.SetUserContext(commons.ContextWithSpanAttributes(ctx,
			attribute.String("app.request.request_id", reqId),
		))

		tracer := otel.Tracer(tl.LibraryName)
		routePathWithMethod := c.Method() + " " + commons.ReplaceUUIDWithPlaceholder(c.Path())

		traceCtx := ctx
		if commons.IsInternalLerianService(c.Get(cn.HeaderUserAgent)) {
			traceCtx = opentelemetry.ExtractHTTPContext(c)
		}

		ctx, span := tracer.Start(traceCtx, routePathWithMethod, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		span.SetAttributes(
			attribute.String("http.method", c.Method()),
			attribute.String("http.url", c.OriginalURL()),
			attribute.String("http.route", c.Route().Path),
			attribute.String("http.scheme", c.Protocol()),
			attribute.String("http.host", c.Hostname()),
			attribute.String("http.user_agent", c.Get("User-Agent")),
		)

		ctx = commons.ContextWithTracer(ctx, tracer)
		ctx = commons.ContextWithMetricFactory(ctx, tl.MetricsFactory)

		c.SetUserContext(ctx)

		err := tm.collectMetrics(ctx)
		if err != nil {
			opentelemetry.HandleSpanError(&span, "Failed to collect metrics", err)
		}

		err = c.Next()

		span.SetAttributes(
			attribute.Int("http.status_code", c.Response().StatusCode()),
		)

		return err
	}
}

// EndTracingSpans is a middleware that ends the tracing spans.
func (tm *TelemetryMiddleware) EndTracingSpans(c *fiber.Ctx) error {
	ctx := c.UserContext()
	if ctx == nil {
		return nil
	}

	err := c.Next()

	go func() {
		trace.SpanFromContext(ctx).End()
	}()

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
		ctx = setGRPCRequestHeaderID(ctx)

		_, _, reqId, _ := commons.NewTrackingFromContext(ctx)
		tracer := otel.Tracer(tl.LibraryName)

		ctx = commons.ContextWithSpanAttributes(ctx,
			attribute.String("app.request.request_id", reqId),
			attribute.String("grpc.method", info.FullMethod),
		)

		traceCtx := ctx
		if commons.IsInternalLerianService(getGRPCUserAgent(ctx)) {
			traceCtx = opentelemetry.ExtractGRPCContext(ctx)
		}

		ctx, span := tracer.Start(traceCtx, info.FullMethod, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		ctx = commons.ContextWithTracer(ctx, tracer)
		ctx = commons.ContextWithMetricFactory(ctx, tl.MetricsFactory)

		err := tm.collectMetrics(ctx)
		if err != nil {
			opentelemetry.HandleSpanError(&span, "Failed to collect metrics", err)
		}

		resp, err := handler(ctx, req)

		span.SetAttributes(
			attribute.Int("grpc.status_code", int(status.Code(err))),
		)

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

		go func() {
			trace.SpanFromContext(ctx).End()
		}()

		return resp, err
	}
}

func (tm *TelemetryMiddleware) collectMetrics(ctx context.Context) error {
	cpuGauge, err := otel.Meter(tm.Telemetry.ServiceName).Int64Gauge("system.cpu.usage", metric.WithUnit("percentage"))
	if err != nil {
		return err
	}

	go commons.GetCPUUsage(ctx, cpuGauge)

	memGauge, err := otel.Meter(tm.Telemetry.ServiceName).Int64Gauge("system.mem.usage", metric.WithUnit("percentage"))
	if err != nil {
		return err
	}

	go commons.GetMemUsage(ctx, memGauge)

	return nil
}

func (tm *TelemetryMiddleware) isRouteExcluded(c *fiber.Ctx, excludedRoutes []string) bool {
	for _, route := range excludedRoutes {
		if strings.HasPrefix(c.Path(), route) {
			return true
		}
	}

	return false
}

// sanitizeURL removes or obfuscates sensitive query parameters from URLs
// to prevent exposing tokens, API keys, and other sensitive data in telemetry.
func sanitizeURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	if parsed.RawQuery == "" {
		return rawURL
	}

	query := parsed.Query()
	modified := false

	for key := range query {
		if security.IsSensitiveField(key) {
			query.Set(key, cn.ObfuscatedValue)

			modified = true
		}
	}

	if !modified {
		return rawURL
	}

	parsed.RawQuery = query.Encode()

	return parsed.String()
}

// getGRPCUserAgent extracts the User-Agent from incoming gRPC metadata.
// Returns empty string if the metadata is not present or doesn't contain user-agent.
func getGRPCUserAgent(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || md == nil {
		return ""
	}

	userAgents := md.Get("user-agent")
	if len(userAgents) == 0 {
		return ""
	}

	return userAgents[0]
}
