//go:build unit

package outbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestWithRetryWindow covers the option.
func TestWithRetryWindow_SetsValue(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{}}
	opt := WithRetryWindow(5 * time.Minute)
	opt(d)
	assert.Equal(t, 5*time.Minute, d.cfg.RetryWindow)
}

func TestWithRetryWindow_ZeroIgnored(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{RetryWindow: time.Minute}}
	opt := WithRetryWindow(0)
	opt(d)
	assert.Equal(t, time.Minute, d.cfg.RetryWindow)
}

// TestWithMaxDispatchAttempts covers the option.
func TestWithMaxDispatchAttempts_SetsValue(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{}}
	opt := WithMaxDispatchAttempts(5)
	opt(d)
	assert.Equal(t, 5, d.cfg.MaxDispatchAttempts)
}

func TestWithMaxDispatchAttempts_ZeroIgnored(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{MaxDispatchAttempts: 3}}
	opt := WithMaxDispatchAttempts(0)
	opt(d)
	assert.Equal(t, 3, d.cfg.MaxDispatchAttempts)
}

// TestWithProcessingTimeout covers the option.
func TestWithProcessingTimeout_SetsValue(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{}}
	opt := WithProcessingTimeout(30 * time.Second)
	opt(d)
	assert.Equal(t, 30*time.Second, d.cfg.ProcessingTimeout)
}

func TestWithProcessingTimeout_ZeroIgnored(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{ProcessingTimeout: time.Minute}}
	opt := WithProcessingTimeout(0)
	opt(d)
	assert.Equal(t, time.Minute, d.cfg.ProcessingTimeout)
}

// TestWithTenantMetricAttributes covers the option.
func TestWithTenantMetricAttributes_True(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{}}
	opt := WithTenantMetricAttributes(true)
	opt(d)
	assert.True(t, d.cfg.IncludeTenantMetrics)
}

func TestWithTenantMetricAttributes_False(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{IncludeTenantMetrics: true}}
	opt := WithTenantMetricAttributes(false)
	opt(d)
	assert.False(t, d.cfg.IncludeTenantMetrics)
}

// TestWithMeterProvider_Nil covers the nil provider path.
func TestWithMeterProvider_Nil(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{cfg: DispatcherConfig{}}
	opt := WithMeterProvider(nil)
	opt(d)
	assert.Nil(t, d.cfg.MeterProvider)
}
