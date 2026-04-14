package http

import (
	"context"
	"net/url"
	"strings"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/security"
	"github.com/gofiber/fiber/v2"
	"google.golang.org/grpc/metadata"
)

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
