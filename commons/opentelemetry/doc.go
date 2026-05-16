// Package opentelemetry provides tracing, metrics, propagation, and redaction helpers.
//
// NewTelemetry builds providers/exporters and can run in disabled mode for local/dev
// environments while preserving API compatibility.
//
// TelemetryConfig.DeploymentEnv is normalized with strings.TrimSpace +
// strings.ToLower before providers are built, and the normalized value is
// emitted on the OpenTelemetry resource with the semantic-convention
// deployment.environment.name key. This keeps OTLP resource attributes and
// Prometheus-side mirrors joinable on the same normalized environment label.
//
// The package also includes carrier utilities for HTTP, gRPC, and queue headers, plus
// redaction-aware attribute extraction for safe span enrichment.
//
// Deprecated: use github.com/LerianStudio/lib-observability/tracing.
package opentelemetry
