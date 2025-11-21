package commons

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeDateTime(t *testing.T) {
	// Define a fixed location for consistent testing
	loc := time.UTC

	tests := []struct {
		name     string
		date     time.Time
		days     *int
		endOfDay bool
		expected string
	}{
		{
			name:     "should preserve time when date is not normalized and days is nil - endOfDay false",
			date:     time.Date(2024, 1, 15, 14, 30, 45, 123456789, loc),
			days:     nil,
			endOfDay: false,
			expected: "2024-01-15 14:30:45",
		},
		{
			name:     "should preserve time when date is not normalized and days is nil - endOfDay true",
			date:     time.Date(2024, 1, 15, 14, 30, 45, 123456789, loc),
			days:     nil,
			endOfDay: true,
			expected: "2024-01-15 14:30:45",
		},
		{
			name:     "should normalize to start of day when date is normalized at 00:00:00 and endOfDay is false",
			date:     time.Date(2024, 1, 15, 0, 0, 0, 0, loc),
			days:     nil,
			endOfDay: false,
			expected: "2024-01-15 00:00:00",
		},
		{
			name:     "should normalize to end of day when date is normalized at 00:00:00 and endOfDay is true",
			date:     time.Date(2024, 1, 15, 0, 0, 0, 0, loc),
			days:     nil,
			endOfDay: true,
			expected: "2024-01-15 23:59:59",
		},
		{
			name:     "should normalize to start of day when date is normalized at 23:59:59 and endOfDay is false",
			date:     time.Date(2024, 1, 15, 23, 59, 59, 999999999, loc),
			days:     nil,
			endOfDay: false,
			expected: "2024-01-15 00:00:00",
		},
		{
			name:     "should normalize to end of day when date is normalized at 23:59:59 and endOfDay is true",
			date:     time.Date(2024, 1, 15, 23, 59, 59, 999999999, loc),
			days:     nil,
			endOfDay: true,
			expected: "2024-01-15 23:59:59",
		},
		{
			name:     "should add days and preserve time when date is not normalized",
			date:     time.Date(2024, 1, 15, 14, 30, 45, 123456789, loc),
			days:     intPtr(5),
			endOfDay: false,
			expected: "2024-01-20 14:30:45",
		},
		{
			name:     "should add days and normalize to start of day when normalized at 00:00:00 and endOfDay is false",
			date:     time.Date(2024, 1, 15, 0, 0, 0, 0, loc),
			days:     intPtr(5),
			endOfDay: false,
			expected: "2024-01-20 00:00:00",
		},
		{
			name:     "should add days and normalize to end of day when normalized at 00:00:00 and endOfDay is true",
			date:     time.Date(2024, 1, 15, 0, 0, 0, 0, loc),
			days:     intPtr(5),
			endOfDay: true,
			expected: "2024-01-20 23:59:59",
		},
		{
			name:     "should subtract days when days is negative",
			date:     time.Date(2024, 1, 15, 14, 30, 45, 123456789, loc),
			days:     intPtr(-5),
			endOfDay: false,
			expected: "2024-01-10 14:30:45",
		},
		{
			name:     "should subtract days and normalize to end of day when normalized and endOfDay is true",
			date:     time.Date(2024, 1, 15, 23, 59, 59, 999999999, loc),
			days:     intPtr(-5),
			endOfDay: true,
			expected: "2024-01-10 23:59:59",
		},
		{
			name:     "should handle time at 00:00:01 (not normalized) - preserve time",
			date:     time.Date(2024, 1, 15, 0, 0, 1, 0, loc),
			days:     nil,
			endOfDay: false,
			expected: "2024-01-15 00:00:01",
		},
		{
			name:     "should handle time at 23:59:58 (not normalized) - preserve time",
			date:     time.Date(2024, 1, 15, 23, 59, 58, 0, loc),
			days:     nil,
			endOfDay: true,
			expected: "2024-01-15 23:59:58",
		},
		{
			name:     "should handle month boundary when adding days",
			date:     time.Date(2024, 1, 31, 0, 0, 0, 0, loc),
			days:     intPtr(1),
			endOfDay: false,
			expected: "2024-02-01 00:00:00",
		},
		{
			name:     "should handle year boundary when adding days",
			date:     time.Date(2024, 12, 31, 23, 59, 59, 999999999, loc),
			days:     intPtr(1),
			endOfDay: true,
			expected: "2025-01-01 23:59:59",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeDateTime(tt.date, tt.days, tt.endOfDay)
			assert.Equal(t, tt.expected, result, "NormalizeDateTime() result should match expected value")
		})
	}
}

// intPtr is a helper function to create a pointer to an int
func intPtr(i int) *int {
	return &i
}
