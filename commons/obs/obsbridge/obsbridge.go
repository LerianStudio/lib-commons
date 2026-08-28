// Package obsbridge is the single, demarcated door between lib-commons and
// github.com/LerianStudio/lib-observability.
//
// Every other package of lib-commons speaks the stdlib-only contracts of
// commons/obs. This package - and only this package - names lib-observability
// types, converting a lib-observability logger, metrics factory or context
// into the corresponding commons/obs contract.
//
// It is pinned to lib-observability/v2. If you consume a different major,
// do NOT use this package: write (or use) the adapter published by that major
// of lib-observability itself and pass the result to lib-commons. Nothing in
// lib-commons outside this package requires you to go through here.
package obsbridge

import (
	"context"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	libobs "github.com/LerianStudio/lib-observability/v2"
	liblog "github.com/LerianStudio/lib-observability/v2/log"
	libmetrics "github.com/LerianStudio/lib-observability/v2/metrics"
	"go.opentelemetry.io/otel/trace"
)

// Logger adapts a lib-observability logger to obs.Logger.
//
// A nil logger yields obs.Nop().
func Logger(logger liblog.Logger) obs.Logger {
	if logger == nil {
		return obs.Nop()
	}

	return loggerAdapter{base: logger}
}

// Metrics adapts a lib-observability metrics factory to obs.MetricsRecorder.
//
// A nil factory yields a recorder backed by libmetrics.NewNopFactory, so the
// result is always safe to call.
func Metrics(factory *libmetrics.MetricsFactory) obs.MetricsRecorder {
	if factory == nil {
		factory = libmetrics.NewNopFactory()
	}

	return metricsAdapter{base: factory}
}

// LoggerFromContext returns the context logger as an obs.Logger.
func LoggerFromContext(ctx context.Context) obs.Logger {
	return Logger(libobs.NewLoggerFromContext(ctx))
}

// MetricsFromContext returns the context metrics factory as an
// obs.MetricsRecorder.
func MetricsFromContext(ctx context.Context) obs.MetricsRecorder {
	_, _, _, factory := libobs.NewTrackingFromContext(ctx) //nolint:dogsled // only the metrics factory is needed here

	return Metrics(factory)
}

// TrackingFromContext mirrors lib-observability's NewTrackingFromContext with
// the logger and the metrics factory already adapted to the commons/obs
// contracts. The tracer is returned unchanged: trace.Tracer belongs to
// go.opentelemetry.io/otel, which is not the module in dispute.
func TrackingFromContext(ctx context.Context) (obs.Logger, trace.Tracer, string, obs.MetricsRecorder) {
	logger, tracer, headerID, factory := libobs.NewTrackingFromContext(ctx)

	return Logger(logger), tracer, headerID, Metrics(factory)
}
