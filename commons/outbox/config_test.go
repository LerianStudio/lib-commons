//go:build unit

package outbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type pointerRetryClassifier struct{}

func (*pointerRetryClassifier) IsNonRetryable(error) bool { return true }

func TestDispatcherConfigNormalize_AppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg := DispatcherConfig{
		DispatchInterval:            -1,
		BatchSize:                   0,
		PublishMaxAttempts:          -2,
		PublishBackoff:              0,
		ListPendingFailureThreshold: -1,
		RetryWindow:                 0,
		MaxDispatchAttempts:         0,
		ProcessingTimeout:           -5,
		PriorityBudget:              0,
		MaxFailedPerBatch:           -1,
	}

	cfg.normalize()

	defaults := DefaultDispatcherConfig()
	require.Equal(t, defaults.DispatchInterval, cfg.DispatchInterval)
	require.Equal(t, defaults.BatchSize, cfg.BatchSize)
	require.Equal(t, defaults.PublishMaxAttempts, cfg.PublishMaxAttempts)
	require.Equal(t, defaults.PublishBackoff, cfg.PublishBackoff)
	require.Equal(t, defaults.ListPendingFailureThreshold, cfg.ListPendingFailureThreshold)
	require.Equal(t, defaults.RetryWindow, cfg.RetryWindow)
	require.Equal(t, defaults.MaxDispatchAttempts, cfg.MaxDispatchAttempts)
	require.Equal(t, defaults.ProcessingTimeout, cfg.ProcessingTimeout)
	require.Equal(t, defaults.PriorityBudget, cfg.PriorityBudget)
	require.Equal(t, defaults.MaxFailedPerBatch, cfg.MaxFailedPerBatch)
	require.Equal(t, defaults.MaxTenantMetricDimensions, cfg.MaxTenantMetricDimensions)
	require.Equal(t, defaults.MaxTrackedListPendingFailureTenants, cfg.MaxTrackedListPendingFailureTenants)
	require.False(t, cfg.IncludeTenantMetrics)
	require.Nil(t, cfg.MeterProvider)
}

func TestDispatcherConfigNormalize_PreservesValidValues(t *testing.T) {
	t.Parallel()

	cfg := DispatcherConfig{
		DispatchInterval:                    3 * time.Second,
		BatchSize:                           25,
		PublishMaxAttempts:                  7,
		PublishBackoff:                      120 * time.Millisecond,
		ListPendingFailureThreshold:         8,
		RetryWindow:                         2 * time.Minute,
		MaxDispatchAttempts:                 9,
		ProcessingTimeout:                   4 * time.Minute,
		PriorityBudget:                      11,
		MaxFailedPerBatch:                   13,
		IncludeTenantMetrics:                true,
		MaxTenantMetricDimensions:           55,
		MaxTrackedListPendingFailureTenants: 99,
	}

	cfg.normalize()

	require.Equal(t, 3*time.Second, cfg.DispatchInterval)
	require.Equal(t, 25, cfg.BatchSize)
	require.Equal(t, 7, cfg.PublishMaxAttempts)
	require.Equal(t, 120*time.Millisecond, cfg.PublishBackoff)
	require.Equal(t, 8, cfg.ListPendingFailureThreshold)
	require.Equal(t, 2*time.Minute, cfg.RetryWindow)
	require.Equal(t, 9, cfg.MaxDispatchAttempts)
	require.Equal(t, 4*time.Minute, cfg.ProcessingTimeout)
	require.Equal(t, 11, cfg.PriorityBudget)
	require.Equal(t, 13, cfg.MaxFailedPerBatch)
	require.True(t, cfg.IncludeTenantMetrics)
	require.Equal(t, 55, cfg.MaxTenantMetricDimensions)
	require.Equal(t, 99, cfg.MaxTrackedListPendingFailureTenants)
}

func TestWithRetryClassifier_IgnoresTypedNil(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{}
	var classifier *pointerRetryClassifier

	WithRetryClassifier(classifier)(dispatcher)

	require.Nil(t, dispatcher.retryClassifier)
}

func TestWithMaxTenantMetricDimensions(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{cfg: DefaultDispatcherConfig()}

	WithMaxTenantMetricDimensions(42)(dispatcher)
	require.Equal(t, 42, dispatcher.cfg.MaxTenantMetricDimensions)

	WithMaxTenantMetricDimensions(0)(dispatcher)
	require.Equal(t, 42, dispatcher.cfg.MaxTenantMetricDimensions)
}

func TestWithPriorityEventTypes_EmptyInputKeepsNil(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{cfg: DefaultDispatcherConfig()}

	WithPriorityEventTypes("")(dispatcher)
	require.Nil(t, dispatcher.cfg.PriorityEventTypes)
}

func TestWithPriorityEventTypes_TrimsWhitespaceAndDropsEmpty(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{cfg: DefaultDispatcherConfig()}

	WithPriorityEventTypes("  payments.created  ", "\t", "payments.failed", "  ")(dispatcher)
	require.Equal(t, []string{"payments.created", "payments.failed"}, dispatcher.cfg.PriorityEventTypes)
}

func TestWithMaxTrackedListPendingFailureTenants(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{cfg: DefaultDispatcherConfig()}

	WithMaxTrackedListPendingFailureTenants(12)(dispatcher)
	require.Equal(t, 12, dispatcher.cfg.MaxTrackedListPendingFailureTenants)

	WithMaxTrackedListPendingFailureTenants(0)(dispatcher)
	require.Equal(t, 12, dispatcher.cfg.MaxTrackedListPendingFailureTenants)
}
