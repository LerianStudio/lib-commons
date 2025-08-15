package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// CounterBuilder provides a fluent API for recording counter metrics with optional labels
type CounterBuilder struct {
	factory *MetricsFactory
	counter metric.Int64Counter
	name    string
	attrs   []attribute.KeyValue
}

// WithLabels adds labels/attributes to the counter metric
func (c *CounterBuilder) WithLabels(labels map[string]string) *CounterBuilder {
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

// WithAttributes adds OpenTelemetry attributes to the counter metric
func (c *CounterBuilder) WithAttributes(attrs ...attribute.KeyValue) *CounterBuilder {
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

// Add records a counter increment
func (c *CounterBuilder) Add(ctx context.Context, value int64) {
	if c.counter == nil {
		return
	}

	// Use only the builder attributes (no trace correlation to avoid high cardinality)
	c.counter.Add(ctx, value, metric.WithAttributes(c.attrs...))
}

func (c *CounterBuilder) AddOne(ctx context.Context) {
	if c.counter == nil {
		return
	}

	c.Add(ctx, 1)
}

// GaugeBuilder provides a fluent API for recording gauge metrics with optional labels
type GaugeBuilder struct {
	factory *MetricsFactory
	gauge   metric.Int64Gauge
	name    string
	attrs   []attribute.KeyValue
}

// WithLabels adds labels/attributes to the gauge metric
func (g *GaugeBuilder) WithLabels(labels map[string]string) *GaugeBuilder {
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

// WithAttributes adds OpenTelemetry attributes to the gauge metric
func (g *GaugeBuilder) WithAttributes(attrs ...attribute.KeyValue) *GaugeBuilder {
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

// Record sets the gauge to the provided value.
//
// Deprecated: use Set for application code. This method is kept for
// parity with OpenTelemetry's instrument API (metric.Int64Gauge.Record)
// to ease portability from raw OTEL usage. It delegates to Set.
func (g *GaugeBuilder) Record(ctx context.Context, value int64) {
	g.Set(ctx, value)
}

// Set sets the current value of a gauge (recommended for application code).
//
// This is the primary implementation for recording gauge values and is
// idiomatic for instantaneous state (e.g., queue length, in-flight operations).
// It uses only the builder attributes to avoid high-cardinality labels.
func (g *GaugeBuilder) Set(ctx context.Context, value int64) {
	if g.gauge == nil {
		return
	}

	// Use only the builder attributes (no trace correlation to avoid high cardinality)
	g.gauge.Record(ctx, value, metric.WithAttributes(g.attrs...))
}

// HistogramBuilder provides a fluent API for recording histogram metrics with optional labels
type HistogramBuilder struct {
	factory   *MetricsFactory
	histogram metric.Int64Histogram
	name      string
	attrs     []attribute.KeyValue
}

// WithLabels adds labels/attributes to the histogram metric
func (h *HistogramBuilder) WithLabels(labels map[string]string) *HistogramBuilder {
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

// WithAttributes adds OpenTelemetry attributes to the histogram metric
func (h *HistogramBuilder) WithAttributes(attrs ...attribute.KeyValue) *HistogramBuilder {
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
func (h *HistogramBuilder) Record(ctx context.Context, value int64) {
	if h.histogram == nil {
		return
	}

	// Use only the builder attributes (no trace correlation to avoid high cardinality)
	h.histogram.Record(ctx, value, metric.WithAttributes(h.attrs...))
}
