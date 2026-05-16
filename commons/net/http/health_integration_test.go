//go:build integration

package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errSimulated is a sentinel error used to drive circuit breaker failures in tests.
var errSimulated = errors.New("simulated service failure")

// testConfig returns a circuit breaker Config with very short timeouts and
// low thresholds suitable for integration tests that need the breaker to trip
// quickly and recover within a bounded wall-clock time.
func testConfig() circuitbreaker.Config {
	return circuitbreaker.Config{
		MaxRequests:         1,
		Interval:            1 * time.Second,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}
}

// parseHealthResponse reads and decodes the JSON body from a health endpoint
// response. It returns the top-level map and the nested dependencies map.
func parseHealthResponse(t *testing.T, resp *http.Response) (result map[string]any, deps map[string]any) {
	t.Helper()

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err, "failed to decode health response body")

	raw, ok := result["dependencies"]
	require.True(t, ok, "expected 'dependencies' key in response")

	deps, ok = raw.(map[string]any)
	require.True(t, ok, "expected 'dependencies' to be a JSON object")

	return result, deps
}

// depStatus extracts a single dependency's status object from the dependencies map.
func depStatus(t *testing.T, deps map[string]any, name string) map[string]any {
	t.Helper()

	raw, ok := deps[name]
	require.True(t, ok, "expected dependency %q in response", name)

	status, ok := raw.(map[string]any)
	require.True(t, ok, "expected dependency %q to be a JSON object", name)

	return status
}

// tripCircuitBreaker drives enough failures through the manager's Execute path
// to move the circuit breaker for serviceName into the open state.
func tripCircuitBreaker(t *testing.T, mgr circuitbreaker.Manager, serviceName string, failures int) {
	t.Helper()

	for i := range failures {
		_, err := mgr.Execute(serviceName, func() (any, error) {
			return nil, errSimulated
		})
		// Early executions return the simulated error; once the breaker
		// trips the manager wraps the gobreaker open-state error.
		require.Error(t, err, "expected error on failure iteration %d", i)
	}

	state := mgr.GetState(serviceName)
	require.Equal(t, circuitbreaker.StateOpen, state,
		"circuit breaker should be open after %d consecutive failures", failures)
}

// ---------------------------------------------------------------------------
// Test 1: All dependencies healthy — circuit breaker in closed state.
// ---------------------------------------------------------------------------

func TestIntegration_Health_AllDependenciesHealthy(t *testing.T) {
	logger := log.NewNop()

	mgr, err := circuitbreaker.NewManager(logger)
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("postgres", circuitbreaker.DefaultConfig())
	require.NoError(t, err)

	// Drive one successful execution so the breaker has activity.
	_, err = mgr.Execute("postgres", func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	app := fiber.New()
	app.Get("/health", HealthWithDependencies(
		DependencyCheck{
			Name:           "database",
			CircuitBreaker: mgr,
			ServiceName:    "postgres",
		},
	))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result, deps := parseHealthResponse(t, resp)

	assert.Equal(t, "available", result["status"])

	dbStatus := depStatus(t, deps, "database")
	assert.Equal(t, true, dbStatus["healthy"])
	assert.Equal(t, "closed", dbStatus["circuit_breaker_state"])

	// Verify the counts reflect the successful execution.
	assert.GreaterOrEqual(t, dbStatus["total_successes"], float64(1),
		"should report at least 1 success")
}

// ---------------------------------------------------------------------------
// Test 2: Dependency unhealthy — circuit breaker tripped to open state.
// ---------------------------------------------------------------------------

func TestIntegration_Health_DependencyUnhealthy_CircuitOpen(t *testing.T) {
	logger := log.NewNop()
	cfg := testConfig()

	mgr, err := circuitbreaker.NewManager(logger)
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("redis", cfg)
	require.NoError(t, err)

	// Trip the breaker: cfg.ConsecutiveFailures == 2, so 2 failures suffice.
	tripCircuitBreaker(t, mgr, "redis", int(cfg.ConsecutiveFailures))

	app := fiber.New()
	app.Get("/health", HealthWithDependencies(
		DependencyCheck{
			Name:           "cache",
			CircuitBreaker: mgr,
			ServiceName:    "redis",
		},
	))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	result, deps := parseHealthResponse(t, resp)

	assert.Equal(t, "degraded", result["status"])

	cacheStatus := depStatus(t, deps, "cache")
	assert.Equal(t, false, cacheStatus["healthy"])
	assert.Equal(t, "open", cacheStatus["circuit_breaker_state"])

	// NOTE: gobreaker resets internal counters to zero when transitioning to
	// the open state. Because DependencyStatus uses `omitempty` on uint32
	// counter fields, zero-valued counters are omitted from the JSON.
	// We verify the breaker tripped by confirming state == "open" above.
}

// ---------------------------------------------------------------------------
// Test 3: Custom HealthCheck function (no circuit breaker).
// ---------------------------------------------------------------------------

func TestIntegration_Health_CustomHealthCheck(t *testing.T) {
	// Sub-test: healthy custom check → 200.
	t.Run("healthy", func(t *testing.T) {
		app := fiber.New()
		app.Get("/health", HealthWithDependencies(
			DependencyCheck{
				Name:        "external-api",
				HealthCheck: func() bool { return true },
			},
		))

		req := httptest.NewRequest(http.MethodGet, "/health", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)

		defer func() { require.NoError(t, resp.Body.Close()) }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		result, deps := parseHealthResponse(t, resp)
		assert.Equal(t, "available", result["status"])

		apiStatus := depStatus(t, deps, "external-api")
		assert.Equal(t, true, apiStatus["healthy"])

		// No circuit breaker configured — state field must be absent.
		_, hasCBState := apiStatus["circuit_breaker_state"]
		assert.False(t, hasCBState, "circuit_breaker_state should be omitted when no CB is configured")
	})

	// Sub-test: unhealthy custom check → 503.
	t.Run("unhealthy", func(t *testing.T) {
		app := fiber.New()
		app.Get("/health", HealthWithDependencies(
			DependencyCheck{
				Name:        "external-api",
				HealthCheck: func() bool { return false },
			},
		))

		req := httptest.NewRequest(http.MethodGet, "/health", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)

		defer func() { require.NoError(t, resp.Body.Close()) }()

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

		result, deps := parseHealthResponse(t, resp)
		assert.Equal(t, "degraded", result["status"])

		apiStatus := depStatus(t, deps, "external-api")
		assert.Equal(t, false, apiStatus["healthy"])
	})
}

// ---------------------------------------------------------------------------
// Test 4: AND semantics — both circuit breaker AND health check must pass.
// ---------------------------------------------------------------------------

func TestIntegration_Health_BothChecks_ANDSemantics(t *testing.T) {
	logger := log.NewNop()

	mgr, err := circuitbreaker.NewManager(logger)
	require.NoError(t, err)

	// Create a breaker and leave it in the closed (healthy) state.
	_, err = mgr.GetOrCreate("postgres", circuitbreaker.DefaultConfig())
	require.NoError(t, err)

	// One successful execution to confirm the breaker is alive and closed.
	_, err = mgr.Execute("postgres", func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	assert.Equal(t, circuitbreaker.StateClosed, mgr.GetState("postgres"),
		"precondition: circuit breaker should be closed")

	app := fiber.New()
	app.Get("/health", HealthWithDependencies(
		DependencyCheck{
			Name:           "database",
			CircuitBreaker: mgr,
			ServiceName:    "postgres",
			HealthCheck:    func() bool { return false }, // custom check fails
		},
	))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	// Circuit breaker is healthy (closed), but HealthCheck returns false.
	// AND semantics → overall degraded.
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	result, deps := parseHealthResponse(t, resp)

	assert.Equal(t, "degraded", result["status"])

	dbStatus := depStatus(t, deps, "database")
	assert.Equal(t, false, dbStatus["healthy"])
	// The circuit breaker itself is still closed.
	assert.Equal(t, "closed", dbStatus["circuit_breaker_state"])
}

// ---------------------------------------------------------------------------
// Test 5: Multiple dependencies — 2 healthy, 1 unhealthy → 503.
// ---------------------------------------------------------------------------

func TestIntegration_Health_MultipleDependencies(t *testing.T) {
	logger := log.NewNop()
	cfg := testConfig()

	mgr, err := circuitbreaker.NewManager(logger)
	require.NoError(t, err)

	// Service 1: postgres — healthy.
	_, err = mgr.GetOrCreate("postgres", circuitbreaker.DefaultConfig())
	require.NoError(t, err)

	_, err = mgr.Execute("postgres", func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	// Service 2: redis — tripped to open.
	_, err = mgr.GetOrCreate("redis", cfg)
	require.NoError(t, err)

	tripCircuitBreaker(t, mgr, "redis", int(cfg.ConsecutiveFailures))

	// Service 3: external-api — healthy via custom check (no circuit breaker).

	app := fiber.New()
	app.Get("/health", HealthWithDependencies(
		DependencyCheck{
			Name:           "database",
			CircuitBreaker: mgr,
			ServiceName:    "postgres",
		},
		DependencyCheck{
			Name:           "cache",
			CircuitBreaker: mgr,
			ServiceName:    "redis",
		},
		DependencyCheck{
			Name:        "external-api",
			HealthCheck: func() bool { return true },
		},
	))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	// One unhealthy dependency makes the overall status degraded.
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	result, deps := parseHealthResponse(t, resp)

	assert.Equal(t, "degraded", result["status"])
	require.Len(t, deps, 3, "should report all 3 dependencies")

	// Database: healthy, closed.
	dbStatus := depStatus(t, deps, "database")
	assert.Equal(t, true, dbStatus["healthy"])
	assert.Equal(t, "closed", dbStatus["circuit_breaker_state"])

	// Cache: unhealthy, open.
	cacheStatus := depStatus(t, deps, "cache")
	assert.Equal(t, false, cacheStatus["healthy"])
	assert.Equal(t, "open", cacheStatus["circuit_breaker_state"])

	// External API: healthy, no circuit breaker state.
	apiStatus := depStatus(t, deps, "external-api")
	assert.Equal(t, true, apiStatus["healthy"])

	_, hasCBState := apiStatus["circuit_breaker_state"]
	assert.False(t, hasCBState, "external-api should not have circuit_breaker_state")
}

// ---------------------------------------------------------------------------
// Test 6: Circuit recovery — open → half-open → closed after timeout.
// ---------------------------------------------------------------------------

func TestIntegration_Health_CircuitRecovery(t *testing.T) {
	logger := log.NewNop()
	cfg := testConfig() // Timeout is 1s — the breaker moves to half-open after this.

	mgr, err := circuitbreaker.NewManager(logger)
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("postgres", cfg)
	require.NoError(t, err)

	// Trip the breaker to open.
	tripCircuitBreaker(t, mgr, "postgres", int(cfg.ConsecutiveFailures))

	// ---- Phase 1: health should report degraded while circuit is open ----

	app := fiber.New()
	app.Get("/health", HealthWithDependencies(
		DependencyCheck{
			Name:           "database",
			CircuitBreaker: mgr,
			ServiceName:    "postgres",
		},
	))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	result, deps := parseHealthResponse(t, resp)
	assert.Equal(t, "degraded", result["status"])

	dbStatus := depStatus(t, deps, "database")
	assert.Equal(t, false, dbStatus["healthy"])
	assert.Equal(t, "open", dbStatus["circuit_breaker_state"])

	// ---- Phase 2: wait for timeout so breaker moves to half-open ----

	// Sleep slightly beyond the configured Timeout (1s) to allow the
	// gobreaker state machine to transition from open → half-open.
	time.Sleep(cfg.Timeout + 200*time.Millisecond)

	// In half-open state, gobreaker allows MaxRequests probe requests.
	// A successful probe moves the breaker back to closed.
	_, err = mgr.Execute("postgres", func() (any, error) {
		return "recovered", nil
	})
	require.NoError(t, err)

	// Verify the breaker is now closed.
	assert.Equal(t, circuitbreaker.StateClosed, mgr.GetState("postgres"),
		"circuit breaker should transition back to closed after successful probe")

	// ---- Phase 3: health should report available after recovery ----

	req = httptest.NewRequest(http.MethodGet, "/health", nil)

	resp2, err := app.Test(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp2.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	result2, deps2 := parseHealthResponse(t, resp2)
	assert.Equal(t, "available", result2["status"])

	dbStatus2 := depStatus(t, deps2, "database")
	assert.Equal(t, true, dbStatus2["healthy"])
	assert.Equal(t, "closed", dbStatus2["circuit_breaker_state"])
}
