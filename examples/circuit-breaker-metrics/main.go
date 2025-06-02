// Package main demonstrates circuit breaker Prometheus metrics integration
// with comprehensive monitoring, alerting, and observability patterns.
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/LerianStudio/lib-commons/commons/circuitbreaker"
	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	fmt.Println("Circuit Breaker Prometheus Metrics Demo")
	fmt.Println("=======================================")

	// Example 1: Basic metrics setup
	fmt.Println("\n1. Basic Metrics Setup")
	basicMetricsExample()

	// Example 2: Advanced metrics with custom labels
	fmt.Println("\n2. Advanced Metrics with Custom Labels")
	advancedMetricsExample()

	// Example 3: HTTP service with metrics endpoint
	fmt.Println("\n3. HTTP Service with Metrics Endpoint")
	httpServiceExample()

	// Example 4: Multi-service metrics collection
	fmt.Println("\n4. Multi-Service Metrics Collection")
	multiServiceExample()

	// Example 5: Metrics-based alerting patterns
	fmt.Println("\n5. Metrics-Based Alerting Patterns")
	alertingPatternsExample()

	// Example 6: Real-time metrics monitoring
	fmt.Println("\n6. Real-Time Metrics Monitoring")
	realTimeMonitoringExample()
}

// basicMetricsExample demonstrates basic Prometheus metrics setup
func basicMetricsExample() {
	fmt.Println("Setting up basic circuit breaker with Prometheus metrics...")

	// Create custom Prometheus registry
	registry := prometheus.NewRegistry()

	// Create metrics exporter
	metricsConfig := circuitbreaker.MetricsConfig{
		Namespace: "myapp",
		Subsystem: "payments",
		Enabled:   true,
		Registry:  registry,
		Labels:    map[string]string{"environment": "production"},
	}

	exporter := circuitbreaker.NewMetricsExporter(metricsConfig)

	// Create circuit breaker (in real implementation, this would be integrated)
	cb := circuitbreaker.New("payment-service",
		circuitbreaker.WithThreshold(3),
		circuitbreaker.WithTimeout(5*time.Second),
	)

	// Simulate operations with metrics recording
	fmt.Println("Simulating operations with metrics...")

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		start := time.Now()

		// Record request
		exporter.RecordRequest("payment-service", "payments", map[string]string{
			"method": "credit_card",
			"region": "us-east-1",
		})

		// Simulate operation
		err := cb.Execute(func() error {
			// Simulate some failures
			if secureFloat32() < 0.3 {
				return fmt.Errorf("payment processing failed")
			}

			// Simulate processing time
			time.Sleep(time.Duration(secureIntn(100)) * time.Millisecond)
			return nil
		})

		duration := time.Since(start)

		// Record result
		if err != nil {
			exporter.RecordFailure(
				"payment-service",
				"payments",
				duration,
				"processing_error",
				map[string]string{
					"method": "credit_card",
					"region": "us-east-1",
				},
			)
			fmt.Printf("❌ Operation %d failed: %v\n", i+1, err)
		} else {
			exporter.RecordSuccess("payment-service", "payments", duration, map[string]string{
				"method": "credit_card",
				"region": "us-east-1",
			})
			fmt.Printf("✅ Operation %d succeeded in %v\n", i+1, duration)
		}

		// Update state metrics
		exporter.UpdateCurrentState("payment-service", "payments", cb.State(), map[string]string{
			"method": "credit_card",
			"region": "us-east-1",
		})

		time.Sleep(100 * time.Millisecond)
	}

	fmt.Printf("Circuit breaker state: %s\n", cb.State())
	fmt.Println("✅ Basic metrics example completed")
}

// advancedMetricsExample demonstrates advanced metrics with custom labels and multiple circuit breakers
func advancedMetricsExample() {
	fmt.Println("Setting up advanced metrics with multiple circuit breakers...")

	// Create metrics exporter with custom configuration
	config := circuitbreaker.MetricsConfig{
		Namespace: "ecommerce",
		Subsystem: "services",
		Enabled:   true,
		Registry:  prometheus.DefaultRegisterer,
		Labels: map[string]string{
			"version":    "v2.1.0",
			"datacenter": "us-west-2",
			"team":       "platform",
		},
	}

	exporter := circuitbreaker.NewMetricsExporter(config)

	// Create multiple circuit breakers for different services
	services := map[string]*circuitbreaker.CircuitBreaker{
		"user-service": circuitbreaker.New("user-service", circuitbreaker.WithThreshold(5)),
		"inventory-service": circuitbreaker.New(
			"inventory-service",
			circuitbreaker.WithThreshold(3),
		),
		"payment-service": circuitbreaker.New("payment-service", circuitbreaker.WithThreshold(2)),
		"shipping-service": circuitbreaker.New(
			"shipping-service",
			circuitbreaker.WithThreshold(4),
		),
	}

	// Simulate operations across multiple services
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		fmt.Printf("\n--- Round %d ---\n", round+1)

		for serviceName, cb := range services {
			labels := map[string]string{
				"service_type": getServiceType(serviceName),
				"priority":     getPriority(serviceName),
			}

			// Record metrics for each operation
			start := time.Now()
			exporter.RecordRequest(serviceName, "ecommerce", labels)

			err := cb.Execute(func() error {
				// Different failure rates for different services
				failureRate := getFailureRate(serviceName)
				if secureFloat32() < failureRate {
					return fmt.Errorf("%s operation failed", serviceName)
				}

				// Different processing times
				processingTime := getProcessingTime(serviceName)
				time.Sleep(processingTime)
				return nil
			})

			duration := time.Since(start)

			if err != nil {
				exporter.RecordFailure(serviceName, "ecommerce", duration, "service_error", labels)
				fmt.Printf("❌ %s: %v\n", serviceName, err)
			} else {
				exporter.RecordSuccess(serviceName, "ecommerce", duration, labels)
				fmt.Printf("✅ %s: success in %v\n", serviceName, duration)
			}

			// Update state and consecutive failures
			exporter.UpdateCurrentState(serviceName, "ecommerce", cb.State(), labels)
			metrics := cb.Metrics()
			exporter.UpdateConsecutiveFailures(
				serviceName,
				"ecommerce",
				metrics.ConsecutiveFailures,
				labels,
			)

			time.Sleep(50 * time.Millisecond)
		}
	}

	// Print final states
	fmt.Println("\n📊 Final Circuit Breaker States:")
	for serviceName, cb := range services {
		state := cb.State()
		metrics := cb.Metrics()
		fmt.Printf("   %s: %s (failures: %d, successes: %d)\n",
			serviceName, state, metrics.Failures, metrics.Successes)
	}

	fmt.Println("✅ Advanced metrics example completed")
}

// httpServiceExample demonstrates HTTP service with metrics endpoint
func httpServiceExample() {
	fmt.Println("Starting HTTP service with Prometheus metrics endpoint...")

	// Create Fiber app
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	// Create circuit breaker and metrics
	registry := prometheus.NewRegistry()
	exporter := circuitbreaker.NewMetricsExporter(circuitbreaker.MetricsConfig{
		Namespace: "webservice",
		Subsystem: "api",
		Enabled:   true,
		Registry:  registry,
	})

	cb := circuitbreaker.New("external-api",
		circuitbreaker.WithThreshold(3),
		circuitbreaker.WithTimeout(2*time.Second),
	)

	// API endpoint that uses circuit breaker
	app.Get("/api/data", func(c *fiber.Ctx) error {
		start := time.Now()
		labels := map[string]string{
			"endpoint": "/api/data",
			"method":   "GET",
		}

		exporter.RecordRequest("external-api", "webservice", labels)

		var result map[string]interface{}
		err := cb.ExecuteWithContext(c.Context(), func() error {
			// Simulate external API call
			processingTime := time.Duration(secureIntn(200)) * time.Millisecond
			time.Sleep(processingTime)

			// Simulate failures
			if secureFloat32() < 0.25 {
				return fmt.Errorf("external API unavailable")
			}

			result = map[string]interface{}{
				"data":      "sample data",
				"timestamp": time.Now().Unix(),
				"status":    "success",
			}
			return nil
		})

		duration := time.Since(start)

		if err != nil {
			exporter.RecordFailure(
				"external-api",
				"webservice",
				duration,
				"external_api_error",
				labels,
			)
			return c.Status(503).JSON(fiber.Map{
				"error":   "Service temporarily unavailable",
				"details": err.Error(),
			})
		}

		exporter.RecordSuccess("external-api", "webservice", duration, labels)
		exporter.UpdateCurrentState("external-api", "webservice", cb.State(), labels)

		return c.JSON(result)
	})

	// Health check endpoint
	app.Get("/health", func(c *fiber.Ctx) error {
		state := cb.State()
		metrics := cb.Metrics()

		return c.JSON(fiber.Map{
			"status":               "healthy",
			"circuit_breaker":      state.String(),
			"requests":             metrics.Requests,
			"successes":            metrics.Successes,
			"failures":             metrics.Failures,
			"consecutive_failures": metrics.ConsecutiveFailures,
		})
	})

	// Prometheus metrics endpoint
	app.Get("/metrics", func(c *fiber.Ctx) error {
		handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
		handler.ServeHTTP(c.Response().BodyWriter(), c.Request())
		return nil
	})

	// Start server in background
	go func() {
		fmt.Println("🚀 HTTP service started on :8080")
		fmt.Println("   • API endpoint: http://localhost:8080/api/data")
		fmt.Println("   • Health check: http://localhost:8080/health")
		fmt.Println("   • Metrics: http://localhost:8080/metrics")

		if err := app.Listen(":8080"); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	// Simulate some requests
	time.Sleep(1 * time.Second)
	fmt.Println("\n🧪 Simulating API requests...")

	client := &http.Client{Timeout: 5 * time.Second}
	for i := 0; i < 15; i++ {
		resp, err := client.Get("http://localhost:8080/api/data")
		if err != nil {
			fmt.Printf("❌ Request %d: %v\n", i+1, err)
			continue
		}

		fmt.Printf("✅ Request %d: Status %d\n", i+1, resp.StatusCode)
		_ = resp.Body.Close()

		time.Sleep(200 * time.Millisecond)
	}

	// Show metrics
	fmt.Println("\n📊 Checking metrics endpoint...")
	resp, err := client.Get("http://localhost:8080/metrics")
	if err != nil {
		fmt.Printf("❌ Failed to get metrics: %v\n", err)
	} else {
		fmt.Printf("✅ Metrics endpoint accessible (Status: %d)\n", resp.StatusCode)
		_ = resp.Body.Close()
	}

	// Cleanup
	_ = app.Shutdown() // Ignoring error for demo code
	fmt.Println("✅ HTTP service example completed")
}

// multiServiceExample demonstrates metrics collection across multiple services
func multiServiceExample() {
	fmt.Println("Setting up multi-service metrics collection...")

	// Create global metrics collector
	collector := circuitbreaker.NewMetricsCollector()

	// Create exporters for different services
	services := []string{"user-service", "order-service", "payment-service", "notification-service"}

	for _, service := range services {
		config := circuitbreaker.MetricsConfig{
			Namespace: "microservices",
			Subsystem: service,
			Enabled:   true,
			Registry:  prometheus.NewRegistry(),
			Labels: map[string]string{
				"service": service,
				"version": "v1.0.0",
			},
		}

		exporter := circuitbreaker.NewMetricsExporter(config)
		collector.RegisterExporter(service, exporter)
	}

	// Simulate operations across services
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		for _, service := range services {
			// Simulate different patterns for each service
			simulateServiceOperations(service, collector, ctx)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Get metrics snapshot
	snapshot, err := collector.GetSnapshot(ctx)
	if err != nil {
		fmt.Printf("❌ Failed to get metrics snapshot: %v\n", err)
		return
	}

	// Display summary
	fmt.Println("\n📊 Multi-Service Metrics Summary:")
	fmt.Printf("   Total Circuit Breakers: %d\n", snapshot.Summary.TotalCircuitBreakers)
	fmt.Printf("   Open Circuit Breakers: %d\n", snapshot.Summary.OpenCircuitBreakers)
	fmt.Printf("   Total Requests: %d\n", snapshot.Summary.TotalRequests)
	fmt.Printf("   Overall Success Rate: %.2f%%\n", snapshot.Summary.OverallSuccessRate)
	fmt.Printf("   Overall Failure Rate: %.2f%%\n", snapshot.Summary.OverallFailureRate)

	fmt.Println("✅ Multi-service metrics example completed")
}

// alertingPatternsExample demonstrates metrics-based alerting patterns
func alertingPatternsExample() {
	fmt.Println("Demonstrating metrics-based alerting patterns...")

	// Create circuit breaker with metrics
	cb := circuitbreaker.New("critical-service",
		circuitbreaker.WithThreshold(3),
		circuitbreaker.WithTimeout(5*time.Second),
	)

	exporter := circuitbreaker.NewMetricsExporter(circuitbreaker.DefaultMetricsConfig())

	// Define alerting thresholds
	alertThresholds := map[string]float64{
		"failure_rate":         10.0, // Alert if failure rate > 10%
		"consecutive_failures": 5.0,  // Alert if consecutive failures > 5
		"response_time_p95":    2.0,  // Alert if P95 response time > 2s
	}

	fmt.Println("\n🚨 Alerting Thresholds:")
	for metric, threshold := range alertThresholds {
		fmt.Printf("   • %s: %.1f\n", metric, threshold)
	}

	// Simulate operations that trigger alerts
	ctx := context.Background()
	alerts := make([]string, 0)

	for i := 0; i < 20; i++ {
		start := time.Now()
		labels := map[string]string{"operation": "critical_task"}

		exporter.RecordRequest("critical-service", "production", labels)

		// Gradually increase failure rate
		failureRate := float32(i) / 20.0
		err := cb.Execute(func() error {
			// Simulate increasing response times
			processingTime := time.Duration(float64(i*50)) * time.Millisecond
			time.Sleep(processingTime)

			if secureFloat32() < failureRate {
				return fmt.Errorf("critical service error")
			}
			return nil
		})

		duration := time.Since(start)

		if err != nil {
			exporter.RecordFailure(
				"critical-service",
				"production",
				duration,
				"service_error",
				labels,
			)
		} else {
			exporter.RecordSuccess("critical-service", "production", duration, labels)
		}

		// Check alerting conditions
		metrics := cb.Metrics()
		newAlerts := checkAlertConditions(metrics, alertThresholds)
		alerts = append(alerts, newAlerts...)

		exporter.UpdateCurrentState("critical-service", "production", cb.State(), labels)
		exporter.UpdateConsecutiveFailures(
			"critical-service",
			"production",
			metrics.ConsecutiveFailures,
			labels,
		)

		time.Sleep(100 * time.Millisecond)
	}

	// Display triggered alerts
	fmt.Printf("\n🚨 Triggered Alerts (%d):\n", len(alerts))
	for i, alert := range alerts {
		fmt.Printf("   %d. %s\n", i+1, alert)
	}

	fmt.Println("✅ Alerting patterns example completed")
}

// realTimeMonitoringExample demonstrates real-time metrics monitoring
func realTimeMonitoringExample() {
	fmt.Println("Starting real-time metrics monitoring...")

	// Create circuit breaker and metrics
	cb := circuitbreaker.New("real-time-service",
		circuitbreaker.WithThreshold(4),
		circuitbreaker.WithTimeout(3*time.Second),
	)

	exporter := circuitbreaker.NewMetricsExporter(circuitbreaker.DefaultMetricsConfig())

	// Create monitoring channels
	done := make(chan bool)

	// Start real-time monitoring goroutine
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				displayRealTimeMetrics(cb, "real-time-service")
			case <-done:
				return
			}
		}
	}()

	// Simulate varying load patterns
	fmt.Println("🔄 Simulating varying load patterns...")

	patterns := []struct {
		name        string
		duration    time.Duration
		requestRate time.Duration
		failureRate float32
	}{
		{"Normal Load", 6 * time.Second, 300 * time.Millisecond, 0.1},
		{"High Load", 4 * time.Second, 100 * time.Millisecond, 0.3},
		{"Critical Load", 6 * time.Second, 50 * time.Millisecond, 0.5},
		{"Recovery", 4 * time.Second, 500 * time.Millisecond, 0.05},
	}

	ctx := context.Background()
	for _, pattern := range patterns {
		fmt.Printf(
			"\n📊 Pattern: %s (Failure Rate: %.1f%%)\n",
			pattern.name,
			pattern.failureRate*100,
		)

		endTime := time.Now().Add(pattern.duration)
		for time.Now().Before(endTime) {
			start := time.Now()
			labels := map[string]string{"pattern": pattern.name}

			exporter.RecordRequest("real-time-service", "monitoring", labels)

			err := cb.Execute(func() error {
				if secureFloat32() < pattern.failureRate {
					return fmt.Errorf("service error during %s", pattern.name)
				}
				time.Sleep(time.Duration(secureIntn(100)) * time.Millisecond)
				return nil
			})

			duration := time.Since(start)

			if err != nil {
				exporter.RecordFailure(
					"real-time-service",
					"monitoring",
					duration,
					"pattern_error",
					labels,
				)
			} else {
				exporter.RecordSuccess("real-time-service", "monitoring", duration, labels)
			}

			exporter.UpdateCurrentState("real-time-service", "monitoring", cb.State(), labels)

			time.Sleep(pattern.requestRate)
		}
	}

	// Stop monitoring
	done <- true
	fmt.Println("\n✅ Real-time monitoring example completed")
}

// Helper functions

func getServiceType(serviceName string) string {
	types := map[string]string{
		"user-service":      "core",
		"inventory-service": "business",
		"payment-service":   "financial",
		"shipping-service":  "logistics",
	}
	return types[serviceName]
}

func getPriority(serviceName string) string {
	priorities := map[string]string{
		"user-service":      "high",
		"inventory-service": "medium",
		"payment-service":   "critical",
		"shipping-service":  "medium",
	}
	return priorities[serviceName]
}

func getFailureRate(serviceName string) float32 {
	rates := map[string]float32{
		"user-service":      0.1,
		"inventory-service": 0.2,
		"payment-service":   0.15,
		"shipping-service":  0.25,
	}
	return rates[serviceName]
}

func getProcessingTime(serviceName string) time.Duration {
	times := map[string]time.Duration{
		"user-service":      50 * time.Millisecond,
		"inventory-service": 100 * time.Millisecond,
		"payment-service":   200 * time.Millisecond,
		"shipping-service":  150 * time.Millisecond,
	}
	return times[serviceName]
}

func simulateServiceOperations(
	service string,
	collector *circuitbreaker.MetricsCollector,
	ctx context.Context,
) {
	// This would simulate operations for each service
	// For demo purposes, we'll just create some placeholder activity
	fmt.Printf("   📡 Simulating operations for %s\n", service)
}

func checkAlertConditions(metrics circuitbreaker.Metrics, thresholds map[string]float64) []string {
	alerts := make([]string, 0)

	// Check failure rate
	if metrics.Requests > 0 {
		failureRate := float64(metrics.Failures) / float64(metrics.Requests) * 100
		if failureRate > thresholds["failure_rate"] {
			alerts = append(alerts, fmt.Sprintf("High failure rate: %.2f%% (threshold: %.1f%%)",
				failureRate, thresholds["failure_rate"]))
		}
	}

	// Check consecutive failures
	if float64(metrics.ConsecutiveFailures) > thresholds["consecutive_failures"] {
		alerts = append(alerts, fmt.Sprintf("High consecutive failures: %d (threshold: %.0f)",
			metrics.ConsecutiveFailures, thresholds["consecutive_failures"]))
	}

	return alerts
}

func displayRealTimeMetrics(cb *circuitbreaker.CircuitBreaker, serviceName string) {
	metrics := cb.Metrics()
	state := cb.State()

	var stateIcon string
	switch state {
	case circuitbreaker.StateClosed:
		stateIcon = "🟢"
	case circuitbreaker.StateOpen:
		stateIcon = "🔴"
	case circuitbreaker.StateHalfOpen:
		stateIcon = "🟡"
	}

	var successRate float64
	if metrics.Requests > 0 {
		successRate = float64(metrics.Successes) / float64(metrics.Requests) * 100
	}

	fmt.Printf("📊 [%s] %s %s | Requests: %d | Success: %.1f%% | Failures: %d | State: %s\n",
		time.Now().Format("15:04:05"),
		stateIcon,
		serviceName,
		metrics.Requests,
		successRate,
		metrics.ConsecutiveFailures,
		state,
	)
}

// Secure random number generator helpers

// secureFloat32 generates a secure random float32 between 0.0 and 1.0
func secureFloat32() float32 {
	var buf [4]byte
	_, err := rand.Read(buf[:])
	if err != nil {
		// Fallback to timestamp-based seed for demo code
		return float32(time.Now().UnixNano()%1000) / 1000.0
	}
	// Convert to float32 between 0.0 and 1.0
	return float32(binary.BigEndian.Uint32(buf[:])) / float32(^uint32(0))
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
