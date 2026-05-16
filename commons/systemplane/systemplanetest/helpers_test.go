//go:build unit

package systemplanetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// settleChangefeed
// ---------------------------------------------------------------------------

func TestSettleChangefeed_ZeroDuration(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	go func() {
		settleChangefeed(0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("settleChangefeed(0) should return immediately")
	}
}

func TestSettleChangefeed_NegativeDuration(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	go func() {
		settleChangefeed(-1)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("settleChangefeed(<0) should return immediately")
	}
}

// ---------------------------------------------------------------------------
// signal
// ---------------------------------------------------------------------------

func TestSignal_SendsToChannel(t *testing.T) {
	t.Parallel()

	ch := make(chan struct{}, 1)
	signal(ch)
	select {
	case <-ch:
		// success
	default:
		t.Fatal("expected signal on channel")
	}
}

func TestSignal_FullChannelNoBlock(t *testing.T) {
	t.Parallel()

	ch := make(chan struct{}, 1)
	ch <- struct{}{} // pre-fill
	// Should not block
	signal(ch)
}

// ---------------------------------------------------------------------------
// drainTenantEvents
// ---------------------------------------------------------------------------

func TestDrainTenantEvents_Empty(t *testing.T) {
	t.Parallel()

	ch := make(chan string, 0)
	// Must return immediately without blocking
	drainTenantEvents(ch)
}

func TestDrainTenantEvents_WithItems(t *testing.T) {
	t.Parallel()

	ch := make(chan string, 3)
	ch <- "a"
	ch <- "b"
	ch <- "c"
	drainTenantEvents(ch)
	assert.Equal(t, 0, len(ch))
}

// ---------------------------------------------------------------------------
// waitForValue
// ---------------------------------------------------------------------------

func TestWaitForValue_ReceivesValue(t *testing.T) {
	t.Parallel()

	ch := make(chan any, 1)
	ch <- "hello"
	got := waitForValue(t, ch)
	assert.Equal(t, "hello", got)
}

// ---------------------------------------------------------------------------
// waitForSignal
// ---------------------------------------------------------------------------

func TestWaitForSignal_ReceivesSignal(t *testing.T) {
	t.Parallel()

	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	// Must not timeout
	waitForSignal(t, ch, "test-signal")
}

// ---------------------------------------------------------------------------
// waitForTenantEvent
// ---------------------------------------------------------------------------

func TestWaitForTenantEvent_String(t *testing.T) {
	t.Parallel()

	ch := make(chan string, 1)
	ch <- "tenant-A"
	got := waitForTenantEvent[string](t, ch)
	assert.Equal(t, "tenant-A", got)
}

// ---------------------------------------------------------------------------
// almostEqual
// ---------------------------------------------------------------------------

func TestAlmostEqual_Equal(t *testing.T) {
	t.Parallel()

	assert.True(t, almostEqual(1.0, 1.0))
}

func TestAlmostEqual_WithinTolerance(t *testing.T) {
	t.Parallel()

	assert.True(t, almostEqual(0.1, 0.1+1e-10))
}

func TestAlmostEqual_OutsideTolerance(t *testing.T) {
	t.Parallel()

	assert.False(t, almostEqual(1.0, 2.0))
}

// ---------------------------------------------------------------------------
// numericEquals
// ---------------------------------------------------------------------------

func TestNumericEquals_IntMatch(t *testing.T) {
	t.Parallel()

	assert.True(t, numericEquals(42, 42.0))
}

func TestNumericEquals_Float64Match(t *testing.T) {
	t.Parallel()

	assert.True(t, numericEquals(3.14, 3.14))
}

func TestNumericEquals_StringNoMatch(t *testing.T) {
	t.Parallel()

	assert.False(t, numericEquals("not-a-number", 1.0))
}

func TestNumericEquals_NilNoMatch(t *testing.T) {
	t.Parallel()

	assert.False(t, numericEquals(nil, 1.0))
}

// ---------------------------------------------------------------------------
// stringSlicesEqual
// ---------------------------------------------------------------------------

func TestStringSlicesEqual_BothNil(t *testing.T) {
	t.Parallel()

	assert.True(t, stringSlicesEqual(nil, nil))
}

func TestStringSlicesEqual_EqualSlices(t *testing.T) {
	t.Parallel()

	assert.True(t, stringSlicesEqual([]string{"a", "b"}, []string{"a", "b"}))
}

func TestStringSlicesEqual_DifferentLength(t *testing.T) {
	t.Parallel()

	assert.False(t, stringSlicesEqual([]string{"a"}, []string{"a", "b"}))
}

func TestStringSlicesEqual_DifferentContent(t *testing.T) {
	t.Parallel()

	assert.False(t, stringSlicesEqual([]string{"a", "b"}, []string{"a", "c"}))
}

func TestStringSlicesEqual_NilVsEmpty(t *testing.T) {
	t.Parallel()

	// Both len==0, so considered equal by the length check
	assert.True(t, stringSlicesEqual(nil, []string{}))
}

// ---------------------------------------------------------------------------
// asFloat
// ---------------------------------------------------------------------------

func TestAsFloat_Float64(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 3.14, asFloat(3.14))
}

func TestAsFloat_Int(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 42.0, asFloat(42))
}

func TestAsFloat_Int64(t *testing.T) {
	t.Parallel()

	assert.Equal(t, float64(100), asFloat(int64(100)))
}

func TestAsFloat_Float32(t *testing.T) {
	t.Parallel()

	result := asFloat(float32(1.5))
	assert.InDelta(t, 1.5, result, 0.001)
}

func TestAsFloat_UnknownTypeReturnsNaN(t *testing.T) {
	t.Parallel()

	result := asFloat("not-a-number")
	// asFloat returns math.NaN() for unknown types
	assert.True(t, result != result, "expected NaN for unknown type") // NaN != NaN
}

func TestAsFloat_NilReturnsNaN(t *testing.T) {
	t.Parallel()

	result := asFloat(nil)
	// asFloat returns math.NaN() for nil (default case)
	assert.True(t, result != result, "expected NaN for nil") // NaN != NaN
}

func TestAsFloat_Int32ReturnsNaN(t *testing.T) {
	t.Parallel()

	// int32 is not in the switch, falls to default (NaN)
	result := asFloat(int32(42))
	assert.True(t, result != result, "expected NaN for int32 (not in switch)")
}
