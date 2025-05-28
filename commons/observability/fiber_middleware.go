package observability

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// FiberMiddlewareOption is a function type for configuring the Fiber middleware
type FiberMiddlewareOption func(*fiberMiddleware) error

// fiberMiddleware is the internal implementation of the Fiber middleware
type fiberMiddleware struct {
	provider         Provider
	ignorePaths      []string
	ignoreHeaders    []string
	maskedParams     []string
	hideBody         bool
	extractUserID    func(*fiber.Ctx) string
	extractRequestID func(*fiber.Ctx) string

	// Metrics
	requestCounter  metric.Int64Counter
	requestDuration metric.Float64Histogram
	requestSize     metric.Int64Histogram
	responseSize    metric.Int64Histogram
	activeRequests  metric.Int64UpDownCounter
}

// WithIgnorePathsFiber specifies URL paths that should not be traced
func WithIgnorePathsFiber(paths ...string) FiberMiddlewareOption {
	return func(m *fiberMiddleware) error {
		if len(paths) == 0 {
			return errors.New("at least one path must be provided")
		}

		m.ignorePaths = append(m.ignorePaths, paths...)

		return nil
	}
}

// WithIgnoreHeadersFiber specifies headers that should not be logged
func WithIgnoreHeadersFiber(headers ...string) FiberMiddlewareOption {
	return func(m *fiberMiddleware) error {
		if len(headers) == 0 {
			return errors.New("at least one header must be provided")
		}

		headerMap := make(map[string]struct{})
		for _, h := range m.ignoreHeaders {
			headerMap[strings.ToLower(h)] = struct{}{}
		}

		for _, h := range headers {
			headerMap[strings.ToLower(h)] = struct{}{}
		}

		m.ignoreHeaders = make([]string, 0, len(headerMap))
		for h := range headerMap {
			m.ignoreHeaders = append(m.ignoreHeaders, h)
		}

		return nil
	}
}

// WithMaskedParamsFiber specifies query parameters that should have their values masked
func WithMaskedParamsFiber(params ...string) FiberMiddlewareOption {
	return func(m *fiberMiddleware) error {
		if len(params) == 0 {
			return errors.New("at least one parameter must be provided")
		}

		m.maskedParams = append(m.maskedParams, params...)

		return nil
	}
}

// WithUserIDExtractor sets a function to extract user ID from the request
func WithUserIDExtractor(fn func(*fiber.Ctx) string) FiberMiddlewareOption {
	return func(m *fiberMiddleware) error {
		if fn == nil {
			return errors.New("user ID extractor cannot be nil")
		}

		m.extractUserID = fn

		return nil
	}
}

// WithRequestIDExtractor sets a function to extract request ID from the request
func WithRequestIDExtractor(fn func(*fiber.Ctx) string) FiberMiddlewareOption {
	return func(m *fiberMiddleware) error {
		if fn == nil {
			return errors.New("request ID extractor cannot be nil")
		}

		m.extractRequestID = fn

		return nil
	}
}

// WithSecurityDefaultsFiber sets all default security options
func WithSecurityDefaultsFiber() FiberMiddlewareOption {
	return func(m *fiberMiddleware) error {
		m.ignoreHeaders = []string{
			"authorization",
			"cookie",
			"set-cookie",
			"x-api-key",
			"x-auth-token",
			"x-forwarded-authorization",
			"x-jwt-token",
		}
		m.maskedParams = []string{
			"access_token",
			"api_key",
			"apikey",
			"auth_token",
			"key",
			"password",
			"secret",
			"token",
			"jwt",
			"refresh_token",
		}
		m.hideBody = true

		return nil
	}
}

// NewFiberMiddleware creates a new Fiber middleware for tracing and metrics
func NewFiberMiddleware(provider Provider, opts ...FiberMiddlewareOption) (fiber.Handler, error) {
	if provider == nil {
		// Return a no-op middleware
		return func(c *fiber.Ctx) error {
			return c.Next()
		}, nil
	}

	// Create with default configuration
	m := &fiberMiddleware{
		provider: provider,
		ignorePaths: []string{
			"/health",
			"/healthz",
			"/metrics",
			"/ready",
			"/readyz",
		},
		ignoreHeaders: []string{
			"authorization",
			"cookie",
			"set-cookie",
			"x-api-key",
			"x-auth-token",
		},
		maskedParams: []string{
			"access_token",
			"api_key",
			"apikey",
			"auth_token",
			"key",
			"password",
			"secret",
			"token",
		},
		extractRequestID: func(c *fiber.Ctx) string {
			return c.Get("X-Request-ID")
		},
	}

	// Apply options
	for _, opt := range opts {
		if err := opt(m); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	// Initialize metrics if provider is enabled
	if provider.IsEnabled() {
		if err := m.initMetrics(); err != nil {
			return nil, fmt.Errorf("failed to initialize metrics: %w", err)
		}
	}

	return m.middleware, nil
}

// initMetrics initializes the middleware metrics
func (m *fiberMiddleware) initMetrics() error {
	meter := m.provider.Meter()

	requestCounter, err := meter.Int64Counter(
		"http.server.request_count",
		metric.WithDescription("Total number of HTTP requests"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return fmt.Errorf("failed to create request counter: %w", err)
	}

	m.requestCounter = requestCounter

	requestDuration, err := meter.Float64Histogram(
		"http.server.duration",
		metric.WithDescription("HTTP request duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("failed to create request duration histogram: %w", err)
	}

	m.requestDuration = requestDuration

	requestSize, err := meter.Int64Histogram(
		"http.server.request_content_length",
		metric.WithDescription("HTTP request body size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return fmt.Errorf("failed to create request size histogram: %w", err)
	}

	m.requestSize = requestSize

	responseSize, err := meter.Int64Histogram(
		"http.server.response_content_length",
		metric.WithDescription("HTTP response body size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return fmt.Errorf("failed to create response size histogram: %w", err)
	}

	m.responseSize = responseSize

	activeRequests, err := meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of active HTTP requests"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return fmt.Errorf("failed to create active requests counter: %w", err)
	}

	m.activeRequests = activeRequests

	return nil
}

// middleware is the actual Fiber handler
func (m *fiberMiddleware) middleware(c *fiber.Ctx) error {
	// Check if we should ignore this path
	path := c.Path()
	for _, ignorePath := range m.ignorePaths {
		if strings.HasPrefix(path, ignorePath) {
			return c.Next()
		}
	}

	// Extract trace context from headers
	ctx := otel.GetTextMapPropagator().Extract(
		c.UserContext(),
		propagation.HeaderCarrier(c.GetReqHeaders()),
	)

	// Start span
	spanName := fmt.Sprintf("%s %s", c.Method(), c.Route().Path)

	ctx, span := m.provider.Tracer().Start(
		ctx,
		spanName,
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	// Update context with provider
	ctx = WithProvider(ctx, m.provider)
	c.SetUserContext(ctx)

	// Extract request attributes
	attrs := []attribute.KeyValue{
		attribute.String("http.method", c.Method()),
		attribute.String("http.route", c.Route().Path),
		attribute.String("http.path", path),
		attribute.String("http.scheme", c.Protocol()),
		attribute.String("http.host", string(c.Request().Host())),
		attribute.String("http.target", c.OriginalURL()),
		attribute.String("http.user_agent", c.Get("User-Agent")),
		attribute.String("net.peer.ip", c.IP()),
		attribute.String(KeyOperationName, spanName),
		attribute.String(KeyOperationType, "http.server"),
	}

	// Add request ID if available
	if m.extractRequestID != nil {
		if requestID := m.extractRequestID(c); requestID != "" {
			attrs = append(attrs, attribute.String("request.id", requestID))
		}
	}

	// Add user ID if available
	if m.extractUserID != nil {
		if userID := m.extractUserID(c); userID != "" {
			attrs = append(attrs, attribute.String("user.id", userID))
		}
	}

	// Set span attributes
	span.SetAttributes(attrs...)

	// Add request headers to span (excluding sensitive ones)
	c.Request().Header.VisitAll(func(key, value []byte) {
		keyStr := string(key)
		if !m.isIgnoredHeader(keyStr) {
			span.SetAttributes(attribute.String("http.request.header."+strings.ToLower(keyStr), string(value)))
		}
	})

	// Record active request
	if m.activeRequests != nil {
		m.activeRequests.Add(ctx, 1, metric.WithAttributes(attrs...))
		defer m.activeRequests.Add(ctx, -1, metric.WithAttributes(attrs...))
	}

	// Record request size
	if m.requestSize != nil && c.Request().Header.ContentLength() > 0 {
		m.requestSize.Record(ctx, int64(c.Request().Header.ContentLength()), metric.WithAttributes(attrs...))
	}

	// Record start time
	start := time.Now()

	// Process request with panic recovery
	var err error

	func() {
		defer func() {
			if r := recover(); r != nil {
				// Convert panic to error
				err = fmt.Errorf("panic recovered: %v", r)
				// Set 500 status code for panic
				c.Status(500)
			}
		}()

		err = c.Next()
	}()

	// Calculate duration
	duration := time.Since(start)

	// Get response status
	statusCode := c.Response().StatusCode()

	// Add response attributes
	responseAttrs := append(attrs,
		attribute.Int("http.status_code", statusCode),
		attribute.String("http.status_class", fmt.Sprintf("%dxx", statusCode/100)),
	)

	// Update span with response info
	span.SetAttributes(attribute.Int("http.status_code", statusCode))

	// Handle error
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		responseAttrs = append(responseAttrs,
			attribute.Bool("error", true),
			attribute.String("error.type", fmt.Sprintf("%T", err)),
			attribute.String("error.message", err.Error()),
		)
	} else if statusCode >= 400 {
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", statusCode))
		span.SetAttributes(attribute.Bool("error", true))
	} else {
		span.SetStatus(codes.Ok, "")
	}

	// Add response headers to span (excluding sensitive ones)
	c.Response().Header.VisitAll(func(key, value []byte) {
		keyStr := string(key)
		if !m.isIgnoredHeader(keyStr) {
			span.SetAttributes(attribute.String("http.response.header."+strings.ToLower(keyStr), string(value)))
		}
	})

	// Record metrics
	if m.requestCounter != nil {
		m.requestCounter.Add(ctx, 1, metric.WithAttributes(responseAttrs...))
	}

	if m.requestDuration != nil {
		m.requestDuration.Record(ctx, float64(duration.Milliseconds()), metric.WithAttributes(responseAttrs...))
	}

	// Record response size
	if m.responseSize != nil {
		responseSize := len(c.Response().Body())
		if responseSize > 0 {
			m.responseSize.Record(ctx, int64(responseSize), metric.WithAttributes(responseAttrs...))
		}
	}

	// Log request
	logger := m.provider.Logger().WithSpan(span)
	fields := map[string]interface{}{
		"method":        c.Method(),
		"path":          path,
		"status":        statusCode,
		"duration_ms":   duration.Milliseconds(),
		"request_size":  c.Request().Header.ContentLength(),
		"response_size": len(c.Response().Body()),
		"ip":            c.IP(),
		"user_agent":    c.Get("User-Agent"),
	}

	if m.extractRequestID != nil {
		if requestID := m.extractRequestID(c); requestID != "" {
			fields["request_id"] = requestID
		}
	}

	if m.extractUserID != nil {
		if userID := m.extractUserID(c); userID != "" {
			fields["user_id"] = userID
		}
	}

	if err != nil {
		fields["error"] = err.Error()
		logger.With(fields).Error("Request failed")
	} else if statusCode >= 400 {
		logger.With(fields).Warn("Request completed with error status")
	} else {
		logger.With(fields).Info("Request completed successfully")
	}

	return err
}

// isIgnoredHeader checks if a header should be ignored (case-insensitive)
func (m *fiberMiddleware) isIgnoredHeader(header string) bool {
	lowerHeader := strings.ToLower(header)
	for _, ignored := range m.ignoreHeaders {
		if lowerHeader == ignored {
			return true
		}
	}

	return false
}
