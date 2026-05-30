package commons

import (
	"os"
	"strings"
)

// Security override env var names. Each toggles a single safety check at
// runtime. When set to a truthy value ("true", "1", "yes", "on"; case-
// insensitive), the corresponding constructor or middleware skips its
// enforcement and emits a WARN log line for audit. There is no required
// "reason" parameter — audit is via log lines, not env-var content.
const (
	EnvAllowInsecureTLS       = "ALLOW_INSECURE_TLS"
	EnvRateLimitEnabled       = "RATE_LIMIT_ENABLED"
	EnvAllowRateLimitFailOpen = "ALLOW_RATELIMIT_FAIL_OPEN"
	EnvAllowCORSWildcard      = "ALLOW_CORS_WILDCARD"
	EnvAllowInsecureOTEL      = "ALLOW_INSECURE_OTEL"
	// EnvAllowWebhookPrivateNet permits webhook destinations on private
	// network ranges (RFC 1918, link-local, loopback) when set truthy.
	EnvAllowWebhookPrivateNet = "ALLOW_WEBHOOK_PRIVATE_NETWORK"
)

// AllowInsecureTLS returns true when ALLOW_INSECURE_TLS env var is truthy.
// Callers MUST skip TLS enforcement when this returns true and emit a WARN
// log line referencing the override.
func AllowInsecureTLS() bool { return getenvBool(EnvAllowInsecureTLS) }

// RateLimitEnabled defaults to false. Apps that need rate limiting MUST
// explicitly opt-in by setting RATE_LIMIT_ENABLED=true. The previous
// security-by-default posture was reversed because the prior framework
// silently enforced rate limiting on apps that never configured it, which
// caused operational surprises. Explicit opt-in is the new contract.
func RateLimitEnabled() bool { return getenvBoolDefault(EnvRateLimitEnabled, false) }

// AllowRateLimitFailOpen returns true when the rate limiter should permit
// requests during a backend (Redis) outage instead of fail-closed (429).
func AllowRateLimitFailOpen() bool { return getenvBool(EnvAllowRateLimitFailOpen) }

// AllowCORSWildcard returns true when CORS wildcard origin (*) is permitted.
func AllowCORSWildcard() bool { return getenvBool(EnvAllowCORSWildcard) }

// AllowInsecureOTEL returns true when the OTEL exporter is permitted to
// send unencrypted traffic.
func AllowInsecureOTEL() bool { return getenvBool(EnvAllowInsecureOTEL) }

// AllowWebhookPrivateNet returns true when webhook destinations on private
// network ranges (RFC 1918, link-local, loopback) are permitted.
func AllowWebhookPrivateNet() bool { return getenvBool(EnvAllowWebhookPrivateNet) }

// getenvBool reports whether the env var is set to a truthy boolean value.
// Accepted truthy: "true", "1", "yes", "on" (case-insensitive, trimmed).
// Any other value (including non-boolean strings such as "reason" audit
// strings from older deploy configs) is treated as false.
func getenvBool(name string) bool {
	return parseBool(os.Getenv(name), false)
}

// getenvBoolDefault returns def when the env var is unset; otherwise parses
// it as a boolean.
func getenvBoolDefault(name string, def bool) bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}

	return parseBool(raw, def)
}

func parseBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}
