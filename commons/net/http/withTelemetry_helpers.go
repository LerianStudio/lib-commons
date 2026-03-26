package http

import (
	"context"
	"net/url"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons"
	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/security"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"
)

func resolveEffectiveTelemetry(tm *TelemetryMiddleware, tl *opentelemetry.Telemetry) *opentelemetry.Telemetry {
	if tl != nil {
		return tl
	}

	if tm != nil {
		return tm.Telemetry
	}

	return nil
}

func telemetryTracer(tl *opentelemetry.Telemetry) (trace.Tracer, bool) {
	if tl == nil || tl.TracerProvider == nil {
		return nil, false
	}

	return tl.TracerProvider.Tracer(tl.LibraryName), true
}

func withRequestIDSpanAttribute(ctx context.Context, requestID string) context.Context {
	return commons.ContextWithSpanAttributes(ctx,
		attribute.String("app.request.request_id", requestID),
	)
}

func endSpanFromContext(ctx context.Context) {
	if ctx == nil {
		return
	}

	trace.SpanFromContext(ctx).End()
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
		return sanitizeMalformedURL(rawURL)
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

func sanitizeMalformedURL(rawURL string) string {
	sanitized := sanitizeLogValue(rawURL)
	if before, _, ok := strings.Cut(sanitized, "?"); ok {
		return before + "?redacted"
	}

	return sanitized
}

// getGRPCUserAgent extracts the User-Agent from incoming gRPC metadata.
// Returns empty string if the metadata is not present or doesn't contain user-agent.
func getGRPCUserAgent(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

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
