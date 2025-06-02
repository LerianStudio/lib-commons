package observability

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/zap"

	"github.com/LerianStudio/lib-commons/commons/log"
)

// BusinessMetrics provides business-specific metrics
type BusinessMetrics struct {
	meter metric.Meter

	// Transaction metrics
	transactionCounter      metric.Int64Counter
	transactionDuration     metric.Float64Histogram
	transactionAmount       metric.Float64Histogram
	transactionErrorCounter metric.Int64Counter

	// Account metrics
	accountCounter metric.Int64Counter

	// Ledger metrics
	ledgerCounter metric.Int64Counter

	// Asset metrics
	assetCounter metric.Int64Counter
}

// NewBusinessMetrics creates a new business metrics instance
func NewBusinessMetrics(meter metric.Meter) (*BusinessMetrics, error) {
	bm := &BusinessMetrics{meter: meter}

	// Initialize transaction metrics
	transactionCounter, err := meter.Int64Counter(
		"midaz.transaction.count",
		metric.WithDescription("Total number of transactions"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction counter: %w", err)
	}

	bm.transactionCounter = transactionCounter

	transactionDuration, err := meter.Float64Histogram(
		"midaz.transaction.duration",
		metric.WithDescription("Transaction processing duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction duration histogram: %w", err)
	}

	bm.transactionDuration = transactionDuration

	transactionAmount, err := meter.Float64Histogram(
		"midaz.transaction.amount",
		metric.WithDescription("Transaction amounts"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction amount histogram: %w", err)
	}

	bm.transactionAmount = transactionAmount

	transactionErrorCounter, err := meter.Int64Counter(
		"midaz.transaction.errors",
		metric.WithDescription("Total number of transaction errors"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction error counter: %w", err)
	}

	bm.transactionErrorCounter = transactionErrorCounter

	// Initialize account metrics
	accountCounter, err := meter.Int64Counter(
		"midaz.account.count",
		metric.WithDescription("Total number of accounts created"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create account counter: %w", err)
	}

	bm.accountCounter = accountCounter

	// Initialize ledger metrics
	ledgerCounter, err := meter.Int64Counter(
		"midaz.ledger.count",
		metric.WithDescription("Total number of ledgers created"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ledger counter: %w", err)
	}

	bm.ledgerCounter = ledgerCounter

	// Initialize asset metrics
	assetCounter, err := meter.Int64Counter(
		"midaz.asset.count",
		metric.WithDescription("Total number of assets created"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create asset counter: %w", err)
	}

	bm.assetCounter = assetCounter

	return bm, nil
}

// RecordTransaction records a transaction metric
func (bm *BusinessMetrics) RecordTransaction(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	status string,
	transactionType string,
	amount float64,
	currency string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
		attribute.String("status", status),
		attribute.String("type", transactionType),
	}

	if currency != "" {
		attrs = append(attrs, attribute.String("currency", currency))
	}

	bm.transactionCounter.Add(ctx, 1, metric.WithAttributes(attrs...))

	if amount > 0 {
		bm.transactionAmount.Record(ctx, amount, metric.WithAttributes(attrs...))
	}
}

// RecordTransactionDuration records transaction processing duration
func (bm *BusinessMetrics) RecordTransactionDuration(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	duration float64,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
	}

	bm.transactionDuration.Record(ctx, duration, metric.WithAttributes(attrs...))
}

// RecordTransactionError records a transaction error
func (bm *BusinessMetrics) RecordTransactionError(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	errorType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
		attribute.String("error_type", errorType),
	}

	bm.transactionErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAccount records an account creation
func (bm *BusinessMetrics) RecordAccount(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	accountType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
		attribute.String("account_type", accountType),
	}

	bm.accountCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordLedger records a ledger creation
func (bm *BusinessMetrics) RecordLedger(
	ctx context.Context,
	organizationID string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
	}

	bm.ledgerCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAsset records an asset creation
func (bm *BusinessMetrics) RecordAsset(
	ctx context.Context,
	organizationID string,
	ledgerID string,
	assetType string,
) {
	attrs := []attribute.KeyValue{
		attribute.String("organization_id", organizationID),
		attribute.String("ledger_id", ledgerID),
		attribute.String("asset_type", assetType),
	}

	bm.assetCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// MetricsManager provides comprehensive OpenTelemetry metrics collection and export
type MetricsManager struct {
	meter              metric.Meter
	metricProvider     *sdkmetric.MeterProvider
	prometheusExporter *prometheus.Exporter
	logger             log.Logger
	config             MetricsConfig

	// Business metrics
	businessMetrics *BusinessMetrics

	// Infrastructure metrics
	httpMetrics     *HTTPMetrics
	databaseMetrics *DatabaseMetrics
	redisMetrics    *RedisMetrics
	runtimeMetrics  *RuntimeMetrics

	// Custom metrics registry
	customMetrics map[string]interface{}
	mutex         sync.RWMutex

	// Auto-collection control
	stopAutoCollection  context.CancelFunc
	autoCollectionMutex sync.Mutex
}

// MetricsConfig defines configuration for metrics collection
type MetricsConfig struct {
	ServiceName          string            `json:"service_name"`
	ServiceVersion       string            `json:"service_version"`
	Environment          string            `json:"environment"`
	EnablePrometheus     bool              `json:"enable_prometheus"`
	PrometheusPort       int               `json:"prometheus_port"`
	CollectionInterval   time.Duration     `json:"collection_interval"`
	EnableRuntimeMetrics bool              `json:"enable_runtime_metrics"`
	ResourceAttributes   map[string]string `json:"resource_attributes"`
}

// DefaultMetricsConfig returns a production-ready metrics configuration
func DefaultMetricsConfig(serviceName string) MetricsConfig {
	return MetricsConfig{
		ServiceName:          serviceName,
		ServiceVersion:       "1.0.0",
		Environment:          "production",
		EnablePrometheus:     true,
		PrometheusPort:       8080,
		CollectionInterval:   15 * time.Second,
		EnableRuntimeMetrics: true,
		ResourceAttributes: map[string]string{
			"service.name":    serviceName,
			"service.version": "1.0.0",
		},
	}
}

// NewMetricsManager creates a new metrics manager with comprehensive collection
func NewMetricsManager(config MetricsConfig, logger log.Logger) (*MetricsManager, error) {
	// Create Prometheus exporter if enabled
	var prometheusExporter *prometheus.Exporter
	var err error

	if config.EnablePrometheus {
		prometheusExporter, err = prometheus.New()
		if err != nil {
			return nil, fmt.Errorf("failed to create Prometheus exporter: %w", err)
		}
	}

	// Create meter provider
	var meterProvider *sdkmetric.MeterProvider
	if prometheusExporter != nil {
		meterProvider = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(prometheusExporter),
		)
	} else {
		meterProvider = sdkmetric.NewMeterProvider()
	}

	// Set as global meter provider
	otel.SetMeterProvider(meterProvider)

	// Create meter
	meter := meterProvider.Meter(
		config.ServiceName,
		metric.WithInstrumentationVersion(config.ServiceVersion),
	)

	mm := &MetricsManager{
		meter:              meter,
		metricProvider:     meterProvider,
		prometheusExporter: prometheusExporter,
		logger:             logger,
		config:             config,
		customMetrics:      make(map[string]interface{}),
	}

	// Initialize business metrics
	bm, err := NewBusinessMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create business metrics: %w", err)
	}
	mm.businessMetrics = bm

	// Initialize infrastructure metrics
	hm, err := NewHTTPMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP metrics: %w", err)
	}
	mm.httpMetrics = hm

	dm, err := NewDatabaseMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create database metrics: %w", err)
	}
	mm.databaseMetrics = dm

	rm, err := NewRedisMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create Redis metrics: %w", err)
	}
	mm.redisMetrics = rm

	if config.EnableRuntimeMetrics {
		rtm, err := NewRuntimeMetrics(meter)
		if err != nil {
			return nil, fmt.Errorf("failed to create runtime metrics: %w", err)
		}
		mm.runtimeMetrics = rtm
	}

	logger.Info("Metrics manager initialized",
		zap.String("service_name", config.ServiceName),
		zap.Bool("prometheus_enabled", config.EnablePrometheus),
		zap.Bool("runtime_metrics_enabled", config.EnableRuntimeMetrics),
	)

	return mm, nil
}

// GetBusinessMetrics returns the business metrics instance
func (mm *MetricsManager) GetBusinessMetrics() *BusinessMetrics {
	return mm.businessMetrics
}

// GetHTTPMetrics returns the HTTP metrics instance
func (mm *MetricsManager) GetHTTPMetrics() *HTTPMetrics {
	return mm.httpMetrics
}

// GetDatabaseMetrics returns the database metrics instance
func (mm *MetricsManager) GetDatabaseMetrics() *DatabaseMetrics {
	return mm.databaseMetrics
}

// GetRedisMetrics returns the Redis metrics instance
func (mm *MetricsManager) GetRedisMetrics() *RedisMetrics {
	return mm.redisMetrics
}

// GetRuntimeMetrics returns the runtime metrics instance
func (mm *MetricsManager) GetRuntimeMetrics() *RuntimeMetrics {
	return mm.runtimeMetrics
}

// RegisterCustomCounter registers a custom counter metric
func (mm *MetricsManager) RegisterCustomCounter(
	name, description, unit string,
) (metric.Int64Counter, error) {
	mm.mutex.Lock()
	defer mm.mutex.Unlock()

	if _, exists := mm.customMetrics[name]; exists {
		return nil, fmt.Errorf("metric %s already registered", name)
	}

	counter, err := mm.meter.Int64Counter(
		name,
		metric.WithDescription(description),
		metric.WithUnit(unit),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create counter %s: %w", name, err)
	}

	mm.customMetrics[name] = counter
	mm.logger.Info("Custom counter registered", zap.String("name", name))

	return counter, nil
}

// RegisterCustomHistogram registers a custom histogram metric
func (mm *MetricsManager) RegisterCustomHistogram(
	name, description, unit string,
) (metric.Float64Histogram, error) {
	mm.mutex.Lock()
	defer mm.mutex.Unlock()

	if _, exists := mm.customMetrics[name]; exists {
		return nil, fmt.Errorf("metric %s already registered", name)
	}

	histogram, err := mm.meter.Float64Histogram(
		name,
		metric.WithDescription(description),
		metric.WithUnit(unit),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create histogram %s: %w", name, err)
	}

	mm.customMetrics[name] = histogram
	mm.logger.Info("Custom histogram registered", zap.String("name", name))

	return histogram, nil
}

// RegisterCustomGauge registers a custom gauge metric
func (mm *MetricsManager) RegisterCustomGauge(
	name, description, unit string,
) (metric.Float64ObservableGauge, error) {
	mm.mutex.Lock()
	defer mm.mutex.Unlock()

	if _, exists := mm.customMetrics[name]; exists {
		return nil, fmt.Errorf("metric %s already registered", name)
	}

	gauge, err := mm.meter.Float64ObservableGauge(
		name,
		metric.WithDescription(description),
		metric.WithUnit(unit),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gauge %s: %w", name, err)
	}

	mm.customMetrics[name] = gauge
	mm.logger.Info("Custom gauge registered", zap.String("name", name))

	return gauge, nil
}

// StartAutoCollection starts automatic collection of runtime and system metrics
func (mm *MetricsManager) StartAutoCollection(ctx context.Context) {
	mm.autoCollectionMutex.Lock()
	defer mm.autoCollectionMutex.Unlock()

	if mm.stopAutoCollection != nil {
		mm.logger.Warn("Auto-collection already started")
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	mm.stopAutoCollection = cancel

	if mm.runtimeMetrics != nil {
		go mm.runRuntimeMetricsCollection(ctx)
	}

	mm.logger.Info("Auto-collection started",
		zap.Duration("interval", mm.config.CollectionInterval),
	)
}

// StopAutoCollection stops automatic collection of metrics
func (mm *MetricsManager) StopAutoCollection() {
	mm.autoCollectionMutex.Lock()
	defer mm.autoCollectionMutex.Unlock()

	if mm.stopAutoCollection != nil {
		mm.stopAutoCollection()
		mm.stopAutoCollection = nil
		mm.logger.Info("Auto-collection stopped")
	}
}

// runRuntimeMetricsCollection runs runtime metrics collection in the background
func (mm *MetricsManager) runRuntimeMetricsCollection(ctx context.Context) {
	ticker := time.NewTicker(mm.config.CollectionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mm.collectRuntimeMetrics(ctx)
		}
	}
}

// collectRuntimeMetrics collects current runtime metrics
func (mm *MetricsManager) collectRuntimeMetrics(ctx context.Context) {
	if mm.runtimeMetrics == nil {
		return
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	mm.runtimeMetrics.RecordMemoryUsage(ctx, memStats)
	mm.runtimeMetrics.RecordGoroutines(ctx, runtime.NumGoroutine())
	mm.runtimeMetrics.RecordGCStats(ctx, memStats)
}

// GetPrometheusExporter returns the Prometheus exporter if enabled
func (mm *MetricsManager) GetPrometheusExporter() *prometheus.Exporter {
	return mm.prometheusExporter
}

// Shutdown gracefully shuts down the metrics manager
func (mm *MetricsManager) Shutdown(ctx context.Context) error {
	mm.StopAutoCollection()

	if mm.metricProvider != nil {
		if err := mm.metricProvider.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to shutdown metric provider: %w", err)
		}
	}

	mm.logger.Info("Metrics manager shutdown completed")
	return nil
}
