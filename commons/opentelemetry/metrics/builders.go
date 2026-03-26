package metrics

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func cloneAttributes(attrs []attribute.KeyValue, extra int) []attribute.KeyValue {
	cloned := make([]attribute.KeyValue, 0, len(attrs)+extra)
	cloned = append(cloned, attrs...)

	return cloned
}

func appendLabelAttributes(attrs []attribute.KeyValue, labels map[string]string) []attribute.KeyValue {
	merged := cloneAttributes(attrs, len(labels))
	for key, value := range labels {
		merged = append(merged, attribute.String(key, value))
	}

	return merged
}

func appendAttributes(attrs []attribute.KeyValue, extra ...attribute.KeyValue) []attribute.KeyValue {
	merged := cloneAttributes(attrs, len(extra))
	merged = append(merged, extra...)

	return merged
}

func measurementOptions(attrs []attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributes(attrs...)
}

var (
	// ErrNilCounter is returned when a counter builder has no instrument.
	ErrNilCounter = errors.New("counter instrument is nil")
	// ErrNilGauge is returned when a gauge builder has no instrument.
	ErrNilGauge = errors.New("gauge instrument is nil")
	// ErrNilHistogram is returned when a histogram builder has no instrument.
	ErrNilHistogram = errors.New("histogram instrument is nil")
	// ErrNilCounterBuilder is returned when a CounterBuilder method is called on a nil receiver.
	ErrNilCounterBuilder = errors.New("counter builder is nil")
	// ErrNilGaugeBuilder is returned when a GaugeBuilder method is called on a nil receiver.
	ErrNilGaugeBuilder = errors.New("gauge builder is nil")
	// ErrNilHistogramBuilder is returned when a HistogramBuilder method is called on a nil receiver.
	ErrNilHistogramBuilder = errors.New("histogram builder is nil")
)

// CounterBuilder provides a fluent API for recording counter metrics with optional labels
type CounterBuilder struct {
	counter metric.Int64Counter
	attrs   []attribute.KeyValue
}

// WithLabels adds labels/attributes to the counter metric.
// Returns a nil-safe builder if the receiver is nil.
func (c *CounterBuilder) WithLabels(labels map[string]string) *CounterBuilder {
	if c == nil {
		return nil
	}

	builder := &CounterBuilder{
		counter: c.counter,
		attrs:   appendLabelAttributes(c.attrs, labels),
	}

	return builder
}

// WithAttributes adds OpenTelemetry attributes to the counter metric.
// Returns a nil-safe builder if the receiver is nil.
func (c *CounterBuilder) WithAttributes(attrs ...attribute.KeyValue) *CounterBuilder {
	if c == nil {
		return nil
	}

	builder := &CounterBuilder{
		counter: c.counter,
		attrs:   appendAttributes(c.attrs, attrs...),
	}

	return builder
}

// Add records a counter increment.
// Returns an error if the value is negative (counters are monotonically increasing).
func (c *CounterBuilder) Add(ctx context.Context, value int64) error {
	if c == nil {
		return ErrNilCounterBuilder
	}

	if c.counter == nil {
		return ErrNilCounter
	}

	if value < 0 {
		return ErrNegativeCounterValue
	}

	c.counter.Add(ctx, value, measurementOptions(c.attrs))

	return nil
}

// AddOne increments the counter by one.
func (c *CounterBuilder) AddOne(ctx context.Context) error {
	if c == nil {
		return ErrNilCounterBuilder
	}

	return c.Add(ctx, 1)
}

// GaugeBuilder provides a fluent API for recording gauge metrics with optional labels
type GaugeBuilder struct {
	gauge metric.Int64Gauge
	attrs []attribute.KeyValue
}

// WithLabels adds labels/attributes to the gauge metric.
// Returns a nil-safe builder if the receiver is nil.
func (g *GaugeBuilder) WithLabels(labels map[string]string) *GaugeBuilder {
	if g == nil {
		return nil
	}

	builder := &GaugeBuilder{
		gauge: g.gauge,
		attrs: appendLabelAttributes(g.attrs, labels),
	}

	return builder
}

// WithAttributes adds OpenTelemetry attributes to the gauge metric.
// Returns a nil-safe builder if the receiver is nil.
func (g *GaugeBuilder) WithAttributes(attrs ...attribute.KeyValue) *GaugeBuilder {
	if g == nil {
		return nil
	}

	builder := &GaugeBuilder{
		gauge: g.gauge,
		attrs: appendAttributes(g.attrs, attrs...),
	}

	return builder
}

// Set sets the current value of a gauge (recommended for application code).
//
// This is the primary implementation for recording gauge values and is
// idiomatic for instantaneous state (e.g., queue length, in-flight operations).
// It uses only the builder attributes to avoid high-cardinality labels.
func (g *GaugeBuilder) Set(ctx context.Context, value int64) error {
	if g == nil {
		return ErrNilGaugeBuilder
	}

	if g.gauge == nil {
		return ErrNilGauge
	}

	g.gauge.Record(ctx, value, measurementOptions(g.attrs))

	return nil
}

// HistogramBuilder provides a fluent API for recording histogram metrics with optional labels
type HistogramBuilder struct {
	histogram metric.Int64Histogram
	attrs     []attribute.KeyValue
}

// WithLabels adds labels/attributes to the histogram metric.
// Returns a nil-safe builder if the receiver is nil.
func (h *HistogramBuilder) WithLabels(labels map[string]string) *HistogramBuilder {
	if h == nil {
		return nil
	}

	builder := &HistogramBuilder{
		histogram: h.histogram,
		attrs:     appendLabelAttributes(h.attrs, labels),
	}

	return builder
}

// WithAttributes adds OpenTelemetry attributes to the histogram metric.
// Returns a nil-safe builder if the receiver is nil.
func (h *HistogramBuilder) WithAttributes(attrs ...attribute.KeyValue) *HistogramBuilder {
	if h == nil {
		return nil
	}

	builder := &HistogramBuilder{
		histogram: h.histogram,
		attrs:     appendAttributes(h.attrs, attrs...),
	}

	return builder
}

// Record records a histogram value
func (h *HistogramBuilder) Record(ctx context.Context, value int64) error {
	if h == nil {
		return ErrNilHistogramBuilder
	}

	if h.histogram == nil {
		return ErrNilHistogram
	}

	h.histogram.Record(ctx, value, measurementOptions(h.attrs))

	return nil
}
