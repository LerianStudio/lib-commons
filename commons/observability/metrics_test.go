package observability

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/LerianStudio/lib-commons/commons/log"
)

func TestDefaultMetricsConfig(t *testing.T) {
	serviceName := "test-service"
	config := DefaultMetricsConfig(serviceName)

	assert.Equal(t, serviceName, config.ServiceName)
	assert.Equal(t, "1.0.0", config.ServiceVersion)
	assert.Equal(t, "production", config.Environment)
	assert.True(t, config.EnablePrometheus)
	assert.Equal(t, 8080, config.PrometheusPort)
	assert.Equal(t, 15*time.Second, config.CollectionInterval)
	assert.True(t, config.EnableRuntimeMetrics)
	assert.Equal(t, serviceName, config.ResourceAttributes["service.name"])
}

func TestNewMetricsManager(t *testing.T) {
	logger := &log.GoLogger{Level: log.DebugLevel}

	config := DefaultMetricsConfig("test-service")
	config.EnablePrometheus = false // Disable for testing

	mm, err := NewMetricsManager(config, logger)
	require.NoError(t, err)
	require.NotNil(t, mm)

	assert.NotNil(t, mm.businessMetrics)
	assert.NotNil(t, mm.httpMetrics)
	assert.NotNil(t, mm.databaseMetrics)
	assert.NotNil(t, mm.redisMetrics)
	assert.NotNil(t, mm.runtimeMetrics)
	assert.Equal(t, config, mm.config)
}

func TestNewMetricsManagerWithPrometheus(t *testing.T) {
	logger := &log.GoLogger{Level: log.DebugLevel}

	config := DefaultMetricsConfig("test-service")
	config.EnablePrometheus = true

	mm, err := NewMetricsManager(config, logger)
	require.NoError(t, err)
	require.NotNil(t, mm)

	assert.NotNil(t, mm.prometheusExporter)
	assert.NotNil(t, mm.GetPrometheusExporter())
}

func TestMetricsManagerCustomMetrics(t *testing.T) {
	logger := &log.GoLogger{Level: log.DebugLevel}

	config := DefaultMetricsConfig("test-service")
	config.EnablePrometheus = false

	mm, err := NewMetricsManager(config, logger)
	require.NoError(t, err)

	t.Run("RegisterCustomCounter", func(t *testing.T) {
		counter, err := mm.RegisterCustomCounter("test.counter", "Test counter", "1")
		require.NoError(t, err)
		assert.NotNil(t, counter)

		// Try to register the same counter again (should fail)
		_, err = mm.RegisterCustomCounter("test.counter", "Test counter", "1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
	})

	t.Run("RegisterCustomHistogram", func(t *testing.T) {
		histogram, err := mm.RegisterCustomHistogram("test.histogram", "Test histogram", "ms")
		require.NoError(t, err)
		assert.NotNil(t, histogram)

		// Try to register the same histogram again (should fail)
		_, err = mm.RegisterCustomHistogram("test.histogram", "Test histogram", "ms")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
	})

	t.Run("RegisterCustomGauge", func(t *testing.T) {
		gauge, err := mm.RegisterCustomGauge("test.gauge", "Test gauge", "1")
		require.NoError(t, err)
		assert.NotNil(t, gauge)

		// Try to register the same gauge again (should fail)
		_, err = mm.RegisterCustomGauge("test.gauge", "Test gauge", "1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
	})
}

func TestMetricsManagerAutoCollection(t *testing.T) {
	logger := &log.GoLogger{Level: log.DebugLevel}

	config := DefaultMetricsConfig("test-service")
	config.EnablePrometheus = false
	config.CollectionInterval = 100 * time.Millisecond // Short interval for testing

	mm, err := NewMetricsManager(config, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Start auto-collection
	mm.StartAutoCollection(ctx)

	// Wait a bit to let collection run
	time.Sleep(200 * time.Millisecond)

	// Stop auto-collection
	mm.StopAutoCollection()

	// Try to start again (should warn but not fail)
	mm.StartAutoCollection(ctx)
	mm.StopAutoCollection()
}

func TestNewBusinessMetrics(t *testing.T) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	bm, err := NewBusinessMetrics(meter)
	require.NoError(t, err)
	require.NotNil(t, bm)

	ctx := context.Background()

	// Test recording transaction
	bm.RecordTransaction(ctx, "org1", "ledger1", "completed", "transfer", 100.50, "USD")

	// Test recording transaction duration
	bm.RecordTransactionDuration(ctx, "org1", "ledger1", 150.0)

	// Test recording transaction error
	bm.RecordTransactionError(ctx, "org1", "ledger1", "validation_failed")

	// Test recording account
	bm.RecordAccount(ctx, "org1", "ledger1", "savings")

	// Test recording ledger
	bm.RecordLedger(ctx, "org1")

	// Test recording asset
	bm.RecordAsset(ctx, "org1", "ledger1", "currency")
}

func TestNewHTTPMetrics(t *testing.T) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	hm, err := NewHTTPMetrics(meter)
	require.NoError(t, err)
	require.NotNil(t, hm)

	ctx := context.Background()

	// Test recording request
	hm.RecordRequest(ctx, "POST", "/api/transactions", 200, 100*time.Millisecond, 1024, 512)

	// Test recording error
	hm.RecordError(ctx, "POST", "/api/transactions", "validation_error", 400)

	// Test recording timeout
	hm.RecordTimeout(ctx, "POST", "/api/transactions", "read_timeout")

	// Test recording connection lifecycle
	hm.RecordConnectionOpen(ctx, "127.0.0.1:8080")
	hm.RecordConnectionClose(ctx, "127.0.0.1:8080", 5*time.Second)

	// Test recording rate limiting
	hm.RecordRateLimit(ctx, "client1", "/api/transactions", "allowed")
	hm.RecordRateLimit(ctx, "client1", "/api/transactions", "blocked")
}

func TestNewDatabaseMetrics(t *testing.T) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	dm, err := NewDatabaseMetrics(meter)
	require.NoError(t, err)
	require.NotNil(t, dm)

	ctx := context.Background()

	// Test recording query
	dm.RecordQuery(ctx, "mydb", "users", "SELECT", 50*time.Millisecond, 10, false)
	dm.RecordQuery(ctx, "mydb", "users", "SELECT", 500*time.Millisecond, 1000, true) // slow query

	// Test recording query error
	dm.RecordQueryError(ctx, "mydb", "users", "SELECT", "syntax_error")

	// Test recording connection lifecycle
	dm.RecordConnection(ctx, "mydb", "opened", 0)
	dm.RecordConnection(ctx, "mydb", "closed", 10*time.Second)
	dm.RecordConnection(ctx, "mydb", "failed", 0)

	// Test recording transaction
	dm.RecordTransaction(ctx, "mydb", "begin", 0)
	dm.RecordTransaction(ctx, "mydb", "commit", 100*time.Millisecond)
	dm.RecordTransaction(ctx, "mydb", "rollback", 0)

	// Test recording lock wait
	dm.RecordLockWait(ctx, "mydb", "users", "row_lock", 200*time.Millisecond)

	// Test recording deadlock
	dm.RecordDeadlock(ctx, "mydb", []string{"users", "orders"})

	// Test database-specific metrics
	assert.NotNil(t, dm.GetPostgreSQLMetrics())
	assert.NotNil(t, dm.GetMongoDBMetrics())
}

func TestNewRedisMetrics(t *testing.T) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	rm, err := NewRedisMetrics(meter)
	require.NoError(t, err)
	require.NotNil(t, rm)

	ctx := context.Background()

	// Test recording command
	rm.RecordCommand(ctx, "GET", 0, 5*time.Millisecond, 1, false)
	rm.RecordCommand(ctx, "MGET", 0, 100*time.Millisecond, 100, true) // slow command

	// Test recording command error
	rm.RecordCommandError(ctx, "GET", 0, "key_not_found")

	// Test recording connection lifecycle
	rm.RecordConnection(ctx, "opened", "client")
	rm.RecordConnection(ctx, "closed", "client")
	rm.RecordConnection(ctx, "failed", "client")

	// Test recording cache operations
	rm.RecordCacheOperation(ctx, "hit", "user", 0)
	rm.RecordCacheOperation(ctx, "miss", "user", 0)

	// Test recording key operations
	rm.RecordKeyOperation(ctx, "get", "user", 0, 0)
	rm.RecordKeyOperation(ctx, "set", "user", 0, 1024)
	rm.RecordKeyOperation(ctx, "expired", "session", 0, 0)

	// Test recording cluster operations
	rm.RecordClusterOperation(ctx, "migration", "node1", "node2")

	// Test recording persistence operations
	rm.RecordPersistenceOperation(ctx, "rdb_save", 2*time.Second, true)
	rm.RecordPersistenceOperation(ctx, "aof_rewrite", 5*time.Second, false)
}

func TestNewRuntimeMetrics(t *testing.T) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	rtm, err := NewRuntimeMetrics(meter)
	require.NoError(t, err)
	require.NotNil(t, rtm)

	ctx := context.Background()

	// Test recording memory usage
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	rtm.RecordMemoryUsage(ctx, memStats)

	// Test recording goroutines
	rtm.RecordGoroutines(ctx, runtime.NumGoroutine())

	// Test recording GC stats
	rtm.RecordGCStats(ctx, memStats)

	// Test setup callbacks
	err = rtm.SetupObservableCallbacks()
	assert.NoError(t, err)

	// Test utility methods
	memStats2 := rtm.GetMemoryStats()
	assert.NotZero(t, memStats2.Alloc)

	goroutineCount := rtm.GetGoroutineCount()
	assert.Greater(t, goroutineCount, 0)

	cpuCount := rtm.GetNumCPU()
	assert.Greater(t, cpuCount, 0)

	gcStats := rtm.GetGCStats()
	assert.NotNil(t, gcStats)
}

func TestMetricsManagerShutdown(t *testing.T) {
	logger := &log.GoLogger{Level: log.DebugLevel}

	config := DefaultMetricsConfig("test-service")
	config.EnablePrometheus = false

	mm, err := NewMetricsManager(config, logger)
	require.NoError(t, err)

	// Start auto-collection
	ctx := context.Background()
	mm.StartAutoCollection(ctx)

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = mm.Shutdown(shutdownCtx)
	assert.NoError(t, err)
}

func TestHTTPStatusClass(t *testing.T) {
	testCases := []struct {
		statusCode int
		expected   string
	}{
		{100, "1xx"},
		{200, "2xx"},
		{404, "4xx"},
		{500, "5xx"},
		{999, "unknown"},
	}

	for _, tc := range testCases {
		t.Run(string(rune(tc.statusCode)), func(t *testing.T) {
			result := getStatusClass(tc.statusCode)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// Benchmark tests
func BenchmarkBusinessMetricsRecordTransaction(b *testing.B) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	bm, err := NewBusinessMetrics(meter)
	require.NoError(b, err)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bm.RecordTransaction(ctx, "org1", "ledger1", "completed", "transfer", 100.50, "USD")
	}
}

func BenchmarkHTTPMetricsRecordRequest(b *testing.B) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	hm, err := NewHTTPMetrics(meter)
	require.NoError(b, err)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hm.RecordRequest(ctx, "POST", "/api/transactions", 200, 100*time.Millisecond, 1024, 512)
	}
}

func BenchmarkDatabaseMetricsRecordQuery(b *testing.B) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	dm, err := NewDatabaseMetrics(meter)
	require.NoError(b, err)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dm.RecordQuery(ctx, "mydb", "users", "SELECT", 50*time.Millisecond, 10, false)
	}
}

func BenchmarkRedisMetricsRecordCommand(b *testing.B) {
	meterProvider := sdkmetric.NewMeterProvider()
	meter := meterProvider.Meter("test")

	rm, err := NewRedisMetrics(meter)
	require.NoError(b, err)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rm.RecordCommand(ctx, "GET", 0, 5*time.Millisecond, 1, false)
	}
}
