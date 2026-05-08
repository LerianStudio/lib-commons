// Package analyzers contains go/analysis analyzers for telemetry primitives.
package analyzers

import "github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"

// CounterFindings contains counter primitives detected in one package.
type CounterFindings struct {
	Counters []schema.CounterPrimitive
}

// HistogramFindings contains histogram primitives detected in one package.
type HistogramFindings struct {
	Histograms []schema.HistogramPrimitive
}

// GaugeFindings contains gauge primitives detected in one package.
type GaugeFindings struct {
	Gauges []schema.GaugePrimitive
}

// SpanFindings contains span primitives detected in one package.
type SpanFindings struct {
	Spans []schema.SpanPrimitive
}

// LogFieldFindings contains structured log fields detected in one package.
type LogFieldFindings struct {
	Fields []schema.LogFieldPrimitive
}

// FrameworkFindings contains framework auto-instrumentation detected in one package.
type FrameworkFindings struct {
	Frameworks []schema.FrameworkInstrumentation
}

// CrossCutFindings contains cross-cutting telemetry consistency issues.
type CrossCutFindings struct {
	Issues []schema.CrossCutFinding
}
