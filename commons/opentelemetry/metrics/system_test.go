//go:build unit

package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test: System metric variable definitions
// ---------------------------------------------------------------------------

func TestSystemMetrics_MetricDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		metric     Metric
		wantName   string
		wantUnit   string
		wantDescNE string // description must not be empty
	}{
		{
			name:       "CPU usage metric has correct name",
			metric:     MetricSystemCPUUsage,
			wantName:   "system.cpu.usage",
			wantUnit:   "percentage",
			wantDescNE: "",
		},
		{
			name:       "Memory usage metric has correct name",
			metric:     MetricSystemMemUsage,
			wantName:   "system.mem.usage",
			wantUnit:   "percentage",
			wantDescNE: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantName, tt.metric.Name)
			assert.Equal(t, tt.wantUnit, tt.metric.Unit)
			assert.NotEmpty(t, tt.metric.Description)
		})
	}
}

// ---------------------------------------------------------------------------
// Test: RecordSystemCPUUsage with valid factory
// ---------------------------------------------------------------------------

func TestRecordSystemCPUUsage_ValidFactory(t *testing.T) {
	t.Parallel()

	factory, reader := newTestFactory(t)

	err := factory.RecordSystemCPUUsage(context.Background(), 75)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "system.cpu.usage")
	require.NotNil(t, m, "system.cpu.usage metric must exist")

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(75), dps[0].Value)
}

// ---------------------------------------------------------------------------
// Test: RecordSystemMemUsage with valid factory
// ---------------------------------------------------------------------------

func TestRecordSystemMemUsage_ValidFactory(t *testing.T) {
	t.Parallel()

	factory, reader := newTestFactory(t)

	err := factory.RecordSystemMemUsage(context.Background(), 42)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "system.mem.usage")
	require.NotNil(t, m, "system.mem.usage metric must exist")

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(42), dps[0].Value)
}

// ---------------------------------------------------------------------------
// Test: RecordSystemCPUUsage — zero value
// ---------------------------------------------------------------------------

func TestRecordSystemCPUUsage_ZeroValue(t *testing.T) {
	t.Parallel()

	factory, reader := newTestFactory(t)

	err := factory.RecordSystemCPUUsage(context.Background(), 0)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "system.cpu.usage")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(0), dps[0].Value)
}

// ---------------------------------------------------------------------------
// Test: RecordSystemMemUsage — zero value
// ---------------------------------------------------------------------------

func TestRecordSystemMemUsage_ZeroValue(t *testing.T) {
	t.Parallel()

	factory, reader := newTestFactory(t)

	err := factory.RecordSystemMemUsage(context.Background(), 0)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "system.mem.usage")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(0), dps[0].Value)
}

// ---------------------------------------------------------------------------
// Test: RecordSystemCPUUsage — boundary value 100%
// ---------------------------------------------------------------------------

func TestRecordSystemCPUUsage_MaxPercentage(t *testing.T) {
	t.Parallel()

	factory, reader := newTestFactory(t)

	err := factory.RecordSystemCPUUsage(context.Background(), 100)
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "system.cpu.usage")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(100), dps[0].Value)
}

// ---------------------------------------------------------------------------
// Test: RecordSystemMemUsage — overwrite (gauge last-value semantics)
// ---------------------------------------------------------------------------

func TestRecordSystemMemUsage_Overwrite(t *testing.T) {
	t.Parallel()

	factory, reader := newTestFactory(t)

	require.NoError(t, factory.RecordSystemMemUsage(context.Background(), 30))
	require.NoError(t, factory.RecordSystemMemUsage(context.Background(), 85))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "system.mem.usage")
	require.NotNil(t, m)

	dps := gaugeDataPoints(t, m)
	require.Len(t, dps, 1)
	// Gauge keeps last value
	assert.Equal(t, int64(85), dps[0].Value)
}

// ---------------------------------------------------------------------------
// Test: Nop factory — system metrics don't error
// ---------------------------------------------------------------------------

func TestRecordSystemMetrics_NopFactory(t *testing.T) {
	t.Parallel()

	factory := NewNopFactory()

	err := factory.RecordSystemCPUUsage(context.Background(), 50)
	assert.NoError(t, err, "nop factory should not error for CPU usage")

	err = factory.RecordSystemMemUsage(context.Background(), 60)
	assert.NoError(t, err, "nop factory should not error for memory usage")
}
