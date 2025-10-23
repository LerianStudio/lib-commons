package circuitbreaker

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
)

// healthChecker performs periodic health checks and manages circuit breaker recovery
type healthChecker struct {
	manager  Manager
	services map[string]HealthCheckFunc
	interval time.Duration
	logger   log.Logger
	stopChan chan struct{}
	wg       sync.WaitGroup
	mu       sync.RWMutex
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(manager Manager, interval time.Duration, logger log.Logger) HealthChecker {
	return &healthChecker{
		manager:  manager,
		services: make(map[string]HealthCheckFunc),
		interval: interval,
		logger:   logger,
		stopChan: make(chan struct{}),
	}
}

// Register adds a service to health check
func (hc *healthChecker) Register(serviceName string, healthCheckFn HealthCheckFunc) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.services[serviceName] = healthCheckFn
	hc.logger.Infof("Registered health check for service: %s", serviceName)
}

// Start begins the health check loop
func (hc *healthChecker) Start() {
	hc.wg.Add(1)

	go hc.healthCheckLoop()

	hc.logger.Infof("Health checker started - checking services every %v", hc.interval)
}

// Stop gracefully stops the health checker
func (hc *healthChecker) Stop() {
	close(hc.stopChan)
	hc.wg.Wait()
	hc.logger.Info("Health checker stopped")
}

func (hc *healthChecker) healthCheckLoop() {
	defer hc.wg.Done()

	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	// Initial check after short delay
	time.Sleep(5 * time.Second)
	hc.performHealthChecks()

	for {
		select {
		case <-ticker.C:
			hc.performHealthChecks()
		case <-hc.stopChan:
			return
		}
	}
}

func (hc *healthChecker) performHealthChecks() {
	hc.mu.RLock()
	// Create snapshot to avoid holding lock during checks
	services := make(map[string]HealthCheckFunc, len(hc.services))
	maps.Copy(services, hc.services)

	hc.mu.RUnlock()

	hc.logger.Debug("Performing health checks on registered services...")

	unhealthyCount := 0
	recoveredCount := 0

	for serviceName, healthCheckFn := range services {
		// Skip if circuit breaker is healthy
		if hc.manager.IsHealthy(serviceName) {
			continue
		}

		unhealthyCount++
		hc.logger.Infof("Attempting to heal service: %s (circuit breaker is open)", serviceName)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := healthCheckFn(ctx)
		cancel()

		if err == nil {
			hc.logger.Infof("Service %s recovered - resetting circuit breaker", serviceName)
			hc.manager.Reset(serviceName)
			recoveredCount++
		} else {
			hc.logger.Warnf("Service %s still unhealthy: %v - will retry in %v", serviceName, err, hc.interval)
		}
	}

	if unhealthyCount > 0 {
		hc.logger.Infof("Health check complete: %d services needed healing, %d recovered", unhealthyCount, recoveredCount)
	} else {
		hc.logger.Debug("All services healthy")
	}
}

// GetHealthStatus returns the current health status of all services
func (hc *healthChecker) GetHealthStatus() map[string]string {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	status := make(map[string]string)

	for serviceName := range hc.services {
		cbState := hc.manager.GetState(serviceName)
		status[serviceName] = string(cbState)
	}

	return status
}
