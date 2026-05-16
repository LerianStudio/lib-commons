// Package assert provides production-safe assertions with telemetry integration.
// This package delegates to github.com/LerianStudio/lib-observability/assert.
package assert

import (
	"context"

	libobsassert "github.com/LerianStudio/lib-observability/assert"
	libobsmetrics "github.com/LerianStudio/lib-observability/metrics"
)

// Logger defines the minimal logging interface required by assertions.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.Logger instead.
type Logger = libobsassert.Logger

// Asserter evaluates invariants and emits telemetry on failure.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.Asserter instead.
type Asserter = libobsassert.Asserter

// ErrAssertionFailed is the sentinel error for failed assertions.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ErrAssertionFailed instead.
var ErrAssertionFailed = libobsassert.ErrAssertionFailed

// AssertionError represents a failed assertion with rich context.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.AssertionError instead.
type AssertionError = libobsassert.AssertionError

// New creates an Asserter with context, logging, and labels.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.New instead.
func New(ctx context.Context, logger Logger, component, operation string) *Asserter {
	return libobsassert.New(ctx, logger, component, operation)
}

// AssertionSpanEventName is the event name used when recording assertion failures on spans.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.AssertionSpanEventName instead.
const AssertionSpanEventName = libobsassert.AssertionSpanEventName

// AssertionMetrics provides assertion-related metrics using OpenTelemetry.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.AssertionMetrics instead.
type AssertionMetrics = libobsassert.AssertionMetrics

// InitAssertionMetrics initializes assertion metrics with the provided MetricsFactory.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.InitAssertionMetrics instead.
func InitAssertionMetrics(factory *libobsmetrics.MetricsFactory) {
	libobsassert.InitAssertionMetrics(factory)
}

// GetAssertionMetrics returns the singleton AssertionMetrics instance.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.GetAssertionMetrics instead.
func GetAssertionMetrics() *AssertionMetrics {
	return libobsassert.GetAssertionMetrics()
}

// ResetAssertionMetrics clears the assertion metrics singleton (useful for tests).
//
// Deprecated: Use github.com/LerianStudio/lib-observability/assert.ResetAssertionMetrics instead.
func ResetAssertionMetrics() {
	libobsassert.ResetAssertionMetrics()
}
