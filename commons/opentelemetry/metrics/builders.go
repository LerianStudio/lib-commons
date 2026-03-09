package metrics

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

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
	factory *MetricsFactory
	counter metric.Int64Counter
	name    string
	attrs   []attribute.KeyValue
}

// WithLabels adds labels/attributes to the counter metric.
// Returns a nil-safe builder if the receiver is nil.
func (c *CounterBuilder) WithLabels(labels map[string]string) *CounterBuilder {
	if c == nil {
		return nil
	}

	builder := &CounterBuilder{
		factory: c.factory,
		counter: c.counter,
		name:    c.name,
		attrs:   make([]attribute.KeyValue, 0, len(c.attrs)+len(labels)),
	}

	builder.attrs = append(builder.attrs, c.attrs...)

	for key, value := range labels {
		builder.attrs = append(builder.attrs, attribute.String(key, value))
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
		factory: c.factory,
		counter: c.counter,
		name:    c.name,
		attrs:   make([]attribute.KeyValue, 0, len(c.attrs)+len(attrs)),
	}

	builder.attrs = append(builder.attrs, c.attrs...)

	builder.attrs = append(builder.attrs, attrs...)

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

	c.counter.Add(ctx, value, metric.WithAttributes(c.attrs...))

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
	factory *MetricsFactory
	gauge   metric.Int64Gauge
	name    string
	attrs   []attribute.KeyValue
}

// WithLabels adds labels/attributes to the gauge metric.
// Returns a nil-safe builder if the receiver is nil.
func (g *GaugeBuilder) WithLabels(labels map[string]string) *GaugeBuilder {
	if g == nil {
		return nil
	}

	builder := &GaugeBuilder{
		factory: g.factory,
		gauge:   g.gauge,
		name:    g.name,
		attrs:   make([]attribute.KeyValue, 0, len(g.attrs)+len(labels)),
	}

	builder.attrs = append(builder.attrs, g.attrs...)

	for key, value := range labels {
		builder.attrs = append(builder.attrs, attribute.String(key, value))
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
		factory: g.factory,
		gauge:   g.gauge,
		name:    g.name,
		attrs:   make([]attribute.KeyValue, 0, len(g.attrs)+len(attrs)),
	}

	builder.attrs = append(builder.attrs, g.attrs...)

	builder.attrs = append(builder.attrs, attrs...)

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

	g.gauge.Record(ctx, value, metric.WithAttributes(g.attrs...))

	return nil
}

// HistogramBuilder provides a fluent API for recording histogram metrics with optional labels
type HistogramBuilder struct {
	factory   *MetricsFactory
	histogram metric.Int64Histogram
	name      string
	attrs     []attribute.KeyValue
}

// WithLabels adds labels/attributes to the histogram metric.
// Returns a nil-safe builder if the receiver is nil.
func (h *HistogramBuilder) WithLabels(labels map[string]string) *HistogramBuilder {
	if h == nil {
		return nil
	}

	builder := &HistogramBuilder{
		factory:   h.factory,
		histogram: h.histogram,
		name:      h.name,
		attrs:     make([]attribute.KeyValue, 0, len(h.attrs)+len(labels)),
	}

	builder.attrs = append(builder.attrs, h.attrs...)

	for key, value := range labels {
		builder.attrs = append(builder.attrs, attribute.String(key, value))
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
		factory:   h.factory,
		histogram: h.histogram,
		name:      h.name,
		attrs:     make([]attribute.KeyValue, 0, len(h.attrs)+len(attrs)),
	}

	builder.attrs = append(builder.attrs, h.attrs...)

	builder.attrs = append(builder.attrs, attrs...)

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

	h.histogram.Record(ctx, value, metric.WithAttributes(h.attrs...))

	return nil
}
