package obsbridge

import (
	"context"

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

	a.base.Log(ctx, toLevel(level), msg, toFields(kv...)...)
}

func (a loggerAdapter) Enabled(level int) bool {
	if a.base == nil {
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

// toLevel maps the obs level scale onto liblog.Level. Both scales are
// identical (Error=0 .. Debug=3); out-of-range values clamp to LevelError,
// the most severe, so a mistake is never silently dropped.
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

func (a metricsAdapter) RecordHistogram(ctx context.Context, name, description, unit string, attrs map[string]string, value float64, buckets []float64) error {
	builder, err := a.base.Histogram(libmetrics.Metric{Name: name, Description: description, Unit: unit, Buckets: buckets})
	if err != nil {
		return err
	}

	return builder.WithAttributes(toAttributes(attrs)...).Record(ctx, int64(value))
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
