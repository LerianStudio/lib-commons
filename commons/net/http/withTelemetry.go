package http

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

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
	"google.golang.org/grpc/status"
)

// DefaultMetricsCollectionInterval is the default interval for collecting system metrics.
// Can be overridden via METRICS_COLLECTION_INTERVAL environment variable.
const DefaultMetricsCollectionInterval = 5 * time.Second

var (
	metricsCollectorOnce     = &sync.Once{}
	metricsCollectorShutdown chan struct{}
	metricsCollectorMu       sync.Mutex
	metricsCollectorStarted  bool
	metricsCollectorInitErr  error
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

		ctx, span := tracer.Start(opentelemetry.ExtractHTTPContext(c), routePathWithMethod, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		span.SetAttributes(
			attribute.String("http.method", c.Method()),
			attribute.String("http.url", sanitizeURL(c.OriginalURL())),
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

		ctx, span := tracer.Start(opentelemetry.ExtractGRPCContext(ctx), info.FullMethod, trace.WithSpanKind(trace.SpanKindServer))
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

func (tm *TelemetryMiddleware) ensureMetricsCollector() error {
	metricsCollectorMu.Lock()
	defer metricsCollectorMu.Unlock()

	if metricsCollectorStarted {
		return nil
	}

	if metricsCollectorInitErr != nil {
		return metricsCollectorInitErr
	}

	metricsCollectorOnce.Do(func() {
		cpuGauge, err := otel.Meter(tm.Telemetry.ServiceName).Int64Gauge("system.cpu.usage", metric.WithUnit("percentage"))
		if err != nil {
			metricsCollectorInitErr = err
			return
		}

		memGauge, err := otel.Meter(tm.Telemetry.ServiceName).Int64Gauge("system.mem.usage", metric.WithUnit("percentage"))
		if err != nil {
			metricsCollectorInitErr = err
			return
		}

		metricsCollectorShutdown = make(chan struct{})
		ticker := time.NewTicker(getMetricsCollectionInterval())

		go func() {
			commons.GetCPUUsage(context.Background(), cpuGauge)
			commons.GetMemUsage(context.Background(), memGauge)

			for {
				select {
				case <-metricsCollectorShutdown:
					ticker.Stop()
					return
				case <-ticker.C:
					commons.GetCPUUsage(context.Background(), cpuGauge)
					commons.GetMemUsage(context.Background(), memGauge)
				}
			}
		}()

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
