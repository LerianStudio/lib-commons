// Package opentelemetry provides tracing, metrics, propagation, and redaction helpers.
//
// NewTelemetry builds providers/exporters and can run in disabled mode for local/dev
// environments while preserving API compatibility.
//
// The package also includes carrier utilities for HTTP, gRPC, and queue headers, plus
// redaction-aware attribute extraction for safe span enrichment.
package opentelemetry
