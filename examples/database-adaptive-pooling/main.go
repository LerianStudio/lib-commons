// Package main demonstrates advanced database connection pooling with adaptive sizing,
// comprehensive monitoring, circuit breaker protection, and real-time metrics collection.
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/commons/mongo"
	"github.com/LerianStudio/lib-commons/commons/postgres"
	"github.com/LerianStudio/lib-commons/commons/zap"
	"go.mongodb.org/mongo-driver/bson"
)

func main() {
	fmt.Println("Database Adaptive Connection Pooling Demo")
	fmt.Println("=========================================")

	// Set up environment for demo
	setupDemoEnvironment()

	// Initialize logger
	logger, err := zap.NewStructured("adaptive-pooling-demo", "debug")
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	// Example 1: PostgreSQL adaptive pooling
	fmt.Println("\n1. PostgreSQL Adaptive Pooling")
	postgresAdaptivePoolingExample(logger)

	// Example 2: MongoDB adaptive pooling
	fmt.Println("\n2. MongoDB Adaptive Pooling")
	mongoAdaptivePoolingExample(logger)

	// Example 3: Pool scaling under load
	fmt.Println("\n3. Pool Scaling Under Load")
	poolScalingExample(logger)

	// Example 4: Circuit breaker protection
	fmt.Println("\n4. Circuit Breaker Protection")
	circuitBreakerExample(logger)

	// Example 5: Performance monitoring and metrics
	fmt.Println("\n5. Performance Monitoring and Metrics")
	performanceMonitoringExample(logger)

	// Example 6: Real-world load simulation
	fmt.Println("\n6. Real-World Load Simulation")
	realWorldLoadExample(logger)

	// Example 7: Pool configuration comparison
	fmt.Println("\n7. Pool Configuration Comparison")
	configurationComparisonExample(logger)
}

// setupDemoEnvironment sets up environment variables for demonstration
func setupDemoEnvironment() {
	// PostgreSQL connection (using a mock connection string for demo)
	_ = os.Setenv("POSTGRES_DSN", "postgres://demo:demo@localhost:5432/demo_db?sslmode=disable")

	// MongoDB connection (using a mock connection string for demo)
	_ = os.Setenv("MONGODB_URI", "mongodb://demo:demo@localhost:27017")
	_ = os.Setenv("MONGODB_DATABASE", "demo_db")
}

// postgresAdaptivePoolingExample demonstrates PostgreSQL adaptive pooling
func postgresAdaptivePoolingExample(logger *zap.Logger) {
	fmt.Println("Setting up PostgreSQL adaptive connection pool...")

	// Create adaptive pool configuration
	config := postgres.AdaptivePoolConfig{
		MinConnections:     3,
		MaxConnections:     20,
		InitialConnections: 5,
		ConnectionTimeout:  10 * time.Second,
		IdleTimeout:        5 * time.Minute,
		MaxLifetime:        30 * time.Minute,

		// Adaptive scaling settings
		ScaleUpThreshold:   0.7, // Scale up when 70% utilized
		ScaleDownThreshold: 0.3, // Scale down when 30% utilized
		ScaleInterval:      15 * time.Second,
		ScaleUpFactor:      1.4, // 40% increase
		ScaleDownFactor:    0.8, // 20% decrease

		// Performance settings
		LatencyThreshold:    50 * time.Millisecond,
		HealthCheckInterval: 30 * time.Second,

		// Circuit breaker settings
		EnableCircuitBreaker: true,
		FailureThreshold:     5,
		RecoveryTimeout:      30 * time.Second,
	}

	// Note: Using mock DSN for demo - in real usage, use actual database
	mockDSN := "postgres://user:pass@localhost:5432/testdb?sslmode=disable"

	// Create adaptive pool (this will fail with mock DSN, but demonstrates the interface)
	pool, err := postgres.NewAdaptivePostgresPool(mockDSN, config, logger)
	if err != nil {
		fmt.Printf("❌ Failed to create adaptive pool (expected with mock DSN): %v\n", err)

		// Demonstrate configuration and capabilities instead
		fmt.Printf("✅ Configuration created successfully:\n")
		fmt.Printf("   Min connections: %d\n", config.MinConnections)
		fmt.Printf("   Max connections: %d\n", config.MaxConnections)
		fmt.Printf("   Scale up threshold: %.1f%%\n", config.ScaleUpThreshold*100)
		fmt.Printf("   Scale down threshold: %.1f%%\n", config.ScaleDownThreshold*100)
		fmt.Printf("   Circuit breaker enabled: %t\n", config.EnableCircuitBreaker)
		fmt.Printf("   Health check interval: %v\n", config.HealthCheckInterval)
		return
	}
	defer pool.Close()

	// Simulate database operations
	fmt.Println("🔄 Simulating database operations...")
	ctx := context.Background()

	// Simulate some queries
	for i := 0; i < 5; i++ {
		query := fmt.Sprintf("SELECT * FROM users WHERE id = %d", i+1)

		_, err := pool.ExecuteQuery(ctx, query)
		if err != nil {
			fmt.Printf("   Query %d failed: %v\n", i+1, err)
		} else {
			fmt.Printf("   ✅ Query %d executed successfully\n", i+1)
		}

		time.Sleep(100 * time.Millisecond)
	}

	// Display metrics
	metrics := pool.GetMetrics()
	fmt.Printf("\n📊 Pool Metrics:\n")
	fmt.Printf("   Active connections: %d\n", metrics.ActiveConnections)
	fmt.Printf("   Total connections: %d\n", metrics.TotalConnections)
	fmt.Printf("   Success rate: %.2f%%\n", metrics.SuccessRate*100)
	fmt.Printf("   Average latency: %v\n", metrics.AverageLatency)
	fmt.Printf("   Circuit breaker: %s\n", metrics.CircuitBreakerStatus)
	fmt.Printf("   Health score: %.2f\n", metrics.HealthScore)

	fmt.Println("✅ PostgreSQL adaptive pooling example completed")
}

// mongoAdaptivePoolingExample demonstrates MongoDB adaptive pooling
func mongoAdaptivePoolingExample(logger *zap.Logger) {
	fmt.Println("Setting up MongoDB adaptive connection pool...")

	// Create adaptive pool configuration
	config := mongo.AdaptiveMongoPoolConfig{
		MinPoolSize:     3,
		MaxPoolSize:     25,
		InitialPoolSize: 5,
		MaxConnIdleTime: 10 * time.Minute,
		ConnectTimeout:  5 * time.Second,
		SocketTimeout:   30 * time.Second,

		// Adaptive scaling settings
		ScaleUpThreshold:   0.75, // Scale up when 75% utilized
		ScaleDownThreshold: 0.25, // Scale down when 25% utilized
		ScaleInterval:      20 * time.Second,
		ScaleUpFactor:      1.5, // 50% increase
		ScaleDownFactor:    0.8, // 20% decrease

		// Performance settings
		LatencyThreshold:    80 * time.Millisecond,
		HealthCheckInterval: 45 * time.Second,

		// Circuit breaker settings
		EnableCircuitBreaker: true,
		FailureThreshold:     8,
		RecoveryTimeout:      45 * time.Second,

		// Read preference
		ReadPreference: "primaryPreferred",
	}

	// Note: Using mock URI for demo - in real usage, use actual database
	mockURI := "mongodb://user:pass@localhost:27017"
	mockDatabase := "testdb"

	// Create adaptive pool (this will fail with mock URI, but demonstrates the interface)
	pool, err := mongo.NewAdaptiveMongoPool(mockURI, mockDatabase, config, logger)
	if err != nil {
		fmt.Printf("❌ Failed to create adaptive pool (expected with mock URI): %v\n", err)

		// Demonstrate configuration and capabilities instead
		fmt.Printf("✅ Configuration created successfully:\n")
		fmt.Printf("   Min pool size: %d\n", config.MinPoolSize)
		fmt.Printf("   Max pool size: %d\n", config.MaxPoolSize)
		fmt.Printf("   Scale up threshold: %.1f%%\n", config.ScaleUpThreshold*100)
		fmt.Printf("   Scale down threshold: %.1f%%\n", config.ScaleDownThreshold*100)
		fmt.Printf("   Read preference: %s\n", config.ReadPreference)
		fmt.Printf("   Circuit breaker enabled: %t\n", config.EnableCircuitBreaker)
		return
	}
	defer pool.Close()

	// Simulate database operations
	fmt.Println("🔄 Simulating MongoDB operations...")
	ctx := context.Background()

	// Simulate document operations
	for i := 0; i < 5; i++ {
		// Simulate find operation
		filter := bson.M{"user_id": i + 1}
		_, err := pool.FindOne(ctx, "users", filter)
		if err != nil {
			fmt.Printf("   FindOne %d failed: %v\n", i+1, err)
		} else {
			fmt.Printf("   ✅ FindOne %d executed successfully\n", i+1)
		}

		// Simulate insert operation
		doc := bson.M{
			"user_id":    i + 100,
			"name":       fmt.Sprintf("User %d", i+100),
			"created_at": time.Now(),
		}
		_, err = pool.InsertOne(ctx, "users", doc)
		if err != nil {
			fmt.Printf("   InsertOne %d failed: %v\n", i+1, err)
		} else {
			fmt.Printf("   ✅ InsertOne %d executed successfully\n", i+1)
		}

		time.Sleep(150 * time.Millisecond)
	}

	// Display metrics
	metrics := pool.GetMetrics()
	fmt.Printf("\n📊 Pool Metrics:\n")
	fmt.Printf("   Total connections: %d\n", metrics.TotalConnections)
	fmt.Printf("   Max pool size: %d\n", metrics.MaxPoolSize)
	fmt.Printf("   Success rate: %.2f%%\n", metrics.SuccessRate*100)
	fmt.Printf("   Average latency: %v\n", metrics.AverageLatency)
	fmt.Printf("   Circuit breaker: %s\n", metrics.CircuitBreakerStatus)
	fmt.Printf("   Health score: %.2f\n", metrics.HealthScore)
	fmt.Printf("   Database size: %d bytes\n", metrics.DatabaseSize)

	fmt.Println("✅ MongoDB adaptive pooling example completed")
}

// poolScalingExample demonstrates automatic pool scaling under varying load
func poolScalingExample(logger *zap.Logger) {
	fmt.Println("Demonstrating pool scaling under varying load...")

	// Create configuration optimized for scaling demonstration
	config := postgres.DefaultAdaptivePoolConfig()
	config.MinConnections = 2
	config.MaxConnections = 15
	config.InitialConnections = 3
	config.ScaleInterval = 5 * time.Second // Faster scaling for demo
	config.ScaleUpThreshold = 0.6          // Lower threshold for demo
	config.ScaleDownThreshold = 0.2        // Lower threshold for demo
	config.HealthCheckInterval = 3 * time.Second

	fmt.Printf("✅ Pool scaling configuration:\n")
	fmt.Printf("   Initial connections: %d\n", config.InitialConnections)
	fmt.Printf("   Min/Max connections: %d/%d\n", config.MinConnections, config.MaxConnections)
	fmt.Printf("   Scale up at: %.1f%% utilization\n", config.ScaleUpThreshold*100)
	fmt.Printf("   Scale down at: %.1f%% utilization\n", config.ScaleDownThreshold*100)
	fmt.Printf("   Scaling check interval: %v\n", config.ScaleInterval)

	// Simulate load phases
	fmt.Println("\n🔄 Simulating load phases:")
	fmt.Println("   Phase 1: Low load (should scale down)")
	time.Sleep(2 * time.Second)

	fmt.Println("   Phase 2: Medium load (steady state)")
	time.Sleep(2 * time.Second)

	fmt.Println("   Phase 3: High load (should scale up)")
	time.Sleep(2 * time.Second)

	fmt.Println("   Phase 4: Load spike (maximum scaling)")
	time.Sleep(2 * time.Second)

	fmt.Println("   Phase 5: Load reduction (gradual scale down)")

	// In a real implementation, this would show actual scaling events
	fmt.Printf("\n📈 Scaling Events (simulated):\n")
	fmt.Printf("   T+00s: Pool initialized with %d connections\n", config.InitialConnections)
	fmt.Printf(
		"   T+05s: Low utilization detected, scaling down to %d connections\n",
		config.MinConnections,
	)
	fmt.Printf(
		"   T+15s: High utilization detected, scaling up to %d connections\n",
		int(float64(config.MinConnections)*config.ScaleUpFactor),
	)
	fmt.Printf(
		"   T+20s: Load spike detected, scaling up to %d connections\n",
		config.MaxConnections,
	)
	fmt.Printf(
		"   T+30s: Load normalized, scaling down to %d connections\n",
		int(float64(config.MaxConnections)*config.ScaleDownFactor),
	)

	fmt.Println("✅ Pool scaling example completed")
}

// circuitBreakerExample demonstrates circuit breaker protection
func circuitBreakerExample(logger *zap.Logger) {
	fmt.Println("Demonstrating circuit breaker protection...")

	// Create configuration with aggressive circuit breaker for demo
	config := postgres.DefaultAdaptivePoolConfig()
	config.EnableCircuitBreaker = true
	config.FailureThreshold = 3 // Trip after 3 failures
	config.RecoveryTimeout = 10 * time.Second
	config.MaxRetries = 2

	fmt.Printf("✅ Circuit breaker configuration:\n")
	fmt.Printf("   Enabled: %t\n", config.EnableCircuitBreaker)
	fmt.Printf("   Failure threshold: %d\n", config.FailureThreshold)
	fmt.Printf("   Recovery timeout: %v\n", config.RecoveryTimeout)
	fmt.Printf("   Max retries per operation: %d\n", config.MaxRetries)

	// Simulate circuit breaker states
	fmt.Println("\n🔄 Simulating circuit breaker behavior:")

	fmt.Println("   State: CLOSED (normal operation)")
	fmt.Println("   ✅ Query 1: Success")
	fmt.Println("   ✅ Query 2: Success")
	fmt.Println("   ✅ Query 3: Success")

	fmt.Println("\n   Simulating database failures...")
	fmt.Println("   ❌ Query 4: Failed (1/3 failures)")
	fmt.Println("   ❌ Query 5: Failed (2/3 failures)")
	fmt.Println("   ❌ Query 6: Failed (3/3 failures) - Circuit breaker OPEN")

	fmt.Println("\n   State: OPEN (blocking all requests)")
	fmt.Println("   🚫 Query 7: Blocked by circuit breaker")
	fmt.Println("   🚫 Query 8: Blocked by circuit breaker")

	fmt.Println("\n   Waiting for recovery timeout...")
	time.Sleep(1 * time.Second) // Simulated wait

	fmt.Println("   State: HALF-OPEN (testing recovery)")
	fmt.Println("   🧪 Query 9: Test query - Success")
	fmt.Println("   State: CLOSED (recovery successful)")
	fmt.Println("   ✅ Query 10: Success")

	// Show benefits
	fmt.Printf("\n🛡️  Circuit breaker benefits:\n")
	fmt.Printf("   • Prevents cascade failures\n")
	fmt.Printf("   • Reduces load on failing database\n")
	fmt.Printf("   • Provides fast failure response\n")
	fmt.Printf("   • Automatic recovery detection\n")
	fmt.Printf("   • Configurable failure thresholds\n")

	fmt.Println("✅ Circuit breaker example completed")
}

// performanceMonitoringExample demonstrates comprehensive performance monitoring
func performanceMonitoringExample(logger *zap.Logger) {
	fmt.Println("Demonstrating comprehensive performance monitoring...")

	// Create mock metrics to demonstrate monitoring capabilities
	fmt.Println("📊 Real-time Performance Metrics Dashboard:")

	// Connection metrics
	fmt.Println("\n🔗 Connection Metrics:")
	fmt.Printf("   Active connections: %d\n", 8)
	fmt.Printf("   Idle connections: %d\n", 12)
	fmt.Printf("   Total connections: %d\n", 20)
	fmt.Printf("   Max connections: %d\n", 50)
	fmt.Printf("   Connection utilization: %.1f%%\n", 40.0)
	fmt.Printf("   Connection turnover rate: %.2f/min\n", 2.5)

	// Performance metrics
	fmt.Println("\n⚡ Performance Metrics:")
	fmt.Printf("   Average latency: %v\n", 45*time.Millisecond)
	fmt.Printf("   P95 latency: %v\n", 120*time.Millisecond)
	fmt.Printf("   P99 latency: %v\n", 250*time.Millisecond)
	fmt.Printf("   Throughput: %.1f queries/sec\n", 125.3)
	fmt.Printf("   Queue wait time: %v\n", 2*time.Millisecond)

	// Error and reliability metrics
	fmt.Println("\n🎯 Reliability Metrics:")
	fmt.Printf("   Success rate: %.2f%%\n", 99.7)
	fmt.Printf("   Failed connections: %d\n", 3)
	fmt.Printf("   Timeout connections: %d\n", 1)
	fmt.Printf("   Circuit breaker status: %s\n", "closed")
	fmt.Printf("   Health score: %.2f/1.00\n", 0.97)

	// Operational metrics
	fmt.Println("\n🔧 Operational Metrics:")
	fmt.Printf("   Uptime: %v\n", 24*time.Hour+35*time.Minute)
	fmt.Printf("   Total queries: %d\n", 45231)
	fmt.Printf("   Successful queries: %d\n", 45095)
	fmt.Printf("   Failed queries: %d\n", 136)
	fmt.Printf("   Pool efficiency: %.2f%%\n", 99.2)

	// Adaptive scaling metrics
	fmt.Println("\n📈 Adaptive Scaling Metrics:")
	fmt.Printf("   Last scaling action: %v ago\n", 5*time.Minute)
	fmt.Printf("   Scaling direction: %s\n", "up")
	fmt.Printf("   Scale up events: %d\n", 8)
	fmt.Printf("   Scale down events: %d\n", 12)
	fmt.Printf("   Resource utilization: %.1f%%\n", 67.5)

	// Alert thresholds
	fmt.Println("\n🚨 Alert Thresholds and Status:")
	alertStatus := []struct {
		metric    string
		current   float64
		threshold float64
		unit      string
		status    string
	}{
		{"Latency", 45, 100, "ms", "✅ OK"},
		{"Success rate", 99.7, 95.0, "%", "✅ OK"},
		{"Connection utilization", 40.0, 80.0, "%", "✅ OK"},
		{"Queue wait time", 2, 5000, "ms", "✅ OK"},
		{"Health score", 0.97, 0.8, "", "✅ OK"},
	}

	for _, alert := range alertStatus {
		fmt.Printf("   %s: %.1f%s (threshold: %.1f%s) %s\n",
			alert.metric, alert.current, alert.unit, alert.threshold, alert.unit, alert.status)
	}

	// Monitoring recommendations
	fmt.Println("\n💡 Monitoring Recommendations:")
	fmt.Printf("   • Set up alerts for health score < 0.8\n")
	fmt.Printf("   • Monitor P99 latency trends\n")
	fmt.Printf("   • Track connection pool scaling events\n")
	fmt.Printf("   • Set up dashboards for real-time visibility\n")
	fmt.Printf("   • Configure automated scaling policies\n")

	fmt.Println("✅ Performance monitoring example completed")
}

// realWorldLoadExample simulates real-world usage patterns
func realWorldLoadExample(logger *zap.Logger) {
	fmt.Println("Simulating real-world load patterns...")

	// Define different load patterns
	loadPatterns := []struct {
		name        string
		duration    time.Duration
		concurrency int
		qps         float64
		description string
	}{
		{"Morning Rush", 5 * time.Second, 15, 45.0, "Heavy load from morning users"},
		{"Midday Steady", 3 * time.Second, 8, 25.0, "Consistent moderate load"},
		{"Afternoon Peak", 4 * time.Second, 20, 60.0, "Peak business hours"},
		{"Evening Decline", 3 * time.Second, 5, 12.0, "Declining user activity"},
		{"Night Maintenance", 2 * time.Second, 2, 3.0, "Minimal background tasks"},
	}

	fmt.Println("📅 24-Hour Load Simulation (accelerated):")

	for i, pattern := range loadPatterns {
		fmt.Printf("\n%d. %s (%v)\n", i+1, pattern.name, pattern.duration)
		fmt.Printf("   Description: %s\n", pattern.description)
		fmt.Printf("   Concurrency: %d workers\n", pattern.concurrency)
		fmt.Printf("   Target QPS: %.1f\n", pattern.qps)

		// Simulate the load pattern
		simulateLoadPattern(pattern.concurrency, pattern.duration, pattern.qps, logger)
	}

	// Show adaptive responses
	fmt.Println("\n🔄 Adaptive Pool Responses:")
	fmt.Printf("   • Morning Rush: Scaled up to 25 connections (from 10)\n")
	fmt.Printf("   • Midday Steady: Maintained 15 connections\n")
	fmt.Printf("   • Afternoon Peak: Scaled up to 30 connections (maximum)\n")
	fmt.Printf("   • Evening Decline: Scaled down to 12 connections\n")
	fmt.Printf("   • Night Maintenance: Scaled down to 5 connections (minimum)\n")

	// Performance summary
	fmt.Println("\n📊 24-Hour Performance Summary:")
	fmt.Printf("   Average latency: %v\n", 52*time.Millisecond)
	fmt.Printf("   Peak latency: %v\n", 180*time.Millisecond)
	fmt.Printf("   Total queries processed: %d\n", 145750)
	fmt.Printf("   Success rate: %.3f%%\n", 99.87)
	fmt.Printf("   Connection pool efficiency: %.2f%%\n", 94.3)
	fmt.Printf("   Scaling events: %d up, %d down\n", 15, 18)

	fmt.Println("✅ Real-world load simulation completed")
}

// simulateLoadPattern simulates a specific load pattern
func simulateLoadPattern(concurrency int, duration time.Duration, qps float64, logger *zap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var wg sync.WaitGroup
	queriesPerWorker := int(qps * duration.Seconds() / float64(concurrency))

	fmt.Printf("   🔄 Starting %d workers, %d queries each\n", concurrency, queriesPerWorker)

	// Start workers
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < queriesPerWorker; j++ {
				select {
				case <-ctx.Done():
					return
				default:
					// Simulate query execution
					queryDuration := time.Duration(secureIntn(100)+20) * time.Millisecond
					time.Sleep(queryDuration)

					// Occasional failure simulation
					if secureFloat64() < 0.001 { // 0.1% failure rate
						logger.Debug("Simulated query failure",
							zap.Int("worker", workerID),
							zap.Int("query", j),
						)
					}
				}
			}
		}(i)
	}

	// Wait for all workers to complete
	wg.Wait()
	fmt.Printf("   ✅ Load pattern completed\n")
}

// configurationComparisonExample compares different pool configurations
func configurationComparisonExample(logger *zap.Logger) {
	fmt.Println("Comparing different pool configurations...")

	configurations := []struct {
		name    string
		config  postgres.AdaptivePoolConfig
		useCase string
		pros    []string
		cons    []string
	}{
		{
			name: "Conservative",
			config: postgres.AdaptivePoolConfig{
				MinConnections:     2,
				MaxConnections:     10,
				ScaleUpThreshold:   0.8,
				ScaleDownThreshold: 0.2,
				ScaleUpFactor:      1.2,
				ScaleDownFactor:    0.9,
			},
			useCase: "Low-traffic applications with predictable load",
			pros:    []string{"Low resource usage", "Stable performance", "Cost-effective"},
			cons:    []string{"May not handle spikes well", "Slower scaling response"},
		},
		{
			name: "Balanced",
			config: postgres.AdaptivePoolConfig{
				MinConnections:     5,
				MaxConnections:     50,
				ScaleUpThreshold:   0.7,
				ScaleDownThreshold: 0.3,
				ScaleUpFactor:      1.5,
				ScaleDownFactor:    0.8,
			},
			useCase: "Most production applications with moderate variability",
			pros:    []string{"Good balance", "Handles spikes well", "Responsive scaling"},
			cons:    []string{"Moderate resource usage", "May overprovision slightly"},
		},
		{
			name: "Aggressive",
			config: postgres.AdaptivePoolConfig{
				MinConnections:     10,
				MaxConnections:     100,
				ScaleUpThreshold:   0.6,
				ScaleDownThreshold: 0.4,
				ScaleUpFactor:      2.0,
				ScaleDownFactor:    0.7,
			},
			useCase: "High-traffic applications with unpredictable spikes",
			pros:    []string{"Excellent spike handling", "Very responsive", "High throughput"},
			cons:    []string{"Higher resource usage", "May over-scale"},
		},
	}

	fmt.Println("📋 Configuration Comparison:")

	for i, cfg := range configurations {
		fmt.Printf("\n%d. %s Configuration\n", i+1, cfg.name)
		fmt.Printf("   Use case: %s\n", cfg.useCase)
		fmt.Printf(
			"   Min/Max connections: %d/%d\n",
			cfg.config.MinConnections,
			cfg.config.MaxConnections,
		)
		fmt.Printf("   Scale thresholds: %.1f%% up, %.1f%% down\n",
			cfg.config.ScaleUpThreshold*100, cfg.config.ScaleDownThreshold*100)
		fmt.Printf("   Scale factors: %.1fx up, %.1fx down\n",
			cfg.config.ScaleUpFactor, cfg.config.ScaleDownFactor)

		fmt.Printf("   Pros:\n")
		for _, pro := range cfg.pros {
			fmt.Printf("     ✅ %s\n", pro)
		}

		fmt.Printf("   Cons:\n")
		for _, con := range cfg.cons {
			fmt.Printf("     ⚠️  %s\n", con)
		}
	}

	// Recommendations
	fmt.Println("\n💡 Configuration Selection Guidelines:")
	fmt.Printf("   • Conservative: Choose for cost-sensitive, low-traffic apps\n")
	fmt.Printf("   • Balanced: Best for most production workloads\n")
	fmt.Printf("   • Aggressive: Use for high-traffic, latency-sensitive apps\n")
	fmt.Printf("   • Always start with balanced and adjust based on metrics\n")
	fmt.Printf("   • Monitor scaling events and adjust thresholds accordingly\n")

	// Performance comparison simulation
	fmt.Println("\n📊 Simulated Performance Under Load:")
	scenarios := []string{"Light Load", "Medium Load", "Heavy Load", "Spike Load"}

	for _, scenario := range scenarios {
		fmt.Printf("\n   %s:\n", scenario)
		for _, cfg := range configurations {
			var latency time.Duration
			var connections int

			switch scenario {
			case "Light Load":
				latency = 20 * time.Millisecond
				connections = cfg.config.MinConnections
			case "Medium Load":
				latency = 35 * time.Millisecond
				connections = (cfg.config.MinConnections + cfg.config.MaxConnections) / 3
			case "Heavy Load":
				latency = 55 * time.Millisecond
				connections = (cfg.config.MinConnections + cfg.config.MaxConnections) / 2
			case "Spike Load":
				latency = 85 * time.Millisecond
				connections = int(float64(cfg.config.MaxConnections) * 0.8)
			}

			fmt.Printf("     %s: %v latency, %d connections\n",
				cfg.name, latency, connections)
		}
	}

	fmt.Println("✅ Configuration comparison completed")
}

// Secure random number generator helpers

// secureFloat64 generates a secure random float64 between 0.0 and 1.0
func secureFloat64() float64 {
	var buf [8]byte
	_, err := rand.Read(buf[:])
	if err != nil {
		// Fallback to timestamp-based seed for demo code
		return float64(time.Now().UnixNano()%1000) / 1000.0
	}
	// Convert to float64 between 0.0 and 1.0
	return float64(binary.BigEndian.Uint64(buf[:])) / float64(^uint64(0))
}

// secureIntn generates a secure random integer between 0 and n (exclusive)
func secureIntn(n int) int {
	if n <= 0 {
		return 0
	}
	var buf [4]byte
	_, err := rand.Read(buf[:])
	if err != nil {
		// Fallback to timestamp-based value for demo code
		return int(time.Now().UnixNano()) % n
	}
	return int(binary.BigEndian.Uint32(buf[:])) % n
}
