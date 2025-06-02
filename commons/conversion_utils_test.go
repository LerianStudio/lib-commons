package commons

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafeIntToUint64(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected uint64
	}{
		{
			name:     "Positive number",
			input:    100,
			expected: 100,
		},
		{
			name:     "Zero",
			input:    0,
			expected: 0,
		},
		{
			name:     "Negative number",
			input:    -100,
			expected: 1,
		},
		{
			name:     "Large positive number",
			input:    math.MaxInt32,
			expected: uint64(math.MaxInt32),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafeIntToUint64(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSafeInt64ToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected int
	}{
		{
			name:     "Normal positive value",
			input:    100,
			expected: 100,
		},
		{
			name:     "Zero",
			input:    0,
			expected: 0,
		},
		{
			name:     "Negative value",
			input:    -100,
			expected: -100,
		},
		{
			name:     "Max int value",
			input:    int64(math.MaxInt),
			expected: math.MaxInt,
		},
		{
			name:     "Min int value",
			input:    int64(math.MinInt),
			expected: math.MinInt,
		},
		{
			name:     "Overflow positive",
			input:    math.MaxInt64,
			expected: math.MaxInt,
		},
		{
			name:     "Overflow negative",
			input:    math.MinInt64,
			expected: math.MinInt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafeInt64ToInt(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSafeUintToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    uint
		expected int
	}{
		{
			name:     "Normal value",
			input:    100,
			expected: 100,
		},
		{
			name:     "Zero",
			input:    0,
			expected: 0,
		},
		{
			name:     "Max safe value",
			input:    uint(math.MaxInt),
			expected: math.MaxInt,
		},
		{
			name:     "Overflow",
			input:    uint(math.MaxInt) + 1,
			expected: math.MaxInt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafeUintToInt(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
