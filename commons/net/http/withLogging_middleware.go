package http

import (
	"context"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons"
	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// logMiddleware holds the logger and configuration used by HTTP and gRPC logging middleware.
type logMiddleware struct {
	Logger              log.Logger
	ObfuscationDisabled bool
}

// LogMiddlewareOption represents the log middleware function as an implementation.
type LogMiddlewareOption func(l *logMiddleware)

// WithCustomLogger is a functional option for logMiddleware.
func WithCustomLogger(logger log.Logger) LogMiddlewareOption {
	return func(l *logMiddleware) {
		if !nilcheck.Interface(logger) {
			l.Logger = logger
		}
	}
}

// WithObfuscationDisabled is a functional option that disables log body obfuscation.
// This is primarily intended for testing and local development.
// In production, use the LOG_OBFUSCATION_DISABLED environment variable.
func WithObfuscationDisabled(disabled bool) LogMiddlewareOption {
	return func(l *logMiddleware) {
		l.ObfuscationDisabled = disabled
	}
}

// buildOpts creates an instance of logMiddleware with options.
func buildOpts(opts ...LogMiddlewareOption) *logMiddleware {
	mid := &logMiddleware{
		Logger:              &log.GoLogger{},
		ObfuscationDisabled: logObfuscationDisabled,
	}

	for _, opt := range opts {
		opt(mid)
	}

	return mid
}

func requestScopedLogger(base log.Logger, requestID string) log.Logger {
	if nilcheck.Interface(base) {
		return log.NewNop()
	}

	return base.
		With(log.String(cn.HeaderID, requestID)).
		With(log.String("message_prefix", requestID+cn.LoggerDefaultSeparator))
}

// WithHTTPLogging is a middleware to log access to http server.
// It logs access log according to Apache Standard Logs which uses Common Log Format (CLF)
// Ref: https://httpd.apache.org/docs/trunk/logs.html#common
func WithHTTPLogging(opts ...LogMiddlewareOption) fiber.Handler {
	mid := buildOpts(opts...)

	return func(c *fiber.Ctx) error {
		if c.Path() == "/health" {
			return c.Next()
		}

		if strings.Contains(c.Path(), "swagger") && c.Path() != "/swagger/index.html" {
			return c.Next()
		}

		setRequestHeaderID(c)

		info := NewRequestInfo(c, mid.ObfuscationDisabled)

		headerID := c.Get(cn.HeaderID)
		logger := requestScopedLogger(mid.Logger, headerID)

		ctx := commons.ContextWithLogger(c.UserContext(), logger)
		c.SetUserContext(ctx)

		err := c.Next()

		rw := ResponseMetricsWrapper{
			Context:    c,
			StatusCode: c.Response().StatusCode(),
			Size:       len(c.Response().Body()),
		}

		info.FinishRequestInfo(&rw)
		logger.Log(c.UserContext(), log.LevelInfo, info.CLFString())

		return err
	}
}

// WithGrpcLogging is a gRPC unary interceptor to log access to gRPC server.
func WithGrpcLogging(opts ...LogMiddlewareOption) grpc.UnaryServerInterceptor {
	mid := buildOpts(opts...)

	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		ctx = normalizeGRPCContext(ctx)
		requestID := resolveGRPCRequestID(ctx, req)

		if rid, ok := getValidBodyRequestID(req); ok {
			if prev := getMetadataID(ctx); prev != "" && prev != rid {
				mid.Logger.Log(ctx, log.LevelDebug, "Overriding correlation id from metadata with body request_id",
					log.String("metadata_id", prev),
					log.String("body_request_id", rid),
				)
			}
		}

		ctx = commons.ContextWithHeaderID(ctx, requestID)
		ctx = commons.ContextWithSpanAttributes(ctx, attribute.String("app.request.request_id", requestID))

		_, _, reqId, _ := commons.NewTrackingFromContext(ctx)

		logger := requestScopedLogger(mid.Logger, reqId)

		ctx = commons.ContextWithLogger(ctx, logger)

		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		methodName := "unknown"
		if info != nil {
			methodName = info.FullMethod
		}

		fields := []log.Field{
			log.String("method", methodName),
			log.String("duration", duration.String()),
		}
		if err != nil {
			fields = append(fields, log.Err(err))
		}

		logger.Log(ctx, log.LevelInfo, "gRPC request finished", fields...)

		return resp, err
	}
}

func normalizeGRPCContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}

func getContextHeaderID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	values, ok := ctx.Value(commons.CustomContextKey).(*commons.CustomContextKeyValue)
	if !ok || values == nil {
		return ""
	}

	return normalizeRequestID(values.HeaderID)
}

func normalizeRequestID(raw string) string {
	return strings.TrimSpace(sanitizeLogValue(raw))
}

func resolveGRPCRequestID(ctx context.Context, req any) string {
	if rid, ok := getValidBodyRequestID(req); ok {
		return rid
	}

	if existing := getContextHeaderID(ctx); existing != "" {
		return existing
	}

	if rid := getMetadataID(ctx); rid != "" {
		return rid
	}

	return uuid.New().String()
}

// setRequestHeaderID ensures the Fiber request carries a unique correlation ID header.
// The effective ID is always echoed back on the response so that callers can
// correlate their request regardless of whether the ID was client-supplied or
// server-generated.
func setRequestHeaderID(c *fiber.Ctx) {
	headerID := normalizeRequestID(c.Get(cn.HeaderID))

	if commons.IsNilOrEmpty(&headerID) {
		headerID = uuid.New().String()
	}

	c.Request().Header.Set(cn.HeaderID, headerID)
	c.Set(cn.HeaderID, headerID)
	c.Response().Header.Set(cn.HeaderID, headerID)

	ctx := commons.ContextWithHeaderID(c.UserContext(), headerID)
	c.SetUserContext(ctx)
}

// getValidBodyRequestID extracts and validates the request_id from the gRPC request body.
// Returns (id, true) when present and valid UUID; otherwise ("", false).
func getValidBodyRequestID(req any) (string, bool) {
	if r, ok := req.(interface{ GetRequestId() string }); ok {
		if nilcheck.Interface(r) {
			return "", false
		}

		if rid := strings.TrimSpace(r.GetRequestId()); rid != "" && commons.IsUUID(rid) {
			return rid, true
		}
	}

	return "", false
}

// getMetadataID extracts a correlation id from incoming gRPC metadata if present.
func getMetadataID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	if md, ok := metadata.FromIncomingContext(ctx); ok && md != nil {
		headerID := md.Get(cn.MetadataID)
		if len(headerID) > 0 && !commons.IsNilOrEmpty(&headerID[0]) {
			return normalizeRequestID(headerID[0])
		}
	}

	return ""
}
