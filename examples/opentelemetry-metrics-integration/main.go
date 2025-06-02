// Package main demonstrates comprehensive OpenTelemetry metrics integration
// using the lib-commons observability package for infrastructure and business metrics.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/LerianStudio/lib-commons/commons/observability"
	"github.com/LerianStudio/lib-commons/commons/zap"
)

func main() {
	// Create logger
	logger, err := zap.NewStructured("metrics-demo", "info")
	if err != nil {
		log.Fatal("Failed to create logger:", err)
	}

	// Run all examples
	fmt.Println("=== OpenTelemetry Metrics Integration Examples ===")
	runBasicMetricsSetup(logger)
	runBusinessMetricsExample(logger)
	runInfrastructureMetricsExample(logger)
	runCustomMetricsExample(logger)
	runAutoCollectionExample(logger)
	runPrometheusIntegrationExample(logger)

	fmt.Println("\n=== All OpenTelemetry metrics examples completed ===")
}

// runBasicMetricsSetup demonstrates basic metrics manager setup
func runBasicMetricsSetup(logger *zap.Logger) {
	fmt.Println("\n--- Example 1: Basic Metrics Manager Setup ---")

	// Create metrics configuration
	config := observability.DefaultMetricsConfig("demo-service")
	config.Environment = "development"
	config.ServiceVersion = "1.0.0"
	config.EnablePrometheus = true
	config.PrometheusPort = 8090
	config.CollectionInterval = 10 * time.Second

	fmt.Printf("Configuration:\n")
	fmt.Printf(
		"  Service: %s v%s (%s)\n",
		config.ServiceName,
		config.ServiceVersion,
		config.Environment,
	)
	fmt.Printf("  Prometheus: %t (port %d)\n", config.EnablePrometheus, config.PrometheusPort)
	fmt.Printf("  Collection interval: %v\n", config.CollectionInterval)
	fmt.Printf("  Runtime metrics: %t\n", config.EnableRuntimeMetrics)

	// Create metrics manager
	metricsManager, err := observability.NewMetricsManager(config, logger)
	if err != nil {
		logger.Error("Failed to create metrics manager", zap.Error(err))
		return
	}
	defer metricsManager.Shutdown(context.Background())

	fmt.Println("✓ Metrics manager created successfully")
	fmt.Printf("  Business metrics: %v\n", metricsManager.GetBusinessMetrics() != nil)
	fmt.Printf("  HTTP metrics: %v\n", metricsManager.GetHTTPMetrics() != nil)
	fmt.Printf("  Database metrics: %v\n", metricsManager.GetDatabaseMetrics() != nil)
	fmt.Printf("  Redis metrics: %v\n", metricsManager.GetRedisMetrics() != nil)
	fmt.Printf("  Runtime metrics: %v\n", metricsManager.GetRuntimeMetrics() != nil)
}

// runBusinessMetricsExample demonstrates business metrics collection
func runBusinessMetricsExample(logger *zap.Logger) {
	fmt.Println("\n--- Example 2: Business Metrics Collection ---")

	config := observability.DefaultMetricsConfig("finance-service")
	config.EnablePrometheus = false // Disable for this example

	metricsManager, err := observability.NewMetricsManager(config, logger)
	if err != nil {
		logger.Error("Failed to create metrics manager", zap.Error(err))
		return
	}
	defer metricsManager.Shutdown(context.Background())

	businessMetrics := metricsManager.GetBusinessMetrics()
	ctx := context.Background()

	// Simulate financial operations
	operations := []struct {
		orgID    string
		ledgerID string
		txType   string
		amount   float64
		currency string
		status   string
	}{
		{"org1", "ledger1", "transfer", 1000.50, "USD", "completed"},
		{"org1", "ledger1", "payment", 250.75, "USD", "completed"},
		{"org2", "ledger2", "deposit", 5000.00, "EUR", "completed"},
		{"org1", "ledger1", "withdrawal", 100.00, "USD", "failed"},
		{"org3", "ledger3", "transfer", 750.25, "GBP", "pending"},
	}

	fmt.Println("Recording business transactions...")
	for i, op := range operations {
		// Record transaction
		businessMetrics.RecordTransaction(
			ctx,
			op.orgID,
			op.ledgerID,
			op.status,
			op.txType,
			op.amount,
			op.currency,
		)

		// Record transaction duration (simulated)
		duration := float64(50 + i*10) // Variable duration
		businessMetrics.RecordTransactionDuration(ctx, op.orgID, op.ledgerID, duration)

		// Record error if failed
		if op.status == "failed" {
			businessMetrics.RecordTransactionError(ctx, op.orgID, op.ledgerID, "insufficient_funds")
		}

		fmt.Printf(
			"  %d. %s %s %.2f %s (%s)\n",
			i+1,
			op.txType,
			op.status,
			op.amount,
			op.currency,
			op.orgID,
		)
	}

	// Record account and asset creation
	businessMetrics.RecordAccount(ctx, "org1", "ledger1", "checking")
	businessMetrics.RecordAccount(ctx, "org1", "ledger1", "savings")
	businessMetrics.RecordLedger(ctx, "org1")
	businessMetrics.RecordAsset(ctx, "org1", "ledger1", "currency")

	fmt.Println("✓ Business metrics recorded successfully")
}

// runInfrastructureMetricsExample demonstrates infrastructure metrics
func runInfrastructureMetricsExample(logger *zap.Logger) {
	fmt.Println("\n--- Example 3: Infrastructure Metrics Collection ---")

	config := observability.DefaultMetricsConfig("api-service")
	config.EnablePrometheus = false

	metricsManager, err := observability.NewMetricsManager(config, logger)
	if err != nil {
		logger.Error("Failed to create metrics manager", zap.Error(err))
		return
	}
	defer metricsManager.Shutdown(context.Background())

	httpMetrics := metricsManager.GetHTTPMetrics()
	dbMetrics := metricsManager.GetDatabaseMetrics()
	redisMetrics := metricsManager.GetRedisMetrics()
	ctx := context.Background()

	fmt.Println("Recording HTTP metrics...")
	// Simulate HTTP requests
	httpRequests := []struct {
		method     string
		path       string
		statusCode int
		duration   time.Duration
		reqSize    int64
		respSize   int64
	}{
		{"GET", "/api/health", 200, 10 * time.Millisecond, 0, 156},
		{"POST", "/api/transactions", 201, 150 * time.Millisecond, 1024, 512},
		{"GET", "/api/accounts", 200, 75 * time.Millisecond, 0, 2048},
		{"POST", "/api/accounts", 400, 25 * time.Millisecond, 512, 256},
		{"DELETE", "/api/transactions/123", 404, 20 * time.Millisecond, 0, 128},
	}

	for i, req := range httpRequests {
		httpMetrics.RecordRequest(
			ctx,
			req.method,
			req.path,
			req.statusCode,
			req.duration,
			req.reqSize,
			req.respSize,
		)

		if req.statusCode >= 400 {
			httpMetrics.RecordError(ctx, req.method, req.path, "client_error", req.statusCode)
		}

		fmt.Printf(
			"  %d. %s %s -> %d (%v)\n",
			i+1,
			req.method,
			req.path,
			req.statusCode,
			req.duration,
		)
	}

	fmt.Println("Recording database metrics...")
	// Simulate database operations
	dbMetrics.RecordQuery(ctx, "postgres", "accounts", "SELECT", 45*time.Millisecond, 25, false)
	dbMetrics.RecordQuery(ctx, "postgres", "transactions", "INSERT", 120*time.Millisecond, 1, false)
	dbMetrics.RecordQuery(
		ctx,
		"postgres",
		"accounts",
		"SELECT",
		500*time.Millisecond,
		1000,
		true,
	) // slow query
	dbMetrics.RecordConnection(ctx, "postgres", "opened", 0)
	dbMetrics.RecordTransaction(ctx, "postgres", "begin", 0)
	dbMetrics.RecordTransaction(ctx, "postgres", "commit", 80*time.Millisecond)

	fmt.Println("Recording Redis metrics...")
	// Simulate Redis operations
	redisMetrics.RecordCommand(ctx, "GET", 0, 2*time.Millisecond, 1, false)
	redisMetrics.RecordCommand(ctx, "SET", 0, 3*time.Millisecond, 1, false)
	redisMetrics.RecordCommand(ctx, "MGET", 0, 50*time.Millisecond, 100, true) // slow command
	redisMetrics.RecordCacheOperation(ctx, "hit", "user_session", 0)
	redisMetrics.RecordCacheOperation(ctx, "miss", "user_profile", 0)
	redisMetrics.RecordKeyOperation(ctx, "set", "cache_key", 0, 1024)

	fmt.Println("✓ Infrastructure metrics recorded successfully")
}

// runCustomMetricsExample demonstrates custom metrics registration
func runCustomMetricsExample(logger *zap.Logger) {
	fmt.Println("\n--- Example 4: Custom Metrics Registration ---")

	config := observability.DefaultMetricsConfig("custom-service")
	config.EnablePrometheus = false

	metricsManager, err := observability.NewMetricsManager(config, logger)
	if err != nil {
		logger.Error("Failed to create metrics manager", zap.Error(err))
		return
	}
	defer metricsManager.Shutdown(context.Background())

	ctx := context.Background()

	// Register custom counter
	orderCounter, err := metricsManager.RegisterCustomCounter(
		"ecommerce.orders.total",
		"Total number of orders processed",
		"1",
	)
	if err != nil {
		logger.Error("Failed to register order counter", zap.Error(err))
		return
	}

	// Register custom histogram
	orderValueHistogram, err := metricsManager.RegisterCustomHistogram(
		"ecommerce.order.value",
		"Order value distribution",
		"USD",
	)
	if err != nil {
		logger.Error("Failed to register order value histogram", zap.Error(err))
		return
	}

	// Register custom gauge
	inventoryGauge, err := metricsManager.RegisterCustomGauge(
		"ecommerce.inventory.items",
		"Current inventory count",
		"1",
	)
	if err != nil {
		logger.Error("Failed to register inventory gauge", zap.Error(err))
		return
	}

	fmt.Println("Custom metrics registered:")
	fmt.Println("  ✓ ecommerce.orders.total (counter)")
	fmt.Println("  ✓ ecommerce.order.value (histogram)")
	fmt.Println("  ✓ ecommerce.inventory.items (gauge)")

	// Use custom metrics
	fmt.Println("Recording custom metrics...")
	orderAttrs := []attribute.KeyValue{
		attribute.String("store", "online"),
		attribute.String("category", "electronics"),
	}

	orderCounter.Add(ctx, 1, metric.WithAttributes(orderAttrs...))
	orderCounter.Add(ctx, 1, metric.WithAttributes(orderAttrs...))
	orderValueHistogram.Record(ctx, 299.99, metric.WithAttributes(orderAttrs...))
	orderValueHistogram.Record(ctx, 149.50, metric.WithAttributes(orderAttrs...))

	fmt.Println("  ✓ Orders recorded with custom metrics")

	// Try to register duplicate metric (should fail)
	_, err = metricsManager.RegisterCustomCounter(
		"ecommerce.orders.total",
		"Duplicate counter",
		"1",
	)
	if err != nil {
		fmt.Printf("  ✓ Duplicate registration prevented: %s\n", err.Error())
	}
}

// runAutoCollectionExample demonstrates automatic metrics collection
func runAutoCollectionExample(logger *zap.Logger) {
	fmt.Println("\n--- Example 5: Automatic Metrics Collection ---")

	config := observability.DefaultMetricsConfig("monitoring-service")
	config.EnablePrometheus = false
	config.CollectionInterval = 500 * time.Millisecond // Fast for demo
	config.EnableRuntimeMetrics = true

	metricsManager, err := observability.NewMetricsManager(config, logger)
	if err != nil {
		logger.Error("Failed to create metrics manager", zap.Error(err))
		return
	}
	defer metricsManager.Shutdown(context.Background())

	// Set up runtime metrics callbacks
	runtimeMetrics := metricsManager.GetRuntimeMetrics()
	if err := runtimeMetrics.SetupObservableCallbacks(); err != nil {
		logger.Error("Failed to setup runtime metrics callbacks", zap.Error(err))
		return
	}

	fmt.Printf("Starting auto-collection (interval: %v)...\n", config.CollectionInterval)

	// Start auto-collection
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	metricsManager.StartAutoCollection(ctx)

	// Display current runtime metrics
	memStats := runtimeMetrics.GetMemoryStats()
	fmt.Printf("Runtime metrics snapshot:\n")
	fmt.Printf("  Goroutines: %d\n", runtimeMetrics.GetGoroutineCount())
	fmt.Printf("  CPUs: %d\n", runtimeMetrics.GetNumCPU())
	fmt.Printf("  Heap size: %d bytes\n", memStats.HeapSys)
	fmt.Printf("  Heap used: %d bytes\n", memStats.HeapInuse)
	fmt.Printf("  GC runs: %d\n", memStats.NumGC)

	// Generate some load to see metrics change
	fmt.Println("Generating load to demonstrate metrics collection...")
	for i := 0; i < 5; i++ {
		// Allocate some memory
		data := make([]byte, 1024*1024) // 1MB
		_ = data

		// Create some goroutines
		done := make(chan bool)
		for j := 0; j < 10; j++ {
			go func() {
				time.Sleep(100 * time.Millisecond)
				done <- true
			}()
		}

		// Wait for goroutines
		for j := 0; j < 10; j++ {
			<-done
		}

		time.Sleep(200 * time.Millisecond)
		fmt.Printf("  Load iteration %d completed (goroutines: %d)\n", i+1, runtime.NumGoroutine())
	}

	// Stop auto-collection
	metricsManager.StopAutoCollection()
	fmt.Println("✓ Auto-collection completed")

	// Show final metrics
	finalMemStats := runtimeMetrics.GetMemoryStats()
	fmt.Printf("Final runtime metrics:\n")
	fmt.Printf("  Goroutines: %d\n", runtimeMetrics.GetGoroutineCount())
	fmt.Printf("  Heap used: %d bytes\n", finalMemStats.HeapInuse)
	fmt.Printf("  GC runs: %d\n", finalMemStats.NumGC)
}

// runPrometheusIntegrationExample demonstrates Prometheus integration
func runPrometheusIntegrationExample(logger *zap.Logger) {
	fmt.Println("\n--- Example 6: Prometheus Integration ---")

	config := observability.DefaultMetricsConfig("prometheus-service")
	config.EnablePrometheus = true
	config.PrometheusPort = 8091

	metricsManager, err := observability.NewMetricsManager(config, logger)
	if err != nil {
		logger.Error("Failed to create metrics manager", zap.Error(err))
		return
	}
	defer metricsManager.Shutdown(context.Background())

	// Get Prometheus exporter
	promExporter := metricsManager.GetPrometheusExporter()
	if promExporter == nil {
		fmt.Println("✗ Prometheus exporter not available")
		return
	}

	fmt.Println("✓ Prometheus exporter created")

	// Record some sample metrics
	ctx := context.Background()
	businessMetrics := metricsManager.GetBusinessMetrics()
	httpMetrics := metricsManager.GetHTTPMetrics()

	// Generate sample data
	for i := 0; i < 10; i++ {
		businessMetrics.RecordTransaction(
			ctx,
			"org1",
			"ledger1",
			"completed",
			"transfer",
			float64(100+i*50),
			"USD",
		)

		httpMetrics.RecordRequest(
			ctx,
			"GET",
			"/api/metrics",
			200,
			time.Duration(10+i)*time.Millisecond,
			0,
			512,
		)
	}

	// Start HTTP server to expose Prometheus metrics
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:           fmt.Sprintf(":%d", config.PrometheusPort),
		Handler:        mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("Starting Prometheus metrics server on port %d...\n", config.PrometheusPort)
	fmt.Printf("Metrics available at: http://localhost:%d/metrics\n", config.PrometheusPort)

	// Start server in background
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Prometheus server error", zap.Error(err))
		}
	}()

	// Let server run for a moment
	time.Sleep(2 * time.Second)

	// Shutdown server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Failed to shutdown Prometheus server", zap.Error(err))
	}

	fmt.Println("✓ Prometheus integration example completed")
	fmt.Println(
		"  In production, the /metrics endpoint would remain available for Prometheus scraping",
	)
}
