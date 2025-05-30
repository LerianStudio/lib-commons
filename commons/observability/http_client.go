package observability

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTPClientOption is a function type for configuring the HTTP client middleware
type HTTPClientOption func(*httpClientMiddleware) error

// httpClientMiddleware is the internal implementation of the HTTP client middleware
type httpClientMiddleware struct {
	provider      Provider
	ignoreHeaders []string
	ignorePaths   []string
	maskedParams  []string
	hideBody      bool
}

// WithIgnoreHeaders specifies HTTP header names that should not be logged
func WithIgnoreHeaders(headers ...string) HTTPClientOption {
	return func(m *httpClientMiddleware) error {
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

// WithIgnorePaths specifies URL paths that should not be traced
func WithIgnorePaths(paths ...string) HTTPClientOption {
	return func(m *httpClientMiddleware) error {
		if len(paths) == 0 {
			return errors.New("at least one path must be provided")
		}

		m.ignorePaths = append(m.ignorePaths, paths...)

		return nil
	}
}

// WithMaskedParams specifies query parameters that should have their values masked
func WithMaskedParams(params ...string) HTTPClientOption {
	return func(m *httpClientMiddleware) error {
		if len(params) == 0 {
			return errors.New("at least one parameter must be provided")
		}

		m.maskedParams = append(m.maskedParams, params...)

		return nil
	}
}

// WithHideRequestBody specifies whether to hide request bodies from logs
func WithHideRequestBody(hide bool) HTTPClientOption {
	return func(m *httpClientMiddleware) error {
		m.hideBody = hide
		return nil
	}
}

// WithDefaultSensitiveHeaders sets the default list of headers to ignore for security
func WithDefaultSensitiveHeaders() HTTPClientOption {
	return func(m *httpClientMiddleware) error {
		m.ignoreHeaders = []string{
			"authorization",
			"cookie",
			"set-cookie",
			"x-api-key",
			"x-auth-token",
			"x-forwarded-authorization",
			"x-jwt-token",
			"x-middleware-token",
		}

		return nil
	}
}

// WithDefaultSensitiveParams sets the default list of parameters to mask for security
func WithDefaultSensitiveParams() HTTPClientOption {
	return func(m *httpClientMiddleware) error {
		m.maskedParams = []string{
			"access_token",
			"api_key",
			"apikey",
			"auth_token",
			"key",
			"password",
			"secret",
			"token",
			"access-token",
			"jwt",
			"refresh_token",
			"refresh-token",
		}

		return nil
	}
}

// WithSecurityDefaults sets all default security options
func WithSecurityDefaults() HTTPClientOption {
	return func(m *httpClientMiddleware) error {
		if err := WithDefaultSensitiveHeaders()(m); err != nil {
			return err
		}

		if err := WithDefaultSensitiveParams()(m); err != nil {
			return err
		}

		m.hideBody = true

		return nil
	}
}

// NewHTTPClientMiddleware creates a new HTTP client middleware for tracing and metrics
func NewHTTPClientMiddleware(provider Provider, opts ...HTTPClientOption) func(http.RoundTripper) http.RoundTripper {
	if provider == nil {
		// Return a no-op middleware
		return func(next http.RoundTripper) http.RoundTripper {
			return next
		}
	}

	// Create with default configuration
	m := &httpClientMiddleware{
		provider: provider,
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
	}

	// Apply options
	for _, opt := range opts {
		if err := opt(m); err != nil {
			// Log error but continue with other options
			if provider.IsEnabled() && provider.Logger() != nil {
				provider.Logger().Errorf("Failed to apply HTTP client middleware option: %v", err)
			}
		}
	}

	return m.middleware
}

// middleware wraps an http.RoundTripper with tracing and metrics
func (m *httpClientMiddleware) middleware(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if !m.provider.IsEnabled() {
			return next.RoundTrip(req)
		}

		if m.shouldIgnoreRequest(req) {
			return next.RoundTrip(req)
		}

		ctx, span := m.prepareSpan(req)
		defer span.End()

		m.processRequestHeaders(req, span)
		m.injectTraceContext(ctx, req)

		req = req.WithContext(httptrace.WithClientTrace(ctx, m.createClientTrace(span)))

		start := time.Now()
		resp, err := next.RoundTrip(req)
		duration := time.Since(start)

		m.recordRequestMetrics(ctx, req, resp, err, duration)

		if err != nil {
			return m.handleRequestError(span, resp, err)
		}

		m.processResponse(span, resp)

		return resp, nil
	})
}

// shouldIgnoreRequest checks if the request should be ignored based on path
func (m *httpClientMiddleware) shouldIgnoreRequest(req *http.Request) bool {
	for _, path := range m.ignorePaths {
		if strings.HasPrefix(req.URL.Path, path) {
			return true
		}
	}

	return false
}

// prepareSpan creates and configures a new span for the request
func (m *httpClientMiddleware) prepareSpan(req *http.Request) (context.Context, trace.Span) {
	name := fmt.Sprintf("HTTP %s %s", req.Method, req.URL.Path)
	ctx, span := m.provider.Tracer().Start(
		req.Context(),
		name,
		trace.WithSpanKind(trace.SpanKindClient),
	)

	// Add HTTP attributes to span
	span.SetAttributes(
		attribute.String("http.method", req.Method),
		attribute.String("http.url", m.sanitizeURL(req.URL.String())),
		attribute.String("http.host", req.URL.Host),
		attribute.String("http.path", req.URL.Path),
		attribute.String(KeyOperationName, name),
		attribute.String(KeyOperationType, "http.request"),
	)

	return ctx, span
}

// processRequestHeaders adds request headers to the span (excluding sensitive ones)
func (m *httpClientMiddleware) processRequestHeaders(req *http.Request, span trace.Span) {
	for key, values := range req.Header {
		if !m.isIgnoredHeader(key) {
			for i, v := range values {
				if i == 0 {
					span.SetAttributes(attribute.String("http.request.header."+strings.ToLower(key), v))
				}
			}
		}
	}
}

// injectTraceContext injects trace context into request headers
func (m *httpClientMiddleware) injectTraceContext(ctx context.Context, req *http.Request) {
	carrier := propagation.HeaderCarrier(req.Header)
	otel.GetTextMapPropagator().Inject(ctx, carrier)
}

// handleRequestError handles request errors and sets span status
func (m *httpClientMiddleware) handleRequestError(span trace.Span, resp *http.Response, err error) (*http.Response, error) {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)

	return resp, err
}

// processResponse processes the response and adds attributes to span
func (m *httpClientMiddleware) processResponse(span trace.Span, resp *http.Response) {
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	m.processResponseHeaders(span, resp)

	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP status code: %d", resp.StatusCode))
		span.SetAttributes(attribute.Bool("error", true))
	} else {
		span.SetStatus(codes.Ok, "")
	}
}

// processResponseHeaders adds response headers to the span (excluding sensitive ones)
func (m *httpClientMiddleware) processResponseHeaders(span trace.Span, resp *http.Response) {
	for key, values := range resp.Header {
		if !m.isIgnoredHeader(key) {
			for i, v := range values {
				if i == 0 {
					span.SetAttributes(attribute.String("http.response.header."+strings.ToLower(key), v))
				}
			}
		}
	}
}

// isIgnoredHeader checks if a header should be ignored (case-insensitive)
func (m *httpClientMiddleware) isIgnoredHeader(header string) bool {
	lowerHeader := strings.ToLower(header)
	for _, ignored := range m.ignoreHeaders {
		if lowerHeader == ignored {
			return true
		}
	}

	return false
}

// sanitizeURL removes sensitive parameters from the URL
func (m *httpClientMiddleware) sanitizeURL(urlStr string) string {
	// Parse URL to check for sensitive params
	for _, param := range m.maskedParams {
		if strings.Contains(urlStr, param+"=") {
			// Replace the parameter value with [REDACTED]
			parts := strings.Split(urlStr, param+"=")
			if len(parts) > 1 {
				endIdx := strings.IndexAny(parts[1], "&?#")
				if endIdx == -1 {
					parts[1] = "[REDACTED]"
				} else {
					parts[1] = "[REDACTED]" + parts[1][endIdx:]
				}

				urlStr = strings.Join(parts, param+"=")
			}
		}
	}

	return urlStr
}

// recordRequestMetrics records metrics about the HTTP request
func (m *httpClientMiddleware) recordRequestMetrics(ctx context.Context, req *http.Request, resp *http.Response, err error, duration time.Duration) {
	// Create attributes for the metrics
	attrs := []attribute.KeyValue{
		attribute.String(KeyHTTPMethod, req.Method),
		attribute.String(KeyHTTPPath, req.URL.Path),
		attribute.String(KeyHTTPHost, req.URL.Host),
	}

	// Add status code attribute if we have a response
	if resp != nil {
		attrs = append(attrs, attribute.Int(KeyHTTPStatus, resp.StatusCode))
	}

	// Record count
	RecordMetric(ctx, m.provider, MetricRequestTotal, 1, attrs...)

	// Record duration
	RecordDuration(ctx, m.provider, MetricRequestDuration, time.Now().Add(-duration), attrs...)

	// Record error or success
	if err != nil || (resp != nil && resp.StatusCode >= 400) {
		errorStatus := "unknown"
		if resp != nil {
			errorStatus = strconv.Itoa(resp.StatusCode)
		}

		attrs = append(attrs, attribute.String(KeyErrorCode, errorStatus))
		RecordMetric(ctx, m.provider, MetricRequestErrorTotal, 1, attrs...)
	} else {
		RecordMetric(ctx, m.provider, MetricRequestSuccess, 1, attrs...)
	}
}

// createClientTrace creates an httptrace.ClientTrace to track HTTP request lifecycle events
func (m *httpClientMiddleware) createClientTrace(span trace.Span) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(hostPort string) {
			span.AddEvent("http.get_conn", trace.WithAttributes(
				attribute.String("http.host_port", hostPort),
			))
		},
		GotConn: func(info httptrace.GotConnInfo) {
			span.AddEvent("http.got_conn", trace.WithAttributes(
				attribute.Bool("reused", info.Reused),
				attribute.Bool("was_idle", info.WasIdle),
				attribute.String("idle_time", info.IdleTime.String()),
			))
		},
		PutIdleConn: func(err error) {
			attrs := []attribute.KeyValue{}
			if err != nil {
				attrs = append(attrs, attribute.String("error", err.Error()))
			}
			span.AddEvent("http.put_idle_conn", trace.WithAttributes(attrs...))
		},
		DNSStart: func(info httptrace.DNSStartInfo) {
			span.AddEvent("http.dns_start", trace.WithAttributes(
				attribute.String("host", info.Host),
			))
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			attrs := []attribute.KeyValue{}
			if len(info.Addrs) > 0 {
				attrs = append(attrs, attribute.String("address", info.Addrs[0].String()))
			}
			if info.Err != nil {
				attrs = append(attrs, attribute.String("error", info.Err.Error()))
			}
			span.AddEvent("http.dns_done", trace.WithAttributes(attrs...))
		},
		ConnectStart: func(network, addr string) {
			span.AddEvent("http.connect_start", trace.WithAttributes(
				attribute.String("network", network),
				attribute.String("addr", addr),
			))
		},
		ConnectDone: func(network, addr string, err error) {
			attrs := []attribute.KeyValue{
				attribute.String("network", network),
				attribute.String("addr", addr),
			}
			if err != nil {
				attrs = append(attrs, attribute.String("error", err.Error()))
			}
			span.AddEvent("http.connect_done", trace.WithAttributes(attrs...))
		},
		TLSHandshakeStart: func() {
			span.AddEvent("http.tls_handshake_start")
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			attrs := []attribute.KeyValue{
				attribute.String("version", tlsVersionString(state.Version)),
				attribute.String("cipher_suite", tlsCipherSuiteString(state.CipherSuite)),
			}
			if err != nil {
				attrs = append(attrs, attribute.String("error", err.Error()))
			}
			span.AddEvent("http.tls_handshake_done", trace.WithAttributes(attrs...))
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			attrs := []attribute.KeyValue{}
			if info.Err != nil {
				attrs = append(attrs, attribute.String("error", info.Err.Error()))
			}
			span.AddEvent("http.wrote_request", trace.WithAttributes(attrs...))
		},
		GotFirstResponseByte: func() {
			span.AddEvent("http.got_first_response_byte")
		},
	}
}

// roundTripperFunc adapts a function to the RoundTripper interface
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper
func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Helper functions for TLS information

func tlsVersionString(version uint16) string {
	switch version {
	case 0x0301:
		return "TLS 1.0"
	case 0x0302:
		return "TLS 1.1"
	case 0x0303:
		return "TLS 1.2"
	case 0x0304:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("unknown (0x%04x)", version)
	}
}

func tlsCipherSuiteString(cipherSuite uint16) string {
	switch cipherSuite {
	case 0x1301:
		return "TLS_AES_128_GCM_SHA256"
	case 0x1302:
		return "TLS_AES_256_GCM_SHA384"
	case 0x1303:
		return "TLS_CHACHA20_POLY1305_SHA256"
	case 0xc02b:
		return "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"
	case 0xc02c:
		return "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384"
	case 0xc02f:
		return "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"
	case 0xc030:
		return "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"
	default:
		return fmt.Sprintf("unknown (0x%04x)", cipherSuite)
	}
}
