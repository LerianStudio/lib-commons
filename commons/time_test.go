package commons

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsValidDate(t *testing.T) {
	tests := []struct {
		name     string
		date     string
		expected bool
	}{
		{
			name:     "Valid date",
			date:     "2024-01-15",
			expected: true,
		},
		{
			name:     "Invalid format - with time",
			date:     "2024-01-15T10:30:45Z",
			expected: false,
		},
		{
			name:     "Invalid format - wrong separator",
			date:     "2024/01/15",
			expected: false,
		},
		{
			name:     "Invalid date - month 13",
			date:     "2024-13-01",
			expected: false,
		},
		{
			name:     "Invalid date - day 32",
			date:     "2024-01-32",
			expected: false,
		},
		{
			name:     "Empty string",
			date:     "",
			expected: false,
		},
		{
			name:     "Not a date",
			date:     "not-a-date",
			expected: false,
		},
		{
			name:     "Leap year date",
			date:     "2024-02-29",
			expected: true,
		},
		{
			name:     "Invalid leap year date",
			date:     "2023-02-29",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidDate(tt.date)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsInitialDateBeforeFinalDate(t *testing.T) {
	baseDate := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	
	tests := []struct {
		name     string
		initial  time.Time
		final    time.Time
		expected bool
	}{
		{
			name:     "Initial before final",
			initial:  baseDate,
			final:    baseDate.AddDate(0, 0, 1),
			expected: true,
		},
		{
			name:     "Initial after final",
			initial:  baseDate.AddDate(0, 0, 1),
			final:    baseDate,
			expected: false,
		},
		{
			name:     "Same dates",
			initial:  baseDate,
			final:    baseDate,
			expected: true,
		},
		{
			name:     "One second difference - initial before",
			initial:  baseDate,
			final:    baseDate.Add(1 * time.Second),
			expected: true,
		},
		{
			name:     "One second difference - initial after",
			initial:  baseDate.Add(1 * time.Second),
			final:    baseDate,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsInitialDateBeforeFinalDate(tt.initial, tt.final)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsDateRangeWithinMonthLimit(t *testing.T) {
	initial := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	
	tests := []struct {
		name     string
		initial  time.Time
		final    time.Time
		limit    int
		expected bool
	}{
		{
			name:     "Within 1 month limit",
			initial:  initial,
			final:    initial.AddDate(0, 0, 15),
			limit:    1,
			expected: true,
		},
		{
			name:     "Exactly at 1 month limit",
			initial:  initial,
			final:    initial.AddDate(0, 1, 0),
			limit:    1,
			expected: true,
		},
		{
			name:     "Beyond 1 month limit",
			initial:  initial,
			final:    initial.AddDate(0, 1, 1),
			limit:    1,
			expected: false,
		},
		{
			name:     "Within 3 months limit",
			initial:  initial,
			final:    initial.AddDate(0, 2, 15),
			limit:    3,
			expected: true,
		},
		{
			name:     "Beyond 3 months limit",
			initial:  initial,
			final:    initial.AddDate(0, 3, 1),
			limit:    3,
			expected: false,
		},
		{
			name:     "Same date with any limit",
			initial:  initial,
			final:    initial,
			limit:    1,
			expected: true,
		},
		{
			name:     "Final before initial (edge case)",
			initial:  initial,
			final:    initial.AddDate(0, 0, -1),
			limit:    1,
			expected: true,
		},
		{
			name:     "Zero month limit",
			initial:  initial,
			final:    initial.AddDate(0, 0, 1),
			limit:    0,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsDateRangeWithinMonthLimit(tt.initial, tt.final, tt.limit)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeDate(t *testing.T) {
	baseDate := time.Date(2024, 1, 15, 10, 30, 45, 123456789, time.UTC)
	
	tests := []struct {
		name     string
		date     time.Time
		days     *int
		expected string
	}{
		{
			name:     "No days adjustment",
			date:     baseDate,
			days:     nil,
			expected: "2024-01-15",
		},
		{
			name:     "Add 5 days",
			date:     baseDate,
			days:     intPtr(5),
			expected: "2024-01-20",
		},
		{
			name:     "Subtract 5 days",
			date:     baseDate,
			days:     intPtr(-5),
			expected: "2024-01-10",
		},
		{
			name:     "Add days crossing month boundary",
			date:     baseDate,
			days:     intPtr(20),
			expected: "2024-02-04",
		},
		{
			name:     "Subtract days crossing month boundary",
			date:     baseDate,
			days:     intPtr(-20),
			expected: "2023-12-26",
		},
		{
			name:     "Zero days adjustment",
			date:     baseDate,
			days:     intPtr(0),
			expected: "2024-01-15",
		},
		{
			name:     "Add days crossing year boundary",
			date:     time.Date(2023, 12, 25, 0, 0, 0, 0, time.UTC),
			days:     intPtr(10),
			expected: "2024-01-04",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeDate(tt.date, tt.days)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Helper function for int pointers
func intPtr(i int) *int {
	return &i
}

// Benchmarks for time functions
func BenchmarkIsValidDate(b *testing.B) {
	dates := []string{
		"2024-01-15",
		"not-a-date",
		"2024-13-01",
		"2024-02-29",
		"",
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, date := range dates {
			_ = IsValidDate(date)
		}
	}
}

func BenchmarkIsInitialDateBeforeFinalDate(b *testing.B) {
	initial := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	final := time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsInitialDateBeforeFinalDate(initial, final)
	}
}

func BenchmarkIsDateRangeWithinMonthLimit(b *testing.B) {
	initial := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	final := time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsDateRangeWithinMonthLimit(initial, final, 3)
	}
}

func BenchmarkNormalizeDate(b *testing.B) {
	date := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	days := 5
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NormalizeDate(date, &days)
	}
}