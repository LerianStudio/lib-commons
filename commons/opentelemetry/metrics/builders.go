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

// CounterBuilder provides a fluent API for recording counter metrics with optional labels.
//
// Two attribute paths are supported:
//
//   - The slice path (WithLabels / WithAttributes): convenient, immutable
//     fluent chaining. On every Add call the OTel SDK builds a fresh
//     attribute.Set from the slice, allocating in the process.
//
//   - The pre-built Set path (WithAttributeSet): caller pre-builds a single
//     attribute.Set at construction time and reuses it allocation-free on
//     the hot path. Use this when the attribute payload is fixed at builder
//     construction time and Add is called at high frequency (e.g. > 1k QPS).
//
// The two paths are mutually exclusive: when WithAttributeSet is in effect
// the slice (c.attrs) is ignored on the hot path. See WithAttributeSet for
// precedence details.
type CounterBuilder struct {
	factory *MetricsFactory
	counter metric.Int64Counter
	name    string
	attrs   []attribute.KeyValue
	// attrSet, when non-nil, is preferred over attrs on the Add hot path
	// and causes Add to use metric.WithAttributeSet (allocation-free) rather
	// than metric.WithAttributes (which allocates a fresh slice and Set per call).
	attrSet *attribute.Set
}

// WithLabels adds labels/attributes to the counter metric using the slice path.
// Returns a nil-safe builder if the receiver is nil.
//
// Note: WithLabels clears any pre-built attribute.Set previously installed by
// WithAttributeSet on the parent. The intent of WithLabels is to extend the
// attribute payload; carrying the immutable Set through would silently ignore
// the new labels. Build the full payload up front and call WithAttributeSet
// last when you want the allocation-free path.
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

// WithAttributes adds OpenTelemetry attributes to the counter metric using
// the slice path. Returns a nil-safe builder if the receiver is nil.
//
// Note: WithAttributes clears any pre-built attribute.Set previously installed
// by WithAttributeSet on the parent. See WithLabels for the rationale.
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

// WithAttributeSet installs a pre-built attribute.Set on the counter builder.
// On the Add hot path the builder will use metric.WithAttributeSet rather
// than metric.WithAttributes, avoiding per-call slice and Set allocations.
//
// Returns a nil-safe builder if the receiver is nil.
//
// Precedence: when a builder has both a pre-built Set (from WithAttributeSet)
// and a slice (from WithLabels / WithAttributes), the pre-built Set wins on
// the hot path and the slice is ignored. The slice is preserved on the new
// builder for callers that branch further with WithLabels / WithAttributes
// (those branches will return to the slice path).
//
// Typical usage:
//
//	hitAttrs := attribute.NewSet(attribute.String("result", "hit"))
//	counter := factory.Counter(MyMetric).
//	    WithAttributeSet(hitAttrs)
//
//	// Hot path:
//	counter.Add(ctx, 1) // no per-call slice/Set allocation
func (c *CounterBuilder) WithAttributeSet(set attribute.Set) *CounterBuilder {
	if c == nil {
		return nil
	}

	setCopy := set

	builder := &CounterBuilder{
		factory: c.factory,
		counter: c.counter,
		name:    c.name,
		attrs:   c.attrs, // preserved but ignored on the hot path while attrSet != nil
		attrSet: &setCopy,
	}

	return builder
}

// Add records a counter increment.
// Returns an error if the value is negative (counters are monotonically increasing).
//
// When the builder has a pre-built attribute.Set installed via WithAttributeSet,
// Add uses metric.WithAttributeSet and incurs zero per-call attribute allocations.
// Otherwise it falls back to metric.WithAttributes over the slice path.
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

	if c.attrSet != nil {
		c.counter.Add(ctx, value, metric.WithAttributeSet(*c.attrSet))
		return nil
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
