//go:build unit

package circuitbreaker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestMetricsFactory creates a MetricsFactory backed by a real SDK meter
// provider with a ManualReader, mirroring the pattern in metrics/v2_test.go.
func newTestMetricsFactory(t *testing.T) (*metrics.MetricsFactory, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test-circuitbreaker")

	factory, err := metrics.NewMetricsFactory(meter, &log.NopLogger{})
	require.NoError(t, err)

	return factory, reader
}

// collectMetrics calls reader.Collect and returns the ResourceMetrics payload.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()

	var rm metricdata.ResourceMetrics

	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	return rm
}

// findMetricByName walks the collected ResourceMetrics and returns the first
// Metrics entry whose Name matches. Returns nil if not found.
func findMetricByName(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}

	return nil
}

// sumDataPoints extracts data points from a Sum metric.
func sumDataPoints(t *testing.T, m *metricdata.Metrics) []metricdata.DataPoint[int64] {
	t.Helper()

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data, got %T", m.Data)

	return sum.DataPoints
}

// hasAttributeValue checks whether a data point's attribute set contains the key/value pair.
func hasAttributeValue(dp metricdata.DataPoint[int64], key, value string) bool {
	iter := dp.Attributes.Iter()
	for iter.Next() {
		kv := iter.Attribute()
		if string(kv.Key) == key && kv.Value.AsString() == value {
			return true
		}
	}

	return false
}

// ---------------------------------------------------------------------------
// Test: WithMetricsFactory(nil) — manager works, no metrics emitted, no panic
// ---------------------------------------------------------------------------

func TestMetrics_WithNilFactory_NoPanic(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(nil))
	require.NoError(t, err)

	// Verify the metricsFactory field is nil on the concrete manager
	m := mgr.(*manager)
	assert.Nil(t, m.metricsFactory, "metricsFactory should be nil when WithMetricsFactory(nil) is used")

	// Create a breaker and execute — must not panic even without metrics
	_, err = mgr.GetOrCreate("no-metrics-svc", DefaultConfig())
	require.NoError(t, err)

	result, err := mgr.Execute("no-metrics-svc", func() (any, error) {
		return "ok", nil
	})
	assert.NoError(t, err)
	assert.Equal(t, "ok", result)

	// Execute with error — recordExecution("error") must not panic
	_, err = mgr.Execute("no-metrics-svc", func() (any, error) {
		return nil, errors.New("boom")
	})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Test: WithMetricsFactory(factory) — option is applied to manager
// ---------------------------------------------------------------------------

func TestMetrics_WithFactory_Applied(t *testing.T) {
	t.Parallel()

	factory, _ := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)

	m := mgr.(*manager)
	assert.Same(t, factory, m.metricsFactory, "metricsFactory should be the factory passed via option")
}

// ---------------------------------------------------------------------------
// Test: recordExecution — success path emits counter with result="success"
// ---------------------------------------------------------------------------

func TestMetrics_RecordExecution_Success(t *testing.T) {
	t.Parallel()

	factory, reader := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("exec-svc", DefaultConfig())
	require.NoError(t, err)

	// Successful execution
	_, err = mgr.Execute("exec-svc", func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "circuit_breaker_executions_total")
	require.NotNil(t, m, "execution metric must be recorded")

	dps := sumDataPoints(t, m)
	require.NotEmpty(t, dps)

	// Find the data point with result="success"
	found := false

	for _, dp := range dps {
		if hasAttributeValue(dp, "result", "success") && hasAttributeValue(dp, "service", "exec-svc") {
			found = true
			assert.Equal(t, int64(1), dp.Value, "one successful execution should record value 1")
		}
	}

	assert.True(t, found, "expected a data point with result=success and service=exec-svc")
}

// ---------------------------------------------------------------------------
// Test: recordExecution — error path emits counter with result="error"
// ---------------------------------------------------------------------------

func TestMetrics_RecordExecution_Error(t *testing.T) {
	t.Parallel()

	factory, reader := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("err-svc", DefaultConfig())
	require.NoError(t, err)

	// Failing execution (the wrapped function returns an error)
	_, err = mgr.Execute("err-svc", func() (any, error) {
		return nil, errors.New("service failure")
	})
	assert.Error(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "circuit_breaker_executions_total")
	require.NotNil(t, m, "execution metric must be recorded on error path")

	dps := sumDataPoints(t, m)

	found := false

	for _, dp := range dps {
		if hasAttributeValue(dp, "result", "error") && hasAttributeValue(dp, "service", "err-svc") {
			found = true
			assert.Equal(t, int64(1), dp.Value)
		}
	}

	assert.True(t, found, "expected a data point with result=error and service=err-svc")
}

// ---------------------------------------------------------------------------
// Test: recordExecution — open-state rejection emits result="rejected_open"
// ---------------------------------------------------------------------------

func TestMetrics_RecordExecution_RejectedOpen(t *testing.T) {
	t.Parallel()

	factory, reader := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             5 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = mgr.GetOrCreate("reject-svc", cfg)
	require.NoError(t, err)

	// Trip the breaker open
	for i := 0; i < 3; i++ {
		_, _ = mgr.Execute("reject-svc", func() (any, error) {
			return nil, errors.New("fail")
		})
	}

	require.Equal(t, StateOpen, mgr.GetState("reject-svc"))

	// This call should be rejected by the open breaker
	_, err = mgr.Execute("reject-svc", func() (any, error) {
		return nil, nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "currently unavailable")

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "circuit_breaker_executions_total")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)

	found := false

	for _, dp := range dps {
		if hasAttributeValue(dp, "result", "rejected_open") && hasAttributeValue(dp, "service", "reject-svc") {
			found = true
			assert.GreaterOrEqual(t, dp.Value, int64(1))
		}
	}

	assert.True(t, found, "expected a data point with result=rejected_open and service=reject-svc")
}

// ---------------------------------------------------------------------------
// Test: recordStateTransition — state change from closed → open emits metric
// ---------------------------------------------------------------------------

func TestMetrics_RecordStateTransition_ClosedToOpen(t *testing.T) {
	t.Parallel()

	factory, reader := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             5 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = mgr.GetOrCreate("state-svc", cfg)
	require.NoError(t, err)

	// Trip the breaker: consecutive failures → closed→open transition
	for i := 0; i < 3; i++ {
		_, _ = mgr.Execute("state-svc", func() (any, error) {
			return nil, errors.New("fail")
		})
	}

	require.Equal(t, StateOpen, mgr.GetState("state-svc"))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "circuit_breaker_state_transitions_total")
	require.NotNil(t, m, "state transition metric must be recorded")

	dps := sumDataPoints(t, m)

	found := false

	for _, dp := range dps {
		if hasAttributeValue(dp, "from_state", string(StateClosed)) &&
			hasAttributeValue(dp, "to_state", string(StateOpen)) &&
			hasAttributeValue(dp, "service", "state-svc") {
			found = true
			assert.GreaterOrEqual(t, dp.Value, int64(1))
		}
	}

	assert.True(t, found, "expected state transition metric with from_state=closed, to_state=open, service=state-svc")
}

// ---------------------------------------------------------------------------
// Test: recordStateTransition — direct call on manager struct (nil factory)
// ---------------------------------------------------------------------------

func TestMetrics_RecordStateTransition_NilFactory_Noop(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)

	m := mgr.(*manager)

	// Direct call with nil metricsFactory — must be a no-op, no panic
	assert.NotPanics(t, func() {
		m.recordStateTransition("any-service", StateClosed, StateOpen)
	})
}

// ---------------------------------------------------------------------------
// Test: recordExecution — direct call on manager struct (nil factory)
// ---------------------------------------------------------------------------

func TestMetrics_RecordExecution_NilFactory_Noop(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)

	m := mgr.(*manager)

	// Direct call with nil metricsFactory — must be a no-op, no panic
	assert.NotPanics(t, func() {
		m.recordExecution("any-service", "success")
	})
}

// ---------------------------------------------------------------------------
// Test: SanitizeMetricLabel is applied — long service name > 64 chars
// ---------------------------------------------------------------------------

func TestMetrics_LongServiceName_Sanitized(t *testing.T) {
	t.Parallel()

	factory, reader := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)

	// Create a service name that exceeds 64 characters
	longName := strings.Repeat("a", 100)
	require.Greater(t, len(longName), 64, "test precondition: service name must exceed 64 chars")

	_, err = mgr.GetOrCreate(longName, DefaultConfig())
	require.NoError(t, err)

	_, err = mgr.Execute(longName, func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "circuit_breaker_executions_total")
	require.NotNil(t, m, "execution metric must be recorded for long service name")

	dps := sumDataPoints(t, m)
	require.NotEmpty(t, dps)

	// The service label must be truncated to 64 characters
	truncatedName := longName[:64]

	found := false

	for _, dp := range dps {
		if hasAttributeValue(dp, "service", truncatedName) {
			found = true
		}
	}

	assert.True(t, found, "service label should be truncated to 64 characters via SanitizeMetricLabel")
}

// ---------------------------------------------------------------------------
// Test: Multiple executions accumulate correctly
// ---------------------------------------------------------------------------

func TestMetrics_MultipleExecutions_Accumulate(t *testing.T) {
	t.Parallel()

	factory, reader := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("accum-svc", DefaultConfig())
	require.NoError(t, err)

	// Run 3 successful and 2 failed executions
	for i := 0; i < 3; i++ {
		_, err = mgr.Execute("accum-svc", func() (any, error) {
			return "ok", nil
		})
		require.NoError(t, err)
	}

	for i := 0; i < 2; i++ {
		_, _ = mgr.Execute("accum-svc", func() (any, error) {
			return nil, errors.New("fail")
		})
	}

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "circuit_breaker_executions_total")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)

	var successVal, errorVal int64

	for _, dp := range dps {
		if hasAttributeValue(dp, "service", "accum-svc") {
			if hasAttributeValue(dp, "result", "success") {
				successVal = dp.Value
			}

			if hasAttributeValue(dp, "result", "error") {
				errorVal = dp.Value
			}
		}
	}

	assert.Equal(t, int64(3), successVal, "3 successful executions should be recorded")
	assert.Equal(t, int64(2), errorVal, "2 failed executions should be recorded")
}

// ---------------------------------------------------------------------------
// Test: Metric definitions have correct names and units
// ---------------------------------------------------------------------------

func TestMetrics_MetricDefinitions(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "circuit_breaker_state_transitions_total", stateTransitionMetric.Name)
	assert.Equal(t, "1", stateTransitionMetric.Unit)
	assert.NotEmpty(t, stateTransitionMetric.Description)

	assert.Equal(t, "circuit_breaker_executions_total", executionMetric.Name)
	assert.Equal(t, "1", executionMetric.Unit)
	assert.NotEmpty(t, executionMetric.Description)
}
