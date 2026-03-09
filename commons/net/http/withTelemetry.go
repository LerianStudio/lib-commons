package http

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons"
	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/runtime"
	"github.com/LerianStudio/lib-commons/v4/commons/security"
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

// Metrics collector singleton state.
var (
	metricsCollectorOnce     = &sync.Once{}
	metricsCollectorShutdown chan struct{}
	metricsCollectorMu       sync.Mutex
	metricsCollectorStarted  bool
	metricsCollectorInitErr  error
)

// telemetryRuntimeLogger returns the runtime logger from the telemetry middleware, or nil.
func telemetryRuntimeLogger(tm *TelemetryMiddleware) runtime.Logger {
	if tm == nil || tm.Telemetry == nil {
		return nil
	}

	return tm.Telemetry.Logger
}

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
			// url.path holds the concrete request path (sanitized). Use http.route for the low-cardinality template.
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
	ctx := c.UserContext()
	if ctx == nil {
		return nil
	}

	err := c.Next()

	trace.SpanFromContext(ctx).End()

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
		effectiveTelemetry := tl
		if effectiveTelemetry == nil && tm != nil {
			effectiveTelemetry = tm.Telemetry
		}

		if effectiveTelemetry == nil {
			return handler(ctx, req)
		}

		ctx = setGRPCRequestHeaderID(ctx)

		_, _, reqId, _ := commons.NewTrackingFromContext(ctx)

		if effectiveTelemetry.TracerProvider == nil {
			return handler(ctx, req)
		}

		tracer := effectiveTelemetry.TracerProvider.Tracer(effectiveTelemetry.LibraryName)

		methodName := "unknown"
		if info != nil {
			methodName = info.FullMethod
		}

		ctx = commons.ContextWithSpanAttributes(ctx,
			attribute.String("app.request.request_id", reqId),
			attribute.String("grpc.method", methodName),
		)

		traceCtx := ctx
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

// collectMetrics ensures the background metrics collector goroutine is running.
func (tm *TelemetryMiddleware) collectMetrics(_ context.Context) error {
	return tm.ensureMetricsCollector()
}

// getMetricsCollectionInterval returns the metrics collection interval.
// Can be configured via METRICS_COLLECTION_INTERVAL environment variable.
// Accepts Go duration format (e.g., "10s", "1m", "500ms").
// Falls back to DefaultMetricsCollectionInterval if not set or invalid.
func getMetricsCollectionInterval() time.Duration {
	if envInterval := os.Getenv("METRICS_COLLECTION_INTERVAL"); envInterval != "" {
		if parsed, err := time.ParseDuration(envInterval); err == nil && parsed > 0 {
			return parsed
		}
	}

	return DefaultMetricsCollectionInterval
}

// ensureMetricsCollector lazily starts the background metrics collector singleton.
func (tm *TelemetryMiddleware) ensureMetricsCollector() error {
	if tm == nil || tm.Telemetry == nil {
		return nil
	}

	if tm.Telemetry.MeterProvider == nil {
		return nil
	}

	metricsCollectorMu.Lock()
	defer metricsCollectorMu.Unlock()

	if metricsCollectorStarted {
		return nil
	}

	if metricsCollectorInitErr != nil {
		// Reset to allow retry after transient init failures
		metricsCollectorOnce = &sync.Once{}
		metricsCollectorInitErr = nil
	}

	metricsCollectorOnce.Do(func() {
		factory := tm.Telemetry.MetricsFactory
		if factory == nil {
			metricsCollectorInitErr = errors.New("telemetry MetricsFactory is nil, cannot start system metrics collector")
			return
		}

		metricsCollectorShutdown = make(chan struct{})
		ticker := time.NewTicker(getMetricsCollectionInterval())

		runtime.SafeGoWithContextAndComponent(
			context.Background(),
			telemetryRuntimeLogger(tm),
			"http",
			"metrics_collector",
			runtime.KeepRunning,
			func(_ context.Context) {
				commons.GetCPUUsage(context.Background(), factory)
				commons.GetMemUsage(context.Background(), factory)

				for {
					select {
					case <-metricsCollectorShutdown:
						ticker.Stop()
						return
					case <-ticker.C:
						commons.GetCPUUsage(context.Background(), factory)
						commons.GetMemUsage(context.Background(), factory)
					}
				}
			},
		)

		metricsCollectorStarted = true
	})

	return metricsCollectorInitErr
}

// StopMetricsCollector stops the background metrics collector goroutine.
// Should be called during application shutdown for graceful cleanup.
// After calling this function, the collector can be restarted by new requests.
//
// Implementation note: This function intentionally resets sync.Once to a new instance
// to allow the collector to be restarted after being stopped. This is an unusual but
// intentional pattern - the mutex ensures thread-safety during the reset operation,
// preventing race conditions between Stop and subsequent Start calls.
func StopMetricsCollector() {
	metricsCollectorMu.Lock()
	defer metricsCollectorMu.Unlock()

	if metricsCollectorStarted && metricsCollectorShutdown != nil {
		close(metricsCollectorShutdown)

		metricsCollectorStarted = false
		metricsCollectorOnce = &sync.Once{}
		metricsCollectorInitErr = nil
	}
}

// isRouteExcludedFromList reports whether the request path matches any excluded route prefix.
// This standalone function is used to evaluate route exclusions independently of whether
// the TelemetryMiddleware receiver is nil.
func isRouteExcludedFromList(c *fiber.Ctx, excludedRoutes []string) bool {
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

	userAgents := md.Get(strings.ToLower(cn.HeaderUserAgent))
	if len(userAgents) == 0 {
		return ""
	}

	return userAgents[0]
}
