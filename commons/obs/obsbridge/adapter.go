package obsbridge

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	liblog "github.com/LerianStudio/lib-observability/v2/log"
	libmetrics "github.com/LerianStudio/lib-observability/v2/metrics"
	"go.opentelemetry.io/otel/attribute"
)

// loggerAdapter converts the stdlib-only obs.Logger calls into the typed
// lib-observability log API.
type loggerAdapter struct {
	base liblog.Logger
}

func (a loggerAdapter) Log(ctx context.Context, level int, msg string, kv ...any) {
	if a.base == nil {
		return
	}

	if ctx == nil {
		ctx = context.Background()
	}

	// An out-of-range level is a caller bug, but dropping the event would
	// hide it. Emit at LevelError and preserve the original int under
	// badLevelKey so the mistake is visible in the log stream.
	if !validLevel(level) {
		kv = append([]any{badLevelKey, level}, kv...)
		a.base.Log(ctx, liblog.LevelError, msg, toFields(kv...)...)

		return
	}

	a.base.Log(ctx, toLevel(level), msg, toFields(kv...)...)
}

func (a loggerAdapter) Enabled(level int) bool {
	if a.base == nil || !validLevel(level) {
		return false
	}

	return a.base.Enabled(toLevel(level))
}

func (a loggerAdapter) Sync(ctx context.Context) error {
	if a.base == nil {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return a.base.Sync(ctx)
}

// badLevelKey marks an event whose level was outside the obs scale.
const badLevelKey = "!BADLEVEL"

// validLevel reports whether level is within the obs scale.
func validLevel(level int) bool {
	return level >= obs.LevelError && level <= obs.LevelDebug
}

// toLevel maps the obs level scale onto liblog.Level. Both scales are
// identical (Error=0 .. Debug=3); anything else clamps to LevelError, the
// most severe, so a mistake is never silently downgraded.
func toLevel(level int) liblog.Level {
	switch level {
	case obs.LevelWarn:
		return liblog.LevelWarn
	case obs.LevelInfo:
		return liblog.LevelInfo
	case obs.LevelDebug:
		return liblog.LevelDebug
	default:
		return liblog.LevelError
	}
}

// toFields converts alternating key/value pairs into typed log fields,
// applying the normalisation rules documented on obs.Logger.
func toFields(kv ...any) []liblog.Field {
	normalized := obs.NormalizeKV(kv...)
	if len(normalized) == 0 {
		return nil
	}

	fields := make([]liblog.Field, 0, len(normalized)/2)

	for i := 0; i < len(normalized); i += 2 {
		key, _ := normalized[i].(string)

		switch value := normalized[i+1].(type) {
		case string:
			fields = append(fields, liblog.String(key, value))
		case int:
			fields = append(fields, liblog.Int(key, value))
		case bool:
			fields = append(fields, liblog.Bool(key, value))
		case error:
			fields = append(fields, liblog.Any(key, value))
		default:
			fields = append(fields, liblog.Any(key, value))
		}
	}

	return fields
}

// metricsAdapter flattens the lib-observability builder chain into the
// single-call obs.MetricsRecorder contract.
type metricsAdapter struct {
	base *libmetrics.MetricsFactory
}

func (a metricsAdapter) AddCounter(ctx context.Context, name, description, unit string, attrs map[string]string, delta int64) error {
	builder, err := a.base.Counter(libmetrics.Metric{Name: name, Description: description, Unit: unit})
	if err != nil {
		return err
	}

	return builder.WithAttributes(toAttributes(attrs)...).Add(ctx, delta)
}

func (a metricsAdapter) SetGauge(ctx context.Context, name, description, unit string, attrs map[string]string, value int64) error {
	builder, err := a.base.Gauge(libmetrics.Metric{Name: name, Description: description, Unit: unit})
	if err != nil {
		return err
	}

	return builder.WithAttributes(toAttributes(attrs)...).Set(ctx, value)
}

// RecordHistogram records value in the named histogram.
//
// lib-observability has no float64 histogram instrument: MetricsFactory
// produces an OTEL Int64Histogram. value is therefore rounded to the nearest
// integer, and a value that cannot be represented as an int64 (NaN, +/-Inf,
// out of range) is rejected with ErrHistogramValueNotRepresentable rather
// than cast blindly. Record durations in milliseconds, not seconds: 0.004s
// rounds to 0.
func (a metricsAdapter) RecordHistogram(ctx context.Context, name, description, unit string, attrs map[string]string, value float64, buckets []float64) error {
	rounded, err := toInt64(value)
	if err != nil {
		return err
	}

	builder, err := a.base.Histogram(libmetrics.Metric{Name: name, Description: description, Unit: unit, Buckets: buckets})
	if err != nil {
		return err
	}

	return builder.WithAttributes(toAttributes(attrs)...).Record(ctx, rounded)
}

// ErrHistogramValueNotRepresentable is returned when a histogram value cannot
// be rounded into an int64.
var ErrHistogramValueNotRepresentable = errors.New("histogram value is not representable as int64")

// toInt64 rounds value to the nearest integer, rejecting NaN, infinities and
// magnitudes outside the int64 range.
func toInt64(value float64) (int64, error) {
	rounded := math.Round(value)
	if math.IsNaN(rounded) || rounded >= math.MaxInt64 || rounded <= math.MinInt64 {
		return 0, fmt.Errorf("%w: %v", ErrHistogramValueNotRepresentable, value)
	}

	return int64(rounded), nil
}

// toAttributes converts a plain string map into OpenTelemetry attributes.
func toAttributes(attrs map[string]string) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}

	out := make([]attribute.KeyValue, 0, len(attrs))
	for key, value := range attrs {
		out = append(out, attribute.String(key, value))
	}

	return out
}

// reverseAdapter converts an obs.Logger back into the typed
// lib-observability logger interface.
type reverseAdapter struct {
	base obs.Logger
}

func (a reverseAdapter) Log(ctx context.Context, level liblog.Level, msg string, fields ...liblog.Field) {
	if a.base == nil {
		return
	}

	if ctx == nil {
		ctx = context.Background()
	}

	a.base.Log(ctx, fromLevel(level), msg, fromFields(fields)...)
}

func (a reverseAdapter) With(fields ...liblog.Field) liblog.Logger {
	return reverseAdapter{base: obs.With(a.base, fromFields(fields)...)}
}

func (a reverseAdapter) WithGroup(name string) liblog.Logger {
	return reverseAdapter{base: obs.WithGroup(a.base, name)}
}

func (a reverseAdapter) Enabled(level liblog.Level) bool {
	if a.base == nil {
		return false
	}

	return a.base.Enabled(fromLevel(level))
}

func (a reverseAdapter) Sync(ctx context.Context) error {
	if a.base == nil {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return a.base.Sync(ctx)
}

// LibLogger adapts an obs.Logger back to the lib-observability logger
// interface, for the lib-observability entry points that still require the
// typed interface (runtime, assert). A nil logger yields liblog.NewNop().
func LibLogger(logger obs.Logger) liblog.Logger {
	if logger == nil {
		return liblog.NewNop()
	}

	if a, ok := logger.(loggerAdapter); ok {
		return a.base
	}

	return reverseAdapter{base: logger}
}

// fromLevel maps liblog.Level onto the obs level scale. The scales are
// numerically identical; anything unrecognised clamps to the most severe.
func fromLevel(level liblog.Level) int {
	switch level {
	case liblog.LevelWarn:
		return obs.LevelWarn
	case liblog.LevelInfo:
		return obs.LevelInfo
	case liblog.LevelDebug:
		return obs.LevelDebug
	default:
		return obs.LevelError
	}
}

// fromFields flattens typed log fields into alternating key/value pairs.
func fromFields(fields []liblog.Field) []any {
	if len(fields) == 0 {
		return nil
	}

	kv := make([]any, 0, len(fields)*2)
	for _, field := range fields {
		kv = append(kv, field.Key, field.Value)
	}

	return kv
}
