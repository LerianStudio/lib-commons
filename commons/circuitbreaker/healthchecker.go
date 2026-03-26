package circuitbreaker

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/runtime"
)

var (
	// ErrNilManager is returned when a nil manager is passed to NewHealthCheckerWithValidation.
	ErrNilManager = errors.New("circuitbreaker: manager must not be nil")
	// ErrInvalidHealthCheckInterval indicates that the health check interval must be positive
	ErrInvalidHealthCheckInterval = errors.New("circuitbreaker: health check interval must be positive")
	// ErrInvalidHealthCheckTimeout indicates that the health check timeout must be positive
	ErrInvalidHealthCheckTimeout = errors.New("circuitbreaker: health check timeout must be positive")
)

// healthChecker performs periodic health checks and manages circuit breaker recovery
type healthChecker struct {
	manager        Manager
	services       map[string]HealthCheckFunc
	interval       time.Duration
	checkTimeout   time.Duration // Timeout for individual health check operations
	logger         log.Logger
	stopChan       chan struct{}
	immediateCheck chan string // Channel to trigger immediate health check for a service
	wg             sync.WaitGroup
	mu             sync.RWMutex
	stopOnce       sync.Once
	started        bool
}

// NewHealthCheckerWithValidation creates a new health checker with validation.
// Returns an error if interval or checkTimeout are not positive.
// interval: how often to run health checks
// checkTimeout: timeout for each individual health check operation
func NewHealthCheckerWithValidation(manager Manager, interval, checkTimeout time.Duration, logger log.Logger) (HealthChecker, error) {
	if manager == nil {
		return nil, ErrNilManager
	}

	if logger == nil {
		return nil, ErrNilLogger
	}

	if interval <= 0 {
		return nil, ErrInvalidHealthCheckInterval
	}

	if checkTimeout <= 0 {
		return nil, ErrInvalidHealthCheckTimeout
	}

	return &healthChecker{
		manager:        manager,
		services:       make(map[string]HealthCheckFunc),
		interval:       interval,
		checkTimeout:   checkTimeout,
		logger:         logger,
		stopChan:       make(chan struct{}),
		immediateCheck: make(chan string, 10),
	}, nil
}

// Register adds a service to health check
func (hc *healthChecker) Register(serviceName string, healthCheckFn HealthCheckFunc) {
	if healthCheckFn == nil {
		hc.logger.Log(context.Background(), log.LevelWarn, "attempted to register nil health check function", log.String("service", serviceName))
		return
	}

	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.services[serviceName] = healthCheckFn
	hc.logger.Log(context.Background(), log.LevelInfo, "registered health check for service", log.String("service", serviceName))
}

// Start begins the health check loop
func (hc *healthChecker) Start() {
	hc.mu.Lock()

	if hc.started {
		hc.mu.Unlock()
		hc.logger.Log(context.Background(), log.LevelWarn, "health checker already started, ignoring duplicate Start() call")

		return
	}

	hc.started = true
	hc.wg.Add(1)
	hc.mu.Unlock()

	runtime.SafeGoWithContextAndComponent(
		context.Background(),
		hc.logger,
		"circuitbreaker",
		"health_check_loop",
		runtime.KeepRunning,
		func(ctx context.Context) {
			hc.healthCheckLoop(ctx)
		},
	)

	hc.logger.Log(context.Background(), log.LevelInfo, "health checker started", log.String("interval", hc.interval.String()))
}

// Stop gracefully stops the health checker
func (hc *healthChecker) Stop() {
	hc.stopOnce.Do(func() {
		close(hc.stopChan)
	})
	hc.wg.Wait()
	hc.logger.Log(context.Background(), log.LevelInfo, "Health checker stopped")
}

func (hc *healthChecker) healthCheckLoop(ctx context.Context) {
	defer hc.wg.Done()

	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	// By entering the select loop immediately, the health checker is responsive
	// to immediate checks from the moment it starts.
	for {
		select {
		case <-ticker.C:
			hc.performHealthChecks()
		case serviceName := <-hc.immediateCheck:
			// Immediate health check for a specific service
			hc.logger.Log(context.Background(), log.LevelDebug, "triggering immediate health check", log.String("service", serviceName))
			hc.checkServiceHealth(serviceName)
		case <-ctx.Done():
			return
		case <-hc.stopChan:
			return
		}
	}
}

func (hc *healthChecker) performHealthChecks() {
	services := hc.snapshotServices()

	hc.logger.Log(context.Background(), log.LevelDebug, "performing health checks on registered services")

	unhealthyCount := 0
	recoveredCount := 0

	for serviceName, healthCheckFn := range services {
		// Skip if circuit breaker is healthy
		if hc.manager.IsHealthy(serviceName) {
			continue
		}

		unhealthyCount++

		if hc.attemptServiceRecovery(serviceName, healthCheckFn) {
			recoveredCount++
		}
	}

	if unhealthyCount > 0 {
		hc.logger.Log(context.Background(), log.LevelInfo, "health check complete", log.Int("unhealthy", unhealthyCount), log.Int("recovered", recoveredCount))
	} else {
		hc.logger.Log(context.Background(), log.LevelDebug, "all services healthy")
	}
}

func (hc *healthChecker) snapshotServices() map[string]HealthCheckFunc {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	services := make(map[string]HealthCheckFunc, len(hc.services))
	maps.Copy(services, hc.services)

	return services
}

func (hc *healthChecker) attemptServiceRecovery(serviceName string, healthCheckFn HealthCheckFunc) bool {
	hc.logger.Log(context.Background(), log.LevelInfo, "attempting to heal service", log.String("service", serviceName), log.String("reason", "circuit breaker open"))

	ctx, cancel := context.WithTimeout(context.Background(), hc.checkTimeout)
	err := healthCheckFn(ctx)

	cancel()

	if err == nil {
		hc.logger.Log(context.Background(), log.LevelInfo, "service recovered, resetting circuit breaker", log.String("service", serviceName))
		hc.manager.Reset(serviceName)

		return true
	}

	hc.logger.Log(context.Background(), log.LevelWarn, "service still unhealthy", log.String("service", serviceName), log.Err(err), log.String("retry_in", hc.interval.String()))

	return false
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

// OnStateChange implements StateChangeListener interface.
// This is called when a circuit breaker changes state.
// The provided context carries a deadline; the health checker uses it for logging
// but schedules checks independently.
func (hc *healthChecker) OnStateChange(_ context.Context, serviceName string, from State, to State) {
	hc.logger.Log(context.Background(), log.LevelDebug, "health checker notified of state change", log.String("service", serviceName), log.String("from", string(from)), log.String("to", string(to)))

	// If circuit just opened, trigger immediate health check
	if to == StateOpen {
		hc.logger.Log(context.Background(), log.LevelInfo, "circuit breaker opened, scheduling immediate health check", log.String("service", serviceName))

		// Non-blocking send to avoid deadlock
		select {
		case hc.immediateCheck <- serviceName:
			hc.logger.Log(context.Background(), log.LevelDebug, "immediate health check scheduled", log.String("service", serviceName))
		default:
			hc.logger.Log(context.Background(), log.LevelWarn, "immediate health check channel full, will check on next interval", log.String("service", serviceName))
		}
	}
}

// checkServiceHealth performs a health check on a specific service
func (hc *healthChecker) checkServiceHealth(serviceName string) {
	hc.mu.RLock()
	healthCheckFn, exists := hc.services[serviceName]
	hc.mu.RUnlock()

	if !exists {
		hc.logger.Log(context.Background(), log.LevelWarn, "no health check function registered", log.String("service", serviceName))
		return
	}

	// Skip if circuit breaker is already healthy
	if hc.manager.IsHealthy(serviceName) {
		hc.logger.Log(context.Background(), log.LevelDebug, "service already healthy, skipping check", log.String("service", serviceName))
		return
	}

	hc.attemptServiceRecovery(serviceName, healthCheckFn)
}
