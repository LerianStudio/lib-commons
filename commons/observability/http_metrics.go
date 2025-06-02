package observability

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HTTPMetrics provides HTTP-specific metrics collection
type HTTPMetrics struct {
	meter metric.Meter

	// Request metrics
	requestCounter    metric.Int64Counter
	requestDuration   metric.Float64Histogram
	requestSizeBytes  metric.Int64Histogram
	responseSizeBytes metric.Int64Histogram

	// Connection metrics
	activeConnections  metric.Int64UpDownCounter
	connectionDuration metric.Float64Histogram

	// Error metrics
	errorCounter   metric.Int64Counter
	timeoutCounter metric.Int64Counter

	// Rate limiting metrics
	rateLimitCounter metric.Int64Counter
	rateLimitBlocked metric.Int64Counter
}

// NewHTTPMetrics creates a new HTTP metrics instance
func NewHTTPMetrics(meter metric.Meter) (*HTTPMetrics, error) {
	hm := &HTTPMetrics{meter: meter}

	// Initialize request metrics
	requestCounter, err := meter.Int64Counter(
		"http.requests.total",
		metric.WithDescription("Total number of HTTP requests"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request counter: %w", err)
	}
	hm.requestCounter = requestCounter

	requestDuration, err := meter.Float64Histogram(
		"http.request.duration",
		metric.WithDescription("HTTP request duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request duration histogram: %w", err)
	}
	hm.requestDuration = requestDuration

	requestSizeBytes, err := meter.Int64Histogram(
		"http.request.size.bytes",
		metric.WithDescription("HTTP request size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request size histogram: %w", err)
	}
	hm.requestSizeBytes = requestSizeBytes

	responseSizeBytes, err := meter.Int64Histogram(
		"http.response.size.bytes",
		metric.WithDescription("HTTP response size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create response size histogram: %w", err)
	}
	hm.responseSizeBytes = responseSizeBytes

	// Initialize connection metrics
	activeConnections, err := meter.Int64UpDownCounter(
		"http.connections.active",
		metric.WithDescription("Number of active HTTP connections"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create active connections counter: %w", err)
	}
	hm.activeConnections = activeConnections

	connectionDuration, err := meter.Float64Histogram(
		"http.connection.duration",
		metric.WithDescription("HTTP connection duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection duration histogram: %w", err)
	}
	hm.connectionDuration = connectionDuration

	// Initialize error metrics
	errorCounter, err := meter.Int64Counter(
		"http.errors.total",
		metric.WithDescription("Total number of HTTP errors"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create error counter: %w", err)
	}
	hm.errorCounter = errorCounter

	timeoutCounter, err := meter.Int64Counter(
		"http.timeouts.total",
		metric.WithDescription("Total number of HTTP timeouts"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create timeout counter: %w", err)
	}
	hm.timeoutCounter = timeoutCounter

	// Initialize rate limiting metrics
	rateLimitCounter, err := meter.Int64Counter(
		"http.rate_limit.requests",
		metric.WithDescription("Total number of rate limited requests"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create rate limit counter: %w", err)
	}
	hm.rateLimitCounter = rateLimitCounter

	rateLimitBlocked, err := meter.Int64Counter(
		"http.rate_limit.blocked",
		metric.WithDescription("Total number of blocked requests due to rate limiting"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create rate limit blocked counter: %w", err)
	}
	hm.rateLimitBlocked = rateLimitBlocked

	return hm, nil
}

// RecordRequest records HTTP request metrics
func (hm *HTTPMetrics) RecordRequest(
	ctx context.Context,
	method string,
	path string,
	statusCode int,
	duration time.Duration,
	requestSize int64,
	responseSize int64,
) {
	attrs := []attribute.KeyValue{
		attribute.String("method", method),
		attribute.String("path", path),
		attribute.String("status_code", strconv.Itoa(statusCode)),
		attribute.String("status_class", getStatusClass(statusCode)),
	}

	hm.requestCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	hm.requestDuration.Record(
		ctx,
		float64(duration.Nanoseconds())/1e6,
		metric.WithAttributes(attrs...),
	)

	if requestSize > 0 {
		hm.requestSizeBytes.Record(ctx, requestSize, metric.WithAttributes(attrs...))
	}

	if responseSize > 0 {
		hm.responseSizeBytes.Record(ctx, responseSize, metric.WithAttributes(attrs...))
	}
}

// RecordError records HTTP error metrics
func (hm *HTTPMetrics) RecordError(
	ctx context.Context,
	method string,
	path string,
	errorType string,
	statusCode int,
) {
	attrs := []attribute.KeyValue{
		attribute.String("method", method),
		attribute.String("path", path),
		attribute.String("error_type", errorType),
		attribute.String("status_code", strconv.Itoa(statusCode)),
	}

	hm.errorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordTimeout records HTTP timeout metrics
func (hm *HTTPMetrics) RecordTimeout(
	ctx context.Context,
	method string,
	path string,
	timeoutType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("method", method),
		attribute.String("path", path),
		attribute.String("timeout_type", timeoutType),
	}

	hm.timeoutCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordConnectionOpen records when a new HTTP connection is opened
func (hm *HTTPMetrics) RecordConnectionOpen(ctx context.Context, remoteAddr string) {
	attrs := []attribute.KeyValue{
		attribute.String("remote_addr", remoteAddr),
	}

	hm.activeConnections.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordConnectionClose records when an HTTP connection is closed
func (hm *HTTPMetrics) RecordConnectionClose(
	ctx context.Context,
	remoteAddr string,
	duration time.Duration,
) {
	attrs := []attribute.KeyValue{
		attribute.String("remote_addr", remoteAddr),
	}

	hm.activeConnections.Add(ctx, -1, metric.WithAttributes(attrs...))
	hm.connectionDuration.Record(
		ctx,
		float64(duration.Nanoseconds())/1e6,
		metric.WithAttributes(attrs...),
	)
}

// RecordRateLimit records rate limiting metrics
func (hm *HTTPMetrics) RecordRateLimit(
	ctx context.Context,
	clientID string,
	endpoint string,
	action string, // "allowed", "blocked", "throttled"
) {
	attrs := []attribute.KeyValue{
		attribute.String("client_id", clientID),
		attribute.String("endpoint", endpoint),
		attribute.String("action", action),
	}

	hm.rateLimitCounter.Add(ctx, 1, metric.WithAttributes(attrs...))

	if action == "blocked" {
		hm.rateLimitBlocked.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// getStatusClass returns the HTTP status code class (1xx, 2xx, 3xx, 4xx, 5xx)
func getStatusClass(statusCode int) string {
	switch {
	case statusCode >= 100 && statusCode < 200:
		return "1xx"
	case statusCode >= 200 && statusCode < 300:
		return "2xx"
	case statusCode >= 300 && statusCode < 400:
		return "3xx"
	case statusCode >= 400 && statusCode < 500:
		return "4xx"
	case statusCode >= 500 && statusCode < 600:
		return "5xx"
	default:
		return "unknown"
	}
}
