package observability

import (
	"errors"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ObservabilityMiddleware provides comprehensive observability for HTTP requests.
// The type name intentionally matches the package name for clarity in external usage.
type ObservabilityMiddleware struct {
	serviceName    string
	tracerProvider trace.TracerProvider
	metricProvider metric.MeterProvider
	logger         *log.StructuredLogger
	tracer         trace.Tracer
	meter          metric.Meter

	// Metrics
	requestCounter  metric.Int64Counter
	requestDuration metric.Float64Histogram
	requestSize     metric.Int64Histogram
	responseSize    metric.Int64Histogram
	activeRequests  metric.Int64UpDownCounter
}

// NewObservabilityMiddleware creates a new observability middleware
func NewObservabilityMiddleware(
	serviceName string,
	tracerProvider trace.TracerProvider,
	metricProvider metric.MeterProvider,
	logger *log.StructuredLogger,
) (*ObservabilityMiddleware, error) {
	// Validate required parameters
	if tracerProvider == nil {
		return nil, errors.New("tracerProvider cannot be nil")
	}

	if metricProvider == nil {
		return nil, errors.New("metricProvider cannot be nil")
	}

	om := &ObservabilityMiddleware{
		serviceName:    serviceName,
		tracerProvider: tracerProvider,
		metricProvider: metricProvider,
		logger:         logger,
	}

	// Set up tracer and meter
	om.tracer = tracerProvider.Tracer(serviceName)
	om.meter = metricProvider.Meter(serviceName)

	// Initialize metrics
	requestCounter, err := om.meter.Int64Counter(
		"http.server.request_count",
		metric.WithDescription("Total number of HTTP requests"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request counter: %w", err)
	}

	om.requestCounter = requestCounter

	requestDuration, err := om.meter.Float64Histogram(
		"http.server.duration",
		metric.WithDescription("HTTP request duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request duration histogram: %w", err)
	}

	om.requestDuration = requestDuration

	requestSize, err := om.meter.Int64Histogram(
		"http.server.request_content_length",
		metric.WithDescription("HTTP request body size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request size histogram: %w", err)
	}

	om.requestSize = requestSize

	responseSize, err := om.meter.Int64Histogram(
		"http.server.response_content_length",
		metric.WithDescription("HTTP response body size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create response size histogram: %w", err)
	}

	om.responseSize = responseSize

	activeRequests, err := om.meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of active HTTP requests"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create active requests counter: %w", err)
	}

	om.activeRequests = activeRequests

	return om, nil
}

// Middleware returns the fiber middleware handler
func (om *ObservabilityMiddleware) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		// Extract request attributes
		attrs := []attribute.KeyValue{
			attribute.String("http.method", c.Method()),
			attribute.String("http.route", c.Route().Path),
			attribute.String("http.scheme", c.Protocol()),
			attribute.String("http.host", string(c.Request().Host())),
			attribute.String("http.target", c.OriginalURL()),
			attribute.String("http.user_agent", c.Get("User-Agent")),
			attribute.String("net.peer.ip", c.IP()),
		}

		// Start span
		ctx, span := om.tracer.Start(
			c.UserContext(),
			fmt.Sprintf("%s %s", c.Method(), c.Route().Path),
			trace.WithAttributes(attrs...),
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		// Update context
		c.SetUserContext(ctx)

		// Record active request
		om.activeRequests.Add(ctx, 1, metric.WithAttributes(attrs...))
		defer om.activeRequests.Add(ctx, -1, metric.WithAttributes(attrs...))

		// Record request size
		if c.Request().Header.ContentLength() > 0 {
			om.requestSize.Record(
				ctx,
				int64(c.Request().Header.ContentLength()),
				metric.WithAttributes(attrs...),
			)
		}

		// Process request
		err := c.Next()

		// Calculate duration
		duration := time.Since(start).Milliseconds()

		// Add response attributes
		statusCode := c.Response().StatusCode()
		attrs = append(attrs,
			attribute.Int("http.status_code", statusCode),
			attribute.String("http.status_class", fmt.Sprintf("%dxx", statusCode/100)),
		)

		// Update span
		span.SetAttributes(attrs...)

		if err != nil {
			span.RecordError(err)
			attrs = append(attrs, attribute.String("error.type", fmt.Sprintf("%T", err)))
		}

		// Record metrics
		om.requestCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
		om.requestDuration.Record(ctx, float64(duration), metric.WithAttributes(attrs...))

		// Record response size
		responseSize := len(c.Response().Body())
		if responseSize > 0 {
			om.responseSize.Record(ctx, int64(responseSize), metric.WithAttributes(attrs...))
		}

		// Log request
		fields := map[string]any{
			"method":        c.Method(),
			"path":          c.Path(),
			"status":        statusCode,
			"duration_ms":   duration,
			"request_size":  c.Request().Header.ContentLength(),
			"response_size": responseSize,
			"ip":            c.IP(),
			"user_agent":    c.Get("User-Agent"),
		}

		if err != nil {
			fields["error"] = err.Error()
			om.logger.WithFields(fields).Error("Request failed")
		} else if statusCode >= 400 {
			om.logger.WithFields(fields).Warn("Request completed with error status")
		} else {
			om.logger.WithFields(fields).Info("Request completed successfully")
		}

		return err
	}
}
