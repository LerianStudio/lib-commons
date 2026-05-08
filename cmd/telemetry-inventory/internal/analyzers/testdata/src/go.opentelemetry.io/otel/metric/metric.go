package metric

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
)

type Option interface{}
type Int64CounterOption interface{}
type Int64HistogramOption interface{}
type Int64GaugeOption interface{}
type Float64CounterOption interface{}
type Float64HistogramOption interface{}
type Float64GaugeOption interface{}

type Meter interface {
	Int64Counter(string, ...Int64CounterOption) (Int64Counter, error)
	Int64Histogram(string, ...Int64HistogramOption) (Int64Histogram, error)
	Int64Gauge(string, ...Int64GaugeOption) (Int64Gauge, error)
	Float64Counter(string, ...Float64CounterOption) (Float64Counter, error)
	Float64Histogram(string, ...Float64HistogramOption) (Float64Histogram, error)
	Float64Gauge(string, ...Float64GaugeOption) (Float64Gauge, error)
}

type Int64Counter interface {
	Add(context.Context, int64, ...Option)
}
type Int64Histogram interface {
	Record(context.Context, int64, ...Option)
}
type Int64Gauge interface {
	Record(context.Context, int64, ...Option)
}
type Float64Counter interface {
	Add(context.Context, float64, ...Option)
}
type Float64Histogram interface {
	Record(context.Context, float64, ...Option)
}
type Float64Gauge interface {
	Record(context.Context, float64, ...Option)
}

func WithAttributes(...attribute.KeyValue) Option { return nil }
func WithDescription(string) Int64CounterOption   { return nil }
func WithUnit(string) Int64CounterOption          { return nil }
func WithExplicitBucketBoundaries(...float64) Int64HistogramOption {
	return nil
}
