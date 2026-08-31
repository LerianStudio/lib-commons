// Package obs defines the minimal observability contracts that lib-commons
// exposes on its public API.
//
// The interfaces here are deliberately expressed with stdlib types only.
// lib-commons MUST NOT import github.com/LerianStudio/lib-observability from
// this package: an interface that names a type defined by a versioned module
// stays nominally bound to that module, which is exactly the coupling this
// package exists to remove. Because these contracts mention nothing but
// stdlib types, an adapter written against ANY major of lib-observability
// satisfies them, and consumers are free to move between lib-observability
// majors without waiting for lib-commons.
//
// Since lib-observability v4 no adapter is needed in either direction: its
// loggers satisfy Logger and its *metrics.MetricsFactory satisfies
// MetricsRecorder as they are, and every lib-observability entry point that
// takes a logger accepts a Logger declared here. See MIGRATION-v7.md.
package obs

import "context"

// Log severity levels.
//
// The numeric scale is identical to lib-observability's log.Level: lower
// values are MORE severe. A logger configured at LevelInfo (2) emits Error,
// Warn and Info and suppresses Debug.
const (
	// LevelError reports failures. Most severe.
	LevelError = 0
	// LevelWarn reports recoverable anomalies.
	LevelWarn = 1
	// LevelInfo reports normal operational events.
	LevelInfo = 2
	// LevelDebug reports diagnostic detail. Least severe.
	LevelDebug = 3
)

// Logger is the logging contract required by lib-commons.
//
// Structured attributes are passed as alternating key/value pairs in kv.
// A key that is not a non-empty string is replaced by a positional
// placeholder ("arg_N"); a trailing key with no value is recorded with a nil
// value. Implementations must be safe for concurrent use and must never
// panic on malformed kv.
type Logger interface {
	// Log emits msg at the given level with the kv attributes attached.
	Log(ctx context.Context, level int, msg string, kv ...any)
	// Enabled reports whether events at level would be emitted.
	Enabled(level int) bool
	// Sync flushes any buffered log entries.
	Sync(ctx context.Context) error
}

// MetricsRecorder is the metrics contract required by lib-commons.
//
// The instrument builder chain of a typical metrics SDK is deliberately
// flattened into a single call per emission so that no builder type from a
// versioned module appears in this contract. Instrument caching, if any,
// belongs to the adapter.
type MetricsRecorder interface {
	// AddCounter adds delta to the named counter.
	AddCounter(ctx context.Context, name, description, unit string, attrs map[string]string, delta int64) error
	// SetGauge sets the named gauge to value.
	SetGauge(ctx context.Context, name, description, unit string, attrs map[string]string, value int64) error
	// RecordHistogram records value in the named histogram.
	//
	// Record durations in MILLISECONDS and set unit accordingly. value is
	// float64 for call-site convenience, but adapters are expected to back
	// this with an integer histogram instrument and round: a duration
	// expressed in seconds (0.004) is recorded as 0. An adapter must reject a
	// value it cannot represent rather than cast it blindly.
	RecordHistogram(ctx context.Context, name, description, unit string, attrs map[string]string, value float64, buckets []float64) error
}

// TelemetryShutdowner is the telemetry lifecycle contract required by
// lib-commons.
//
// Because the method mentions no nominal type at all, *tracing.Telemetry from
// any lib-observability major satisfies this interface directly, with no
// adapter.
type TelemetryShutdowner interface {
	// ShutdownTelemetry flushes and stops telemetry exporters.
	ShutdownTelemetry()
}
