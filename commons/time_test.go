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
		{
			name:     "should truncate nanoseconds and detect boundary at 00:00:00 with fractional seconds",
			date:     time.Date(2024, 1, 15, 0, 0, 0, 123456789, loc),
			days:     nil,
			endOfDay: false,
			expected: "2024-01-15 00:00:00",
		},
		{
			name:     "should truncate nanoseconds and detect boundary at 00:00:00 with fractional seconds - endOfDay true",
			date:     time.Date(2024, 1, 15, 0, 0, 0, 123456789, loc),
			days:     nil,
			endOfDay: true,
			expected: "2024-01-15 23:59:59",
		},
		{
			name:     "should truncate nanoseconds and detect boundary at 23:59:59 with fractional seconds",
			date:     time.Date(2024, 1, 15, 23, 59, 59, 123456789, loc),
			days:     nil,
			endOfDay: true,
			expected: "2024-01-15 23:59:59",
		},
		{
			name:     "should truncate nanoseconds and detect boundary at 23:59:59 with fractional seconds - endOfDay false",
			date:     time.Date(2024, 1, 15, 23, 59, 59, 123456789, loc),
			days:     nil,
			endOfDay: false,
			expected: "2024-01-15 00:00:00",
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

func TestIsValidDate(t *testing.T) {
	tests := []struct {
		name     string
		date     string
		expected bool
	}{
		{
			name:     "should return true for valid date format YYYY-MM-DD",
			date:     "2024-01-15",
			expected: true,
		},
		{
			name:     "should return false for valid date with single digit month and day",
			date:     "2024-1-5",
			expected: false,
		},
		{
			name:     "should return true for valid date at year boundary",
			date:     "2024-12-31",
			expected: true,
		},
		{
			name:     "should return true for valid date at month start",
			date:     "2024-01-01",
			expected: true,
		},
		{
			name:     "should return false for invalid date format - missing dashes",
			date:     "20240115",
			expected: false,
		},
		{
			name:     "should return false for invalid date format - wrong separator",
			date:     "2024/01/15",
			expected: false,
		},
		{
			name:     "should return false for invalid date format - with time",
			date:     "2024-01-15 14:30:45",
			expected: false,
		},
		{
			name:     "should return false for empty string",
			date:     "",
			expected: false,
		},
		{
			name:     "should return false for invalid date - invalid month",
			date:     "2024-13-15",
			expected: false,
		},
		{
			name:     "should return false for invalid date - invalid day",
			date:     "2024-01-32",
			expected: false,
		},
		{
			name:     "should return false for invalid date - leap year invalid day",
			date:     "2024-02-30",
			expected: false,
		},
		{
			name:     "should return true for valid leap year date",
			date:     "2024-02-29",
			expected: true,
		},
		{
			name:     "should return false for non-leap year february 29",
			date:     "2023-02-29",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidDate(tt.date)
			assert.Equal(t, tt.expected, result, "IsValidDate() result should match expected value")
		})
	}
}

func TestIsInitialDateBeforeFinalDate(t *testing.T) {
	loc := time.UTC

	tests := []struct {
		name     string
		initial  time.Time
		final    time.Time
		expected bool
	}{
		{
			name:     "should return true when initial date is before final date",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 20, 10, 0, 0, 0, loc),
			expected: true,
		},
		{
			name:     "should return true when initial date equals final date",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			expected: true,
		},
		{
			name:     "should return true when initial date is before final date by time",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 15, 11, 0, 0, 0, loc),
			expected: true,
		},
		{
			name:     "should return false when initial date is after final date",
			initial:  time.Date(2024, 1, 20, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			expected: false,
		},
		{
			name:     "should return false when initial date is after final date by time",
			initial:  time.Date(2024, 1, 15, 11, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			expected: false,
		},
		{
			name:     "should return true when initial date is same day but earlier time",
			initial:  time.Date(2024, 1, 15, 9, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			expected: true,
		},
		{
			name:     "should handle year boundary correctly - initial before",
			initial:  time.Date(2023, 12, 31, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 1, 10, 0, 0, 0, loc),
			expected: true,
		},
		{
			name:     "should handle year boundary correctly - initial after",
			initial:  time.Date(2024, 1, 1, 10, 0, 0, 0, loc),
			final:    time.Date(2023, 12, 31, 10, 0, 0, 0, loc),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsInitialDateBeforeFinalDate(tt.initial, tt.final)
			assert.Equal(t, tt.expected, result, "IsInitialDateBeforeFinalDate() result should match expected value")
		})
	}
}

func TestIsDateRangeWithinMonthLimit(t *testing.T) {
	loc := time.UTC

	tests := []struct {
		name     string
		initial  time.Time
		final    time.Time
		limit    int
		expected bool
	}{
		{
			name:     "should return true when date range is within month limit",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 2, 15, 10, 0, 0, 0, loc),
			limit:    1,
			expected: true,
		},
		{
			name:     "should return true when final date equals limit date",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 2, 15, 10, 0, 0, 0, loc),
			limit:    1,
			expected: true,
		},
		{
			name:     "should return true when final date is before limit date",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 2, 10, 10, 0, 0, 0, loc),
			limit:    1,
			expected: true,
		},
		{
			name:     "should return false when final date is after limit date",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 2, 20, 10, 0, 0, 0, loc),
			limit:    1,
			expected: false,
		},
		{
			name:     "should return true when limit is 0 and dates are same",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			limit:    0,
			expected: true,
		},
		{
			name:     "should return false when limit is 0 and dates are different",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 16, 10, 0, 0, 0, loc),
			limit:    0,
			expected: false,
		},
		{
			name:     "should handle multiple months limit correctly - within limit",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 3, 15, 10, 0, 0, 0, loc),
			limit:    2,
			expected: true,
		},
		{
			name:     "should handle multiple months limit correctly - exceeds limit",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 4, 15, 10, 0, 0, 0, loc),
			limit:    2,
			expected: false,
		},
		{
			name:     "should handle year boundary correctly - within limit",
			initial:  time.Date(2024, 11, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2025, 1, 15, 10, 0, 0, 0, loc),
			limit:    2,
			expected: true,
		},
		{
			name:     "should handle year boundary correctly - exceeds limit",
			initial:  time.Date(2024, 11, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2025, 2, 15, 10, 0, 0, 0, loc),
			limit:    2,
			expected: false,
		},
		{
			name:     "should handle negative limit",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 2, 15, 10, 0, 0, 0, loc),
			limit:    -1,
			expected: false,
		},
		{
			name:     "should handle same day with different times - final before limit",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 15, 9, 0, 0, 0, loc),
			limit:    0,
			expected: true,
		},
		{
			name:     "should handle same day with different times - final after limit",
			initial:  time.Date(2024, 1, 15, 10, 0, 0, 0, loc),
			final:    time.Date(2024, 1, 15, 11, 0, 0, 0, loc),
			limit:    0,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsDateRangeWithinMonthLimit(tt.initial, tt.final, tt.limit)
			assert.Equal(t, tt.expected, result, "IsDateRangeWithinMonthLimit() result should match expected value")
		})
	}
}

func TestNormalizeDate(t *testing.T) {
	loc := time.UTC

	tests := []struct {
		name     string
		date     time.Time
		days     *int
		expected string
	}{
		{
			name:     "should format date without adding days when days is nil",
			date:     time.Date(2024, 1, 15, 14, 30, 45, 123456789, loc),
			days:     nil,
			expected: "2024-01-15",
		},
		{
			name:     "should add days when days is provided",
			date:     time.Date(2024, 1, 15, 14, 30, 45, 123456789, loc),
			days:     intPtr(5),
			expected: "2024-01-20",
		},
		{
			name:     "should subtract days when days is negative",
			date:     time.Date(2024, 1, 15, 14, 30, 45, 123456789, loc),
			days:     intPtr(-5),
			expected: "2024-01-10",
		},
		{
			name:     "should handle month boundary when adding days",
			date:     time.Date(2024, 1, 31, 14, 30, 45, 123456789, loc),
			days:     intPtr(1),
			expected: "2024-02-01",
		},
		{
			name:     "should handle year boundary when adding days",
			date:     time.Date(2024, 12, 31, 14, 30, 45, 123456789, loc),
			days:     intPtr(1),
			expected: "2025-01-01",
		},
		{
			name:     "should handle year boundary when subtracting days",
			date:     time.Date(2024, 1, 1, 14, 30, 45, 123456789, loc),
			days:     intPtr(-1),
			expected: "2023-12-31",
		},
		{
			name:     "should format date at start of day correctly",
			date:     time.Date(2024, 1, 15, 0, 0, 0, 0, loc),
			days:     nil,
			expected: "2024-01-15",
		},
		{
			name:     "should format date at end of day correctly",
			date:     time.Date(2024, 1, 15, 23, 59, 59, 999999999, loc),
			days:     nil,
			expected: "2024-01-15",
		},
		{
			name:     "should handle leap year correctly",
			date:     time.Date(2024, 2, 28, 14, 30, 45, 123456789, loc),
			days:     intPtr(1),
			expected: "2024-02-29",
		},
		{
			name:     "should handle non-leap year correctly",
			date:     time.Date(2023, 2, 28, 14, 30, 45, 123456789, loc),
			days:     intPtr(1),
			expected: "2023-03-01",
		},
		{
			name:     "should add zero days correctly",
			date:     time.Date(2024, 1, 15, 14, 30, 45, 123456789, loc),
			days:     intPtr(0),
			expected: "2024-01-15",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeDate(tt.date, tt.days)
			assert.Equal(t, tt.expected, result, "NormalizeDate() result should match expected value")
		})
	}
}

func TestParseDateTime(t *testing.T) {
	loc := time.UTC

	tests := []struct {
		name        string
		dateStr     string
		isEndDate   bool
		expected    time.Time
		hasTime     bool
		expectError bool
	}{
		{
			name:        "should parse RFC3339 format with time",
			dateStr:     "2024-01-15T14:30:45Z",
			isEndDate:   false,
			expected:    time.Date(2024, 1, 15, 14, 30, 45, 0, loc),
			hasTime:     true,
			expectError: false,
		},
		{
			name:        "should parse date and time format without Z",
			dateStr:     "2024-01-15T14:30:45",
			isEndDate:   false,
			expected:    time.Date(2024, 1, 15, 14, 30, 45, 0, time.UTC),
			hasTime:     true,
			expectError: false,
		},
		{
			name:        "should parse date and time format with space separator",
			dateStr:     "2024-01-15 14:30:45",
			isEndDate:   false,
			expected:    time.Date(2024, 1, 15, 14, 30, 45, 0, time.UTC),
			hasTime:     true,
			expectError: false,
		},
		{
			name:        "should parse date only format and set to start of day when isEndDate is false",
			dateStr:     "2024-01-15",
			isEndDate:   false,
			expected:    time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			hasTime:     false,
			expectError: false,
		},
		{
			name:        "should parse date only format and set to end of day when isEndDate is true",
			dateStr:     "2024-01-15",
			isEndDate:   true,
			expected:    time.Date(2024, 1, 15, 23, 59, 59, 0, time.UTC),
			hasTime:     false,
			expectError: false,
		},
		{
			name:        "should return error for invalid date format",
			dateStr:     "invalid-date",
			isEndDate:   false,
			expected:    time.Time{},
			hasTime:     false,
			expectError: true,
		},
		{
			name:        "should return error for empty string",
			dateStr:     "",
			isEndDate:   false,
			expected:    time.Time{},
			hasTime:     false,
			expectError: true,
		},
		{
			name:        "should return error for malformed date",
			dateStr:     "2024/01/15",
			isEndDate:   false,
			expected:    time.Time{},
			hasTime:     false,
			expectError: true,
		},
		{
			name:        "should return error for date with invalid time format",
			dateStr:     "2024-01-15 25:00:00",
			isEndDate:   false,
			expected:    time.Time{},
			hasTime:     false,
			expectError: true,
		},
		{
			name:        "should parse date with timezone offset in RFC3339",
			dateStr:     "2024-01-15T14:30:45+00:00",
			isEndDate:   false,
			expected:    time.Date(2024, 1, 15, 14, 30, 45, 0, time.FixedZone("", 0)),
			hasTime:     true,
			expectError: false,
		},
		{
			name:        "should handle leap year date correctly",
			dateStr:     "2024-02-29",
			isEndDate:   false,
			expected:    time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
			hasTime:     false,
			expectError: false,
		},
		{
			name:        "should return error for invalid leap year date",
			dateStr:     "2023-02-29",
			isEndDate:   false,
			expected:    time.Time{},
			hasTime:     false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, hasTime, err := ParseDateTime(tt.dateStr, tt.isEndDate)

			if tt.expectError {
				assert.Error(t, err, "ParseDateTime() should return error for invalid input")
				assert.False(t, hasTime, "ParseDateTime() should return hasTime=false when error occurs")
				assert.True(t, result.IsZero(), "ParseDateTime() should return zero time when error occurs")
			} else {
				assert.NoError(t, err, "ParseDateTime() should not return error for valid input")
				assert.Equal(t, tt.hasTime, hasTime, "ParseDateTime() hasTime should match expected value")
				assert.Equal(t, tt.expected.Year(), result.Year(), "ParseDateTime() year should match")
				assert.Equal(t, tt.expected.Month(), result.Month(), "ParseDateTime() month should match")
				assert.Equal(t, tt.expected.Day(), result.Day(), "ParseDateTime() day should match")
				assert.Equal(t, tt.expected.Hour(), result.Hour(), "ParseDateTime() hour should match")
				assert.Equal(t, tt.expected.Minute(), result.Minute(), "ParseDateTime() minute should match")
				assert.Equal(t, tt.expected.Second(), result.Second(), "ParseDateTime() second should match")
			}
		})
	}
}

func TestIsValidDateTime(t *testing.T) {
	tests := []struct {
		name     string
		date     string
		expected bool
	}{
		{
			name:     "should return true for valid date time format",
			date:     "2024-01-15 14:30:45",
			expected: true,
		},
		{
			name:     "should return true for valid date time at start of day",
			date:     "2024-01-15 00:00:00",
			expected: true,
		},
		{
			name:     "should return true for valid date time at end of day",
			date:     "2024-01-15 23:59:59",
			expected: true,
		},
		{
			name:     "should return false for date only format",
			date:     "2024-01-15",
			expected: false,
		},
		{
			name:     "should return false for RFC3339 format",
			date:     "2024-01-15T14:30:45Z",
			expected: false,
		},
		{
			name:     "should return false for invalid time format - missing seconds",
			date:     "2024-01-15 14:30",
			expected: false,
		},
		{
			name:     "should return false for invalid time format - invalid hour",
			date:     "2024-01-15 25:30:45",
			expected: false,
		},
		{
			name:     "should return false for invalid time format - invalid minute",
			date:     "2024-01-15 14:60:45",
			expected: false,
		},
		{
			name:     "should return false for invalid time format - invalid second",
			date:     "2024-01-15 14:30:60",
			expected: false,
		},
		{
			name:     "should return false for empty string",
			date:     "",
			expected: false,
		},
		{
			name:     "should return false for malformed date",
			date:     "2024/01/15 14:30:45",
			expected: false,
		},
		{
			name:     "should return false for invalid date",
			date:     "2024-13-15 14:30:45",
			expected: false,
		},
		{
			name:     "should return false for invalid date day",
			date:     "2024-01-32 14:30:45",
			expected: false,
		},
		{
			name:     "should return false for date time with single digit values",
			date:     "2024-1-5 1:2:3",
			expected: false,
		},
		{
			name:     "should return true for valid date time with leading zeros",
			date:     "2024-01-05 01:02:03",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidDateTime(tt.date)
			assert.Equal(t, tt.expected, result, "IsValidDateTime() result should match expected value")
		})
	}
}
