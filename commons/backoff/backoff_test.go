//go:build unit

package backoff

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExponential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		base     time.Duration
		attempt  int
		expected time.Duration
	}{
		{
			name:     "attempt 0 returns base",
			base:     100 * time.Millisecond,
			attempt:  0,
			expected: 100 * time.Millisecond,
		},
		{
			name:     "attempt 1 doubles base",
			base:     100 * time.Millisecond,
			attempt:  1,
			expected: 200 * time.Millisecond,
		},
		{
			name:     "attempt 2 quadruples base",
			base:     100 * time.Millisecond,
			attempt:  2,
			expected: 400 * time.Millisecond,
		},
		{
			name:     "attempt 3 is 8x base",
			base:     100 * time.Millisecond,
			attempt:  3,
			expected: 800 * time.Millisecond,
		},
		{
			name:     "attempt 10 is 1024x base",
			base:     1 * time.Millisecond,
			attempt:  10,
			expected: 1024 * time.Millisecond,
		},
		{
			name:     "negative attempt treated as 0",
			base:     100 * time.Millisecond,
			attempt:  -5,
			expected: 100 * time.Millisecond,
		},
		{
			name:     "zero base returns 0",
			base:     0,
			attempt:  5,
			expected: 0,
		},
		{
			name:     "negative base returns 0",
			base:     -100 * time.Millisecond,
			attempt:  5,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := Exponential(tt.base, tt.attempt)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExponential_OverflowProtection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		attempt int
	}{
		{"attempt 62 (max allowed)", 62},
		{"attempt 63 clamped to 62", 63},
		{"attempt 100 clamped to 62", 100},
		{"attempt 1000 clamped to 62", 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := Exponential(1*time.Nanosecond, tt.attempt)
			expected := Exponential(1*time.Nanosecond, 62)
			assert.Equal(t, expected, result)
			assert.NotPanics(t, func() {
				_ = Exponential(time.Second, tt.attempt)
			})
		})
	}
}

func TestExponential_MultiplicationOverflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    time.Duration
		attempt int
	}{
		{
			name:    "hour base with attempt 40 overflows",
			base:    time.Hour,
			attempt: 40,
		},
		{
			name:    "hour base with attempt 62 overflows",
			base:    time.Hour,
			attempt: 62,
		},
		{
			name:    "second base with attempt 50 overflows",
			base:    time.Second,
			attempt: 50,
		},
		{
			name:    "large base with moderate attempt overflows",
			base:    24 * time.Hour,
			attempt: 30,
		},
		{
			name:    "max int64 nanoseconds base with attempt 1 overflows",
			base:    time.Duration(math.MaxInt64/2 + 1),
			attempt: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := Exponential(tt.base, tt.attempt)
			assert.Equal(t, time.Duration(math.MaxInt64), result,
				"overflow should clamp to math.MaxInt64")
		})
	}
}

func TestExponential_MultiplicationBoundary(t *testing.T) {
	t.Parallel()

	t.Run("just below overflow threshold remains exact", func(t *testing.T) {
		t.Parallel()

		// 1 nanosecond * 2^40 = 1,099,511,627,776 ns (~18 min) -- no overflow
		result := Exponential(1*time.Nanosecond, 40)
		expected := time.Duration(int64(1) << 40)
		assert.Equal(t, expected, result)
	})

	t.Run("1 nanosecond base never overflows at max shift", func(t *testing.T) {
		t.Parallel()

		// 1 ns * 2^62 = 4,611,686,018,427,387,904 ns (~146 years) -- fits int64
		result := Exponential(1*time.Nanosecond, 62)
		expected := time.Duration(int64(1) << 62)
		assert.Equal(t, expected, result)
	})

	t.Run("2 nanoseconds base overflows at max shift", func(t *testing.T) {
		t.Parallel()

		// 2 ns * 2^62 would be 2^63 which overflows int64
		result := Exponential(2*time.Nanosecond, 62)
		assert.Equal(t, time.Duration(math.MaxInt64), result)
	})

	t.Run("result is always positive", func(t *testing.T) {
		t.Parallel()

		// Ensure no wraparound to negative values
		largeValues := []struct {
			base    time.Duration
			attempt int
		}{
			{time.Hour, 40},
			{time.Minute, 50},
			{time.Second, 55},
			{time.Millisecond, 60},
			{24 * time.Hour, 62},
		}

		for _, v := range largeValues {
			result := Exponential(v.base, v.attempt)
			assert.Positive(t, int64(result),
				"Exponential(%v, %d) should never be negative", v.base, v.attempt)
		}
	})
}

func TestFullJitter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		delay time.Duration
	}{
		{"100ms delay", 100 * time.Millisecond},
		{"1s delay", 1 * time.Second},
		{"10s delay", 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for range 100 {
				result := FullJitter(tt.delay)
				assert.GreaterOrEqual(t, result, time.Duration(0))
				assert.Less(t, result, tt.delay)
			}
		})
	}
}

func TestFullJitter_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		delay    time.Duration
		expected time.Duration
	}{
		{"zero delay returns 0", 0, 0},
		{"negative delay returns 0", -100 * time.Millisecond, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := FullJitter(tt.delay)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFullJitter_Distribution(t *testing.T) {
	t.Parallel()

	const iterations = 1000

	delay := 100 * time.Millisecond

	var sum time.Duration

	for range iterations {
		sum += FullJitter(delay)
	}

	avg := sum / iterations
	expectedMid := delay / 2
	tolerance := delay / 5

	assert.InDelta(t, int64(expectedMid), int64(avg), float64(tolerance),
		"average should be roughly half the delay (expected ~%v, got %v)", expectedMid, avg)
}

func TestExponentialWithJitter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    time.Duration
		attempt int
	}{
		{"attempt 0", 100 * time.Millisecond, 0},
		{"attempt 1", 100 * time.Millisecond, 1},
		{"attempt 5", 100 * time.Millisecond, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			maxDelay := Exponential(tt.base, tt.attempt)

			for range 50 {
				result := ExponentialWithJitter(tt.base, tt.attempt)
				assert.GreaterOrEqual(t, result, time.Duration(0))
				assert.Less(t, result, maxDelay)
			}
		})
	}
}

func TestExponentialWithJitter_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		base     time.Duration
		attempt  int
		expected time.Duration
	}{
		{"zero base returns 0", 0, 5, 0},
		{"negative base returns 0", -100 * time.Millisecond, 5, 0},
		{"negative attempt treated as 0", 100 * time.Millisecond, -5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.expected == 0 && tt.base > 0 {
				maxDelay := Exponential(tt.base, 0)

				for range 50 {
					result := ExponentialWithJitter(tt.base, tt.attempt)
					assert.GreaterOrEqual(t, result, time.Duration(0))
					assert.Less(t, result, maxDelay)
				}
			} else {
				result := ExponentialWithJitter(tt.base, tt.attempt)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestWaitContext(t *testing.T) {
	t.Parallel()

	t.Run("completes sleep successfully", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		start := time.Now()
		err := WaitContext(ctx, 50*time.Millisecond)
		elapsed := time.Since(start)

		require.NoError(t, err)
		assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond)
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		err := WaitContext(ctx, 1*time.Second)
		elapsed := time.Since(start)

		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Less(t, elapsed, 100*time.Millisecond)
	})

	t.Run("respects context deadline", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		err := WaitContext(ctx, 1*time.Second)

		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("zero duration returns immediately", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		start := time.Now()
		err := WaitContext(ctx, 0)
		elapsed := time.Since(start)

		require.NoError(t, err)
		assert.Less(t, elapsed, 10*time.Millisecond)
	})

	t.Run("negative duration returns immediately", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		start := time.Now()
		err := WaitContext(ctx, -100*time.Millisecond)
		elapsed := time.Since(start)

		require.NoError(t, err)
		assert.Less(t, elapsed, 10*time.Millisecond)
	})

	t.Run("already cancelled context returns immediately", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		err := WaitContext(ctx, 1*time.Second)
		elapsed := time.Since(start)

		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Less(t, elapsed, 10*time.Millisecond)
	})
}

func TestCryptoFallbackRand(t *testing.T) {
	t.Parallel()

	t.Run("returns value in range", func(t *testing.T) {
		t.Parallel()

		const maxValue = 1000

		for range 100 {
			result := cryptoFallbackRand(maxValue)
			assert.GreaterOrEqual(t, result, int64(0))
			assert.Less(t, result, int64(maxValue))
		}
	})
}
