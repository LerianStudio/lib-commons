package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MetricsCollector provides convenient methods for recording common metrics
type MetricsCollector struct {
	provider Provider

	// Counters
	requestCounter metric.Float64Counter
	errorCounter   metric.Float64Counter
	successCounter metric.Float64Counter
	retryCounter   metric.Float64Counter

	// Histograms
	requestDuration     metric.Float64Histogram
	requestBatchSize    metric.Int64Histogram
	requestBatchLatency metric.Int64Histogram
}

// NewMetricsCollector creates a new MetricsCollector for recording metrics
func NewMetricsCollector(provider Provider) (*MetricsCollector, error) {
	// If provider is not enabled, return a no-op collector
	if !provider.IsEnabled() {
		return &MetricsCollector{provider: provider}, nil
	}

	meter := provider.Meter()

	requestCounter, err := meter.Float64Counter(
		MetricRequestTotal,
		metric.WithDescription("Total number of requests made"),
	)
	if err != nil {
		return nil, err
	}

	errorCounter, err := meter.Float64Counter(
		MetricRequestErrorTotal,
		metric.WithDescription("Total number of request errors"),
	)
	if err != nil {
		return nil, err
	}

	successCounter, err := meter.Float64Counter(
		MetricRequestSuccess,
		metric.WithDescription("Total number of successful requests"),
	)
	if err != nil {
		return nil, err
	}

	retryCounter, err := meter.Float64Counter(
		MetricRequestRetryTotal,
		metric.WithDescription("Total number of request retries"),
	)
	if err != nil {
		return nil, err
	}

	requestDuration, err := meter.Float64Histogram(
		MetricRequestDuration,
		metric.WithDescription("Duration of requests in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	requestBatchSize, err := meter.Int64Histogram(
		MetricRequestBatchSize,
		metric.WithDescription("Size of request batches"),
	)
	if err != nil {
		return nil, err
	}

	requestBatchLatency, err := meter.Int64Histogram(
		MetricRequestBatchLatency,
		metric.WithDescription("Latency of request batches in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	return &MetricsCollector{
		provider:            provider,
		requestCounter:      requestCounter,
		errorCounter:        errorCounter,
		successCounter:      successCounter,
		retryCounter:        retryCounter,
		requestDuration:     requestDuration,
		requestBatchSize:    requestBatchSize,
		requestBatchLatency: requestBatchLatency,
	}, nil
}

// RecordRequest records a request with its result and duration
func (m *MetricsCollector) RecordRequest(ctx context.Context, operation, resourceType string, statusCode int, duration time.Duration, attrs ...attribute.KeyValue) {
	// If provider is not enabled, do nothing
	if !m.provider.IsEnabled() {
		return
	}

	// Set base attributes
	baseAttrs := []attribute.KeyValue{
		attribute.String(KeyOperationName, operation),
		attribute.String(KeyOperationType, "request"),
		attribute.String(KeyResourceType, resourceType),
		attribute.Int(KeyHTTPStatus, statusCode),
	}

	// Combine with additional attributes
	allAttrs := append(baseAttrs, attrs...)

	// Record request
	m.requestCounter.Add(ctx, 1, metric.WithAttributes(allAttrs...))

	// Record duration in milliseconds
	m.requestDuration.Record(ctx, float64(duration.Milliseconds()), metric.WithAttributes(allAttrs...))

	// Record success or error
	if statusCode >= 400 {
		// Error
		m.errorCounter.Add(ctx, 1, metric.WithAttributes(allAttrs...))
	} else {
		// Success
		m.successCounter.Add(ctx, 1, metric.WithAttributes(allAttrs...))
	}
}

// RecordBatchRequest records a batch request with its size and latency
func (m *MetricsCollector) RecordBatchRequest(ctx context.Context, operation, resourceType string, batchSize int, duration time.Duration, attrs ...attribute.KeyValue) {
	// If provider is not enabled, do nothing
	if !m.provider.IsEnabled() {
		return
	}

	// Set base attributes
	baseAttrs := []attribute.KeyValue{
		attribute.String(KeyOperationName, operation),
		attribute.String(KeyOperationType, "batch"),
		attribute.String(KeyResourceType, resourceType),
	}

	// Combine with additional attributes
	allAttrs := append(baseAttrs, attrs...)

	// Record batch size
	m.requestBatchSize.Record(ctx, int64(batchSize), metric.WithAttributes(allAttrs...))

	// Record batch latency in milliseconds
	m.requestBatchLatency.Record(ctx, int64(duration.Milliseconds()), metric.WithAttributes(allAttrs...))
}

// RecordRetry records a retry attempt
func (m *MetricsCollector) RecordRetry(ctx context.Context, operation, resourceType string, attempt int, attrs ...attribute.KeyValue) {
	// If provider is not enabled, do nothing
	if !m.provider.IsEnabled() {
		return
	}

	// Set base attributes
	baseAttrs := []attribute.KeyValue{
		attribute.String(KeyOperationName, operation),
		attribute.String(KeyOperationType, "retry"),
		attribute.String(KeyResourceType, resourceType),
		attribute.Int("retry.attempt", attempt),
	}

	// Combine with additional attributes
	allAttrs := append(baseAttrs, attrs...)

	// Record retry
	m.retryCounter.Add(ctx, 1, metric.WithAttributes(allAttrs...))
}

// RecordError records an error occurrence
func (m *MetricsCollector) RecordError(ctx context.Context, operation, resourceType, errorType string, attrs ...attribute.KeyValue) {
	// If provider is not enabled, do nothing
	if !m.provider.IsEnabled() {
		return
	}

	// Set base attributes
	baseAttrs := []attribute.KeyValue{
		attribute.String(KeyOperationName, operation),
		attribute.String(KeyOperationType, "error"),
		attribute.String(KeyResourceType, resourceType),
		attribute.String("error.type", errorType),
	}

	// Combine with additional attributes
	allAttrs := append(baseAttrs, attrs...)

	// Record error
	m.errorCounter.Add(ctx, 1, metric.WithAttributes(allAttrs...))
}

// Timer provides a convenient way to record the duration of an operation
type Timer struct {
	startTime    time.Time
	collector    *MetricsCollector
	ctx          context.Context
	operation    string
	resourceType string
	attrs        []attribute.KeyValue
}

// NewTimer creates a new timer for recording the duration of an operation
func (m *MetricsCollector) NewTimer(ctx context.Context, operation, resourceType string, attrs ...attribute.KeyValue) *Timer {
	return &Timer{
		startTime:    time.Now(),
		collector:    m,
		ctx:          ctx,
		operation:    operation,
		resourceType: resourceType,
		attrs:        attrs,
	}
}

// Stop records the duration of the operation with the result
func (t *Timer) Stop(statusCode int, additionalAttrs ...attribute.KeyValue) {
	duration := time.Since(t.startTime)
	allAttrs := append(t.attrs, additionalAttrs...)
	t.collector.RecordRequest(t.ctx, t.operation, t.resourceType, statusCode, duration, allAttrs...)
}

// StopBatch records the duration of a batch operation
func (t *Timer) StopBatch(batchSize int, additionalAttrs ...attribute.KeyValue) {
	duration := time.Since(t.startTime)
	allAttrs := append(t.attrs, additionalAttrs...)
	t.collector.RecordBatchRequest(t.ctx, t.operation, t.resourceType, batchSize, duration, allAttrs...)
}

// StopWithError records the duration and marks it as an error
func (t *Timer) StopWithError(errorType string, additionalAttrs ...attribute.KeyValue) {
	duration := time.Since(t.startTime)
	allAttrs := append(t.attrs, additionalAttrs...)
	// Record as error (status code 500)
	t.collector.RecordRequest(t.ctx, t.operation, t.resourceType, 500, duration, allAttrs...)
	// Also record specific error
	t.collector.RecordError(t.ctx, t.operation, t.resourceType, errorType, allAttrs...)
}

// BatchTimer provides a convenient way to record batch operations
type BatchTimer struct {
	Timer
	items []interface{}
}

// NewBatchTimer creates a new timer for batch operations
func (m *MetricsCollector) NewBatchTimer(ctx context.Context, operation, resourceType string, attrs ...attribute.KeyValue) *BatchTimer {
	return &BatchTimer{
		Timer: Timer{
			startTime:    time.Now(),
			collector:    m,
			ctx:          ctx,
			operation:    operation,
			resourceType: resourceType,
			attrs:        attrs,
		},
		items: make([]interface{}, 0),
	}
}

// AddItem adds an item to the batch
func (bt *BatchTimer) AddItem(item interface{}) {
	bt.items = append(bt.items, item)
}

// AddItems adds multiple items to the batch
func (bt *BatchTimer) AddItems(items ...interface{}) {
	bt.items = append(bt.items, items...)
}

// Stop records the batch operation
func (bt *BatchTimer) Stop(additionalAttrs ...attribute.KeyValue) {
	bt.StopBatch(len(bt.items), additionalAttrs...)
}

// Counter provides a simple counter abstraction
type Counter struct {
	collector    *MetricsCollector
	name         string
	resourceType string
	attrs        []attribute.KeyValue
}

// NewCounter creates a new counter
func (m *MetricsCollector) NewCounter(name, resourceType string, attrs ...attribute.KeyValue) *Counter {
	return &Counter{
		collector:    m,
		name:         name,
		resourceType: resourceType,
		attrs:        attrs,
	}
}

// Inc increments the counter by 1
func (c *Counter) Inc(ctx context.Context, additionalAttrs ...attribute.KeyValue) {
	c.Add(ctx, 1, additionalAttrs...)
}

// Add adds a value to the counter
func (c *Counter) Add(ctx context.Context, value float64, additionalAttrs ...attribute.KeyValue) {
	if !c.collector.provider.IsEnabled() {
		return
	}

	baseAttrs := []attribute.KeyValue{
		attribute.String(KeyOperationName, c.name),
		attribute.String(KeyResourceType, c.resourceType),
	}

	allAttrs := append(baseAttrs, c.attrs...)
	allAttrs = append(allAttrs, additionalAttrs...)

	c.collector.requestCounter.Add(ctx, value, metric.WithAttributes(allAttrs...))
}
