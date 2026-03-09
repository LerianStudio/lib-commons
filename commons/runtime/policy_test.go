//go:build unit

package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPanicPolicy_String tests the String method for all PanicPolicy values.
func TestPanicPolicy_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   PanicPolicy
		expected string
	}{
		{
			name:     "KeepRunning returns correct string",
			policy:   KeepRunning,
			expected: "KeepRunning",
		},
		{
			name:     "CrashProcess returns correct string",
			policy:   CrashProcess,
			expected: "CrashProcess",
		},
		{
			name:     "Unknown positive value returns Unknown",
			policy:   PanicPolicy(99),
			expected: "Unknown",
		},
		{
			name:     "Negative value returns Unknown",
			policy:   PanicPolicy(-1),
			expected: "Unknown",
		},
		{
			name:     "Large value returns Unknown",
			policy:   PanicPolicy(1000),
			expected: "Unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := tt.policy.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestPanicPolicy_IotaOrdering verifies the iota constant ordering.
func TestPanicPolicy_IotaOrdering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		policy        PanicPolicy
		expectedValue int
	}{
		{
			name:          "KeepRunning is 0 (first iota)",
			policy:        KeepRunning,
			expectedValue: 0,
		},
		{
			name:          "CrashProcess is 1 (second iota)",
			policy:        CrashProcess,
			expectedValue: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expectedValue, int(tt.policy))
		})
	}
}

// TestPanicPolicy_TypeSafety verifies type conversion behavior.
func TestPanicPolicy_TypeSafety(t *testing.T) {
	t.Parallel()

	t.Run("explicit int conversion works", func(t *testing.T) {
		t.Parallel()

		p := KeepRunning
		assert.Equal(t, 0, int(p))

		p = CrashProcess
		assert.Equal(t, 1, int(p))
	})

	t.Run("policy from int conversion", func(t *testing.T) {
		t.Parallel()

		p := PanicPolicy(0)
		assert.Equal(t, KeepRunning, p)

		p = PanicPolicy(1)
		assert.Equal(t, CrashProcess, p)
	})
}
