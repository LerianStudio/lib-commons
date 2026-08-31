// Package obsbridge is the single, demarcated door between lib-commons and
// github.com/LerianStudio/lib-observability.
//
// Since lib-observability v4 the door is thin: every exported function of
// that library declares its logger and recorder parameters with universal
// types, and its context accessors return values whose method sets already
// satisfy the commons/obs contracts. No conversion code is left here — the
// only thing this package still buys is confinement. It is the one package of
// lib-commons that imports lib-observability to reach the ambient telemetry
// stored in a context.Context, so the next major of that library costs
// lib-commons a single import line instead of one per call site.
//
// Nothing outside this package is required to go through it. A consumer holds
// a lib-observability logger or metrics factory and passes it straight into
// any lib-commons entry point that takes an obs.Logger or an
// obs.MetricsRecorder; so does a logger declared in a package that has never
// imported lib-observability at all.
package obsbridge

import (
	"context"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	libobs "github.com/LerianStudio/lib-observability/v4"
	"go.opentelemetry.io/otel/trace"
)

// LoggerFromContext returns the logger carried by ctx as an obs.Logger.
//
// The result is never nil: lib-observability resolves a missing logger to its
// no-op implementation.
func LoggerFromContext(ctx context.Context) obs.Logger {
	return libobs.NewLoggerFromContext(ctx)
}

// MetricsFromContext returns the metrics factory carried by ctx as an
// obs.MetricsRecorder.
//
// The result is always safe to call: the factory guards a nil receiver on
// every instrument method.
func MetricsFromContext(ctx context.Context) obs.MetricsRecorder {
	_, _, _, factory := libobs.NewTrackingFromContext(ctx) //nolint:dogsled // only the metrics factory is needed here

	return factory
}

// TrackingFromContext mirrors lib-observability's NewTrackingFromContext with
// the logger and the metrics factory typed as the commons/obs contracts. The
// tracer is returned unchanged: trace.Tracer belongs to
// go.opentelemetry.io/otel, which is not the module in dispute.
func TrackingFromContext(ctx context.Context) (obs.Logger, trace.Tracer, string, obs.MetricsRecorder) {
	logger, tracer, headerID, factory := libobs.NewTrackingFromContext(ctx)

	return logger, tracer, headerID, factory
}
