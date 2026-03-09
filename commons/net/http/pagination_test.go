//go:build unit

package http

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePagination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		queryString    string
		expectedLimit  int
		expectedOffset int
		expectedErr    error
		errContains    string
	}{
		{
			name:           "default values when no query params",
			queryString:    "",
			expectedLimit:  20,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "valid limit and offset",
			queryString:    "limit=10&offset=5",
			expectedLimit:  10,
			expectedOffset: 5,
			expectedErr:    nil,
		},
		{
			name:           "limit capped at maxLimit",
			queryString:    "limit=500",
			expectedLimit:  200,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "limit exactly at maxLimit",
			queryString:    "limit=200",
			expectedLimit:  200,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "limit just below maxLimit",
			queryString:    "limit=199",
			expectedLimit:  199,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "limit just above maxLimit gets capped",
			queryString:    "limit=201",
			expectedLimit:  200,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:        "invalid limit non-numeric",
			queryString: "limit=abc",
			expectedErr: nil,
			errContains: "invalid limit value",
		},
		{
			name:        "invalid offset non-numeric",
			queryString: "offset=xyz",
			expectedErr: nil,
			errContains: "invalid offset value",
		},
		{
			name:           "limit zero uses default",
			queryString:    "limit=0",
			expectedLimit:  20,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "negative limit uses default",
			queryString:    "limit=-5",
			expectedLimit:  20,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "negative offset coerces to default",
			queryString:    "limit=10&offset=-1",
			expectedLimit:  10,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "very large limit gets capped",
			queryString:    "limit=999999999",
			expectedLimit:  200,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "very large offset is valid",
			queryString:    "limit=10&offset=999999999",
			expectedLimit:  10,
			expectedOffset: 999999999,
			expectedErr:    nil,
		},
		{
			name:           "empty limit param uses default",
			queryString:    "limit=&offset=10",
			expectedLimit:  20,
			expectedOffset: 10,
			expectedErr:    nil,
		},
		{
			name:           "empty offset param uses default",
			queryString:    "limit=25&offset=",
			expectedLimit:  25,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "only limit provided",
			queryString:    "limit=75",
			expectedLimit:  75,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "only offset provided",
			queryString:    "offset=100",
			expectedLimit:  20,
			expectedOffset: 100,
			expectedErr:    nil,
		},
		{
			name:           "offset zero is valid",
			queryString:    "offset=0",
			expectedLimit:  20,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:           "limit one is valid minimum",
			queryString:    "limit=1",
			expectedLimit:  1,
			expectedOffset: 0,
			expectedErr:    nil,
		},
		{
			name:        "limit with decimal is invalid",
			queryString: "limit=10.5",
			errContains: "invalid limit value",
		},
		{
			name:        "offset with decimal is invalid",
			queryString: "offset=5.5",
			errContains: "invalid offset value",
		},
		{
			name:        "limit with special characters",
			queryString: "limit=10@#",
			errContains: "invalid limit value",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()

			var limit, offset int
			var err error

			app.Get("/test", func(c *fiber.Ctx) error {
				limit, offset, err = ParsePagination(c)
				return nil
			})

			req := httptest.NewRequest("GET", "/test?"+tc.queryString, nil)
			resp, testErr := app.Test(req)
			require.NoError(t, testErr)
			resp.Body.Close()

			if tc.expectedErr != nil {
				require.ErrorIs(t, err, tc.expectedErr)
				assert.Zero(t, limit)
				assert.Zero(t, offset)

				return
			}

			if tc.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
				assert.Zero(t, limit)
				assert.Zero(t, offset)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedLimit, limit)
			assert.Equal(t, tc.expectedOffset, offset)
		})
	}
}

func TestParseOpaqueCursorPagination(t *testing.T) {
	t.Parallel()

	opaqueCursor := "opaque-cursor-value"

	tests := []struct {
		name           string
		queryString    string
		expectedLimit  int
		expectedCursor string
		errContains    string
	}{
		{
			name:           "default values when no query params",
			queryString:    "",
			expectedLimit:  20,
			expectedCursor: "",
		},
		{
			name:           "valid limit only",
			queryString:    "limit=50",
			expectedLimit:  50,
			expectedCursor: "",
		},
		{
			name:           "valid cursor and limit",
			queryString:    "cursor=" + opaqueCursor + "&limit=30",
			expectedLimit:  30,
			expectedCursor: opaqueCursor,
		},
		{
			name:           "cursor only uses default limit",
			queryString:    "cursor=" + opaqueCursor,
			expectedLimit:  20,
			expectedCursor: opaqueCursor,
		},
		{
			name:           "limit capped at maxLimit",
			queryString:    "limit=500",
			expectedLimit:  200,
			expectedCursor: "",
		},
		{
			name:        "invalid limit non-numeric",
			queryString: "limit=abc",
			errContains: "invalid limit value",
		},
		{
			name:           "limit zero uses default",
			queryString:    "limit=0",
			expectedLimit:  20,
			expectedCursor: "",
		},
		{
			name:           "negative limit uses default",
			queryString:    "limit=-5",
			expectedLimit:  20,
			expectedCursor: "",
		},
		{
			name:           "opaque cursor is accepted without validation",
			queryString:    "cursor=not-base64-$$$",
			expectedLimit:  20,
			expectedCursor: "not-base64-$$$",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()

			var cursor string
			var limit int
			var err error

			app.Get("/test", func(c *fiber.Ctx) error {
				cursor, limit, err = ParseOpaqueCursorPagination(c)
				return nil
			})

			req := httptest.NewRequest("GET", "/test?"+tc.queryString, nil)
			resp, testErr := app.Test(req)
			require.NoError(t, testErr)
			resp.Body.Close()

			if tc.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedLimit, limit)
			assert.Equal(t, tc.expectedCursor, cursor)
		})
	}
}

func TestEncodeUUIDCursor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   uuid.UUID
	}{
		{
			name: "valid UUID",
			id:   uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		},
		{
			name: "nil UUID",
			id:   uuid.Nil,
		},
		{
			name: "random UUID",
			id:   uuid.New(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			encoded := EncodeUUIDCursor(tc.id)
			assert.NotEmpty(t, encoded)

			decoded, err := DecodeUUIDCursor(encoded)
			require.NoError(t, err)
			assert.Equal(t, tc.id, decoded)
		})
	}
}

func TestDecodeUUIDCursor(t *testing.T) {
	t.Parallel()

	validUUID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	validCursor := EncodeUUIDCursor(validUUID)

	tests := []struct {
		name        string
		cursor      string
		expected    uuid.UUID
		errContains string
	}{
		{
			name:     "valid cursor",
			cursor:   validCursor,
			expected: validUUID,
		},
		{
			name:        "invalid base64",
			cursor:      "not-valid-base64!!!",
			expected:    uuid.Nil,
			errContains: "decode failed",
		},
		{
			name:        "valid base64 but invalid UUID",
			cursor:      base64.StdEncoding.EncodeToString([]byte("not-a-uuid")),
			expected:    uuid.Nil,
			errContains: "parse failed",
		},
		{
			name:        "empty string",
			cursor:      "",
			expected:    uuid.Nil,
			errContains: "parse failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := DecodeUUIDCursor(tc.cursor)

			if tc.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
				assert.ErrorIs(t, err, ErrInvalidCursor)
				assert.Equal(t, uuid.Nil, decoded)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expected, decoded)
		})
	}
}

func TestEncodeTimestampCursor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		timestamp time.Time
		id        uuid.UUID
	}{
		{
			name:      "valid timestamp and UUID",
			timestamp: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
			id:        uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		},
		{
			name:      "zero timestamp",
			timestamp: time.Time{},
			id:        uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		},
		{
			name:      "non-UTC timestamp gets converted to UTC",
			timestamp: time.Date(2025, 1, 15, 10, 30, 0, 0, time.FixedZone("EST", -5*60*60)),
			id:        uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := EncodeTimestampCursor(tc.timestamp, tc.id)
			require.NoError(t, err)
			assert.NotEmpty(t, encoded)

			decoded, err := DecodeTimestampCursor(encoded)
			require.NoError(t, err)
			assert.Equal(t, tc.id, decoded.ID)
			assert.Equal(t, tc.timestamp.UTC(), decoded.Timestamp)
		})
	}
}

func TestDecodeTimestampCursor(t *testing.T) {
	t.Parallel()

	validTimestamp := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	validID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	validCursor, encErr := EncodeTimestampCursor(validTimestamp, validID)
	require.NoError(t, encErr)

	tests := []struct {
		name              string
		cursor            string
		expectedTimestamp time.Time
		expectedID        uuid.UUID
		errContains       string
	}{
		{
			name:              "valid cursor",
			cursor:            validCursor,
			expectedTimestamp: validTimestamp,
			expectedID:        validID,
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
			cursor:      base64.StdEncoding.EncodeToString([]byte(`{"t":"2025-01-15T10:30:00Z"}`)),
			errContains: "missing id",
		},
		{
			name: "valid JSON with nil UUID",
			cursor: base64.StdEncoding.EncodeToString(
				[]byte(`{"t":"2025-01-15T10:30:00Z","i":"00000000-0000-0000-0000-000000000000"}`),
			),
			errContains: "missing id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := DecodeTimestampCursor(tc.cursor)

			if tc.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
				assert.ErrorIs(t, err, ErrInvalidCursor)
				assert.Nil(t, decoded)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, decoded)
			assert.Equal(t, tc.expectedTimestamp, decoded.Timestamp)
			assert.Equal(t, tc.expectedID, decoded.ID)
		})
	}
}

func TestParseTimestampCursorPagination(t *testing.T) {
	t.Parallel()

	validTimestamp := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	validID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	validCursor, encErr := EncodeTimestampCursor(validTimestamp, validID)
	require.NoError(t, encErr)

	tests := []struct {
		name              string
		queryString       string
		expectedLimit     int
		expectedTimestamp *time.Time
		expectedID        *uuid.UUID
		errContains       string
	}{
		{
			name:          "default values when no query params",
			queryString:   "",
			expectedLimit: 20,
		},
		{
			name:          "valid limit only",
			queryString:   "limit=50",
			expectedLimit: 50,
		},
		{
			name:              "valid cursor and limit",
			queryString:       "cursor=" + validCursor + "&limit=30",
			expectedLimit:     30,
			expectedTimestamp: &validTimestamp,
			expectedID:        &validID,
		},
		{
			name:              "cursor only uses default limit",
			queryString:       "cursor=" + validCursor,
			expectedLimit:     20,
			expectedTimestamp: &validTimestamp,
			expectedID:        &validID,
		},
		{
			name:          "limit capped at maxLimit",
			queryString:   "limit=500",
			expectedLimit: 200,
		},
		{
			name:        "invalid limit non-numeric",
			queryString: "limit=abc",
			errContains: "invalid limit value",
		},
		{
			name:          "limit zero uses default",
			queryString:   "limit=0",
			expectedLimit: 20,
		},
		{
			name:          "negative limit uses default",
			queryString:   "limit=-5",
			expectedLimit: 20,
		},
		{
			name:        "invalid cursor",
			queryString: "cursor=invalid",
			errContains: "invalid cursor format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()

			var cursor *TimestampCursor
			var limit int
			var err error

			app.Get("/test", func(c *fiber.Ctx) error {
				cursor, limit, err = ParseTimestampCursorPagination(c)
				return nil
			})

			req := httptest.NewRequest("GET", "/test?"+tc.queryString, nil)
			resp, testErr := app.Test(req)
			require.NoError(t, testErr)
			resp.Body.Close()

			if tc.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedLimit, limit)

			if tc.expectedTimestamp == nil {
				assert.Nil(t, cursor)
			} else {
				require.NotNil(t, cursor)
				assert.Equal(t, *tc.expectedTimestamp, cursor.Timestamp)
				assert.Equal(t, *tc.expectedID, cursor.ID)
			}
		})
	}
}

func TestTimestampCursor_RoundTrip(t *testing.T) {
	t.Parallel()

	// Use fixed deterministic values for reproducible tests
	timestamp := time.Date(2025, 6, 15, 14, 30, 45, 0, time.UTC)
	id := uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

	encoded, encErr := EncodeTimestampCursor(timestamp, id)
	require.NoError(t, encErr)
	decoded, err := DecodeTimestampCursor(encoded)

	require.NoError(t, err)
	require.NotNil(t, decoded)
	assert.Equal(t, timestamp, decoded.Timestamp)
	assert.Equal(t, id, decoded.ID)
}

func TestPaginationConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 20, cn.DefaultLimit)
	assert.Equal(t, 0, cn.DefaultOffset)
	assert.Equal(t, 200, cn.MaxLimit)
}

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

// ---------------------------------------------------------------------------
// Nil guard tests
// ---------------------------------------------------------------------------

func TestParsePagination_NilContext(t *testing.T) {
	t.Parallel()

	limit, offset, err := ParsePagination(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
	assert.Zero(t, limit)
	assert.Zero(t, offset)
}

func TestParseOpaqueCursorPagination_NilContext(t *testing.T) {
	t.Parallel()

	cursor, limit, err := ParseOpaqueCursorPagination(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
	assert.Empty(t, cursor)
	assert.Zero(t, limit)
}

func TestParseTimestampCursorPagination_NilContext(t *testing.T) {
	t.Parallel()

	cursor, limit, err := ParseTimestampCursorPagination(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
	assert.Nil(t, cursor)
	assert.Zero(t, limit)
}

// ---------------------------------------------------------------------------
// Lenient negative offset coercion
// ---------------------------------------------------------------------------

func TestParsePagination_NegativeOffsetCoercesToZero(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	var limit, offset int
	var err error

	app.Get("/test", func(c *fiber.Ctx) error {
		limit, offset, err = ParsePagination(c)
		return nil
	})

	req := httptest.NewRequest("GET", "/test?limit=10&offset=-100", nil)
	resp, testErr := app.Test(req)
	require.NoError(t, testErr)
	resp.Body.Close()

	require.NoError(t, err)
	assert.Equal(t, 10, limit)
	assert.Equal(t, 0, offset, "negative offset should be coerced to 0 (DefaultOffset)")
}

// ---------------------------------------------------------------------------
// EncodeTimestampCursor and EncodeSortCursor return proper errors
// ---------------------------------------------------------------------------

func TestEncodeTimestampCursor_Success(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	id := uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

	encoded, err := EncodeTimestampCursor(ts, id)
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)

	// Verify round-trip
	decoded, err := DecodeTimestampCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, ts, decoded.Timestamp)
	assert.Equal(t, id, decoded.ID)
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

	// EncodeSortCursor now validates that sortColumn is non-empty,
	// matching the decoder's validation contract.
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
