# Changelog

All notable changes to lib-commons will be documented in this file.

## Unreleased

- Added `server.WithStdlibHTTPServer(*http.Server)` for caller-owned stdlib HTTP servers while preserving the existing Fiber HTTP path; Fiber and stdlib HTTP are mutually exclusive, and stdlib servers receive a safe default `ReadHeaderTimeout` when unset.
- Added `http.ErrorEnvelope`, `http.ErrorPayload`, and `http.RespondErrorEnvelope` as a richer sibling error contract without changing the existing flat `RespondError(c, status, title, message)` API.
- Added `ratelimit.WithExceededHandler` for caller-controlled 429 response bodies and `RedisStorage.Increment` for direct use of the package's atomic fixed-window Redis primitive.
- Added `metrics.CounterBuilder.WithAttributeSet(attribute.Set)` for allocation-conscious hot paths with prebuilt OpenTelemetry attribute sets.
- Added `circuitbreaker.NewPassthroughManager` and additive `TenantAwareManager` methods for tenant-isolated breakers while preserving the existing `Manager` interface; breaker telemetry uses stable `tenant_hash` labels instead of raw tenant IDs.
- Added `webhook.WithAllowPrivateNetwork` as a security-gated, IP-literal-only escape hatch for local/E2E webhook targets; hostnames resolving to private addresses remain blocked.
- Fixed OpenTelemetry deployment environment handling so `TelemetryConfig.DeploymentEnv` is trimmed/lowercased before provider/resource construction and emitted as `deployment.environment.name` with the normalized value.
