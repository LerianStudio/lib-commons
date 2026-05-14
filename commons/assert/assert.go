// Package assert provides production-safe assertions with telemetry integration.
// This package delegates to github.com/LerianStudio/lib-observability/assert.
package assert

import (
	"context"

	libobsassert "github.com/LerianStudio/lib-observability/assert"
	libobsmetrics "github.com/LerianStudio/lib-observability/metrics"
)

// Logger defines the minimal logging interface required by assertions.
type Logger = libobsassert.Logger

// Asserter evaluates invariants and emits telemetry on failure.
type Asserter = libobsassert.Asserter

// ErrAssertionFailed is the sentinel error for failed assertions.
var ErrAssertionFailed = libobsassert.ErrAssertionFailed

// AssertionError represents a failed assertion with rich context.
type AssertionError = libobsassert.AssertionError

// New creates an Asserter with context, logging, and labels.
func New(ctx context.Context, logger Logger, component, operation string) *Asserter {
	return libobsassert.New(ctx, logger, component, operation)
}

// AssertionSpanEventName is the event name used when recording assertion failures on spans.
const AssertionSpanEventName = libobsassert.AssertionSpanEventName

// AssertionMetrics provides assertion-related metrics using OpenTelemetry.
type AssertionMetrics = libobsassert.AssertionMetrics

// InitAssertionMetrics initializes assertion metrics with the provided MetricsFactory.
func InitAssertionMetrics(factory *libobsmetrics.MetricsFactory) {
	libobsassert.InitAssertionMetrics(factory)
}

// GetAssertionMetrics returns the singleton AssertionMetrics instance.
func GetAssertionMetrics() *AssertionMetrics {
	return libobsassert.GetAssertionMetrics()
}

// ResetAssertionMetrics clears the assertion metrics singleton (useful for tests).
func ResetAssertionMetrics() {
	libobsassert.ResetAssertionMetrics()
}
