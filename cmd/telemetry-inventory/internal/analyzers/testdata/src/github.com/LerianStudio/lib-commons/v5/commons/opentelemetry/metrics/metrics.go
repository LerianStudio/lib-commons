package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
)

type MetricsFactory struct{}

type Metric struct {
	Name        string
	Description string
	Unit        string
	Buckets     []float64
}

type CounterBuilder struct{}
type HistogramBuilder struct{}
type GaugeBuilder struct{}

func (*MetricsFactory) Counter(Metric) (*CounterBuilder, error)     { return nil, nil }
func (*MetricsFactory) Histogram(Metric) (*HistogramBuilder, error) { return nil, nil }
func (*MetricsFactory) Gauge(Metric) (*GaugeBuilder, error)         { return nil, nil }

func (*MetricsFactory) RecordAccountCreated(context.Context, ...attribute.KeyValue) error { return nil }
func (*MetricsFactory) RecordTransactionProcessed(context.Context, ...attribute.KeyValue) error {
	return nil
}
func (*MetricsFactory) RecordSystemCPUUsage(context.Context, int64) error { return nil }
func (*MetricsFactory) RecordSystemMemUsage(context.Context, int64) error { return nil }
func (*MetricsFactory) RecordTransactionRouteCreated(context.Context, ...attribute.KeyValue) error {
	return nil
}
func (*MetricsFactory) RecordOperationRouteCreated(context.Context, ...attribute.KeyValue) error {
	return nil
}

func (*CounterBuilder) WithAttributes(...attribute.KeyValue) *CounterBuilder     { return nil }
func (*CounterBuilder) WithLabels(map[string]string) *CounterBuilder             { return nil }
func (*CounterBuilder) AddOne(context.Context) error                             { return nil }
func (*CounterBuilder) Add(context.Context, int64) error                         { return nil }
func (*HistogramBuilder) WithAttributes(...attribute.KeyValue) *HistogramBuilder { return nil }
func (*HistogramBuilder) Record(context.Context, int64) error                    { return nil }
func (*GaugeBuilder) WithAttributes(...attribute.KeyValue) *GaugeBuilder         { return nil }
func (*GaugeBuilder) Set(context.Context, int64) error                           { return nil }
