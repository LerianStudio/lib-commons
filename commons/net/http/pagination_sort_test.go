//go:build unit

package http

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeSortCursor_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		sortColumn string
		sortValue  string
		id         string
		pointsNext bool
	}{
		{
			name:       "timestamp column forward",
			sortColumn: "created_at",
			sortValue:  "2025-06-15T14:30:45Z",
			id:         "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			pointsNext: true,
		},
		{
			name:       "status column backward",
			sortColumn: "status",
			sortValue:  "COMPLETED",
			id:         "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			pointsNext: false,
		},
		{
			name:       "empty sort value",
			sortColumn: "completed_at",
			sortValue:  "",
			id:         "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			pointsNext: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := EncodeSortCursor(tc.sortColumn, tc.sortValue, tc.id, tc.pointsNext)
			require.NoError(t, err)
			assert.NotEmpty(t, encoded)

			decoded, err := DecodeSortCursor(encoded)
			require.NoError(t, err)
			require.NotNil(t, decoded)
			assert.Equal(t, tc.sortColumn, decoded.SortColumn)
			assert.Equal(t, tc.sortValue, decoded.SortValue)
			assert.Equal(t, tc.id, decoded.ID)
			assert.Equal(t, tc.pointsNext, decoded.PointsNext)
		})
	}
}

func TestDecodeSortCursor_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cursor      string
		errContains string
	}{
		{
			name:        "empty string",
			cursor:      "",
			errContains: "unmarshal failed",
		},
		{
			name:        "whitespace only",
			cursor:      "   ",
			errContains: "decode failed",
		},
		{
			name:        "invalid base64",
			cursor:      "not-valid-base64!!!",
			errContains: "decode failed",
		},
		{
			name:        "valid base64 but invalid JSON",
			cursor:      base64.StdEncoding.EncodeToString([]byte("not-json")),
			errContains: "unmarshal failed",
		},
		{
			name:        "valid JSON but missing ID",
			cursor:      base64.StdEncoding.EncodeToString([]byte(`{"sc":"created_at","sv":"2025-01-01","pn":true}`)),
			errContains: "missing id",
		},
		{
			name:        "invalid sort column",
			cursor:      base64.StdEncoding.EncodeToString([]byte(`{"sc":"created_at;DROP TABLE users","sv":"2025-01-01","i":"abc","pn":true}`)),
			errContains: "invalid sort column",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := DecodeSortCursor(tc.cursor)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidCursor)
			assert.Contains(t, err.Error(), tc.errContains)
			assert.Nil(t, decoded)
		})
	}
}

func TestSortCursorDirection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		requestedDir string
		pointsNext   bool
		expectedDir  string
		expectedOp   string
	}{
		{
			name:         "ASC forward",
			requestedDir: "ASC",
			pointsNext:   true,
			expectedDir:  "ASC",
			expectedOp:   ">",
		},
		{
			name:         "DESC forward",
			requestedDir: "DESC",
			pointsNext:   true,
			expectedDir:  "DESC",
			expectedOp:   "<",
		},
		{
			name:         "ASC backward",
			requestedDir: "ASC",
			pointsNext:   false,
			expectedDir:  "DESC",
			expectedOp:   "<",
		},
		{
			name:         "DESC backward",
			requestedDir: "DESC",
			pointsNext:   false,
			expectedDir:  "ASC",
			expectedOp:   ">",
		},
		{
			name:         "lowercase asc forward",
			requestedDir: "asc",
			pointsNext:   true,
			expectedDir:  "ASC",
			expectedOp:   ">",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			actualDir, operator := SortCursorDirection(tc.requestedDir, tc.pointsNext)
			assert.Equal(t, tc.expectedDir, actualDir)
			assert.Equal(t, tc.expectedOp, operator)
		})
	}
}

func TestCalculateSortCursorPagination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		isFirstPage   bool
		hasPagination bool
		pointsNext    bool
		expectNext    bool
		expectPrev    bool
	}{
		{
			name:          "first page with more results",
			isFirstPage:   true,
			hasPagination: true,
			pointsNext:    true,
			expectNext:    true,
			expectPrev:    false,
		},
		{
			name:          "middle page forward",
			isFirstPage:   false,
			hasPagination: true,
			pointsNext:    true,
			expectNext:    true,
			expectPrev:    true,
		},
		{
			name:          "last page forward",
			isFirstPage:   false,
			hasPagination: false,
			pointsNext:    true,
			expectNext:    false,
			expectPrev:    true,
		},
		{
			name:          "first page no more results",
			isFirstPage:   true,
			hasPagination: false,
			pointsNext:    true,
			expectNext:    false,
			expectPrev:    false,
		},
		{
			name:          "backward navigation with more",
			isFirstPage:   false,
			hasPagination: true,
			pointsNext:    false,
			expectNext:    true,
			expectPrev:    true,
		},
		{
			name:          "backward navigation at start",
			isFirstPage:   true,
			hasPagination: false,
			pointsNext:    false,
			expectNext:    true,
			expectPrev:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			next, prev, calcErr := CalculateSortCursorPagination(
				tc.isFirstPage, tc.hasPagination, tc.pointsNext,
				"created_at",
				"2025-01-01T00:00:00Z", "id-first",
				"2025-01-02T00:00:00Z", "id-last",
			)
			require.NoError(t, calcErr)

			if tc.expectNext {
				assert.NotEmpty(t, next, "expected next cursor")

				decoded, err := DecodeSortCursor(next)
				require.NoError(t, err)
				assert.Equal(t, "created_at", decoded.SortColumn)
				assert.True(t, decoded.PointsNext)
			} else {
				assert.Empty(t, next, "expected no next cursor")
			}

			if tc.expectPrev {
				assert.NotEmpty(t, prev, "expected prev cursor")

				decoded, err := DecodeSortCursor(prev)
				require.NoError(t, err)
				assert.Equal(t, "created_at", decoded.SortColumn)
				assert.False(t, decoded.PointsNext)
			} else {
				assert.Empty(t, prev, "expected no prev cursor")
			}
		})
	}
}

func TestValidateSortColumn(t *testing.T) {
	t.Parallel()

	allowed := []string{"id", "created_at", "status"}

	tests := []struct {
		name     string
		column   string
		expected string
	}{
		{
			name:     "exact match returns allowed value",
			column:   "created_at",
			expected: "created_at",
		},
		{
			name:     "case insensitive match uppercase",
			column:   "CREATED_AT",
			expected: "created_at",
		},
		{
			name:     "case insensitive match mixed case",
			column:   "Status",
			expected: "status",
		},
		{
			name:     "empty column returns default",
			column:   "",
			expected: "id",
		},
		{
			name:     "unknown column returns default",
			column:   "nonexistent",
			expected: "id",
		},
		{
			name:     "id returns id",
			column:   "id",
			expected: "id",
		},
		{
			name:     "sql injection attempt returns default",
			column:   "id; DROP TABLE--",
			expected: "id",
		},
		{
			name:     "whitespace only returns default",
			column:   "   ",
			expected: "id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := ValidateSortColumn(tc.column, allowed, "id")
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestValidateSortColumn_EmptyAllowed(t *testing.T) {
	t.Parallel()

	result := ValidateSortColumn("anything", nil, "fallback")
	assert.Equal(t, "fallback", result)
}

func TestValidateSortColumn_CustomDefault(t *testing.T) {
	t.Parallel()

	result := ValidateSortColumn("unknown", []string{"name"}, "created_at")
	assert.Equal(t, "created_at", result)
}

func TestEncodeSortCursor_Success(t *testing.T) {
	t.Parallel()

	encoded, err := EncodeSortCursor("created_at", "2025-01-01", "some-id", true)
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)

	decoded, err := DecodeSortCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, "created_at", decoded.SortColumn)
	assert.Equal(t, "2025-01-01", decoded.SortValue)
	assert.Equal(t, "some-id", decoded.ID)
	assert.True(t, decoded.PointsNext)
}

func TestEncodeSortCursor_EmptySortColumn_RejectsAtEncodeTime(t *testing.T) {
	t.Parallel()

	encoded, err := EncodeSortCursor("", "value", "id-1", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	assert.Contains(t, err.Error(), "sort column must not be empty")
	assert.Empty(t, encoded)
}

func TestEncodeSortCursor_EmptyID_RejectsAtEncodeTime(t *testing.T) {
	t.Parallel()

	encoded, err := EncodeSortCursor("created_at", "value", "", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	assert.Contains(t, err.Error(), "id must not be empty")
	assert.Empty(t, encoded)
}
