//go:build unit

package http

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		errIs             error
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
			name:          "limit zero uses default limit",
			queryString:   "limit=0",
			expectedLimit: 20,
		},
		{
			name:          "negative limit uses default limit",
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
			require.NoError(t, resp.Body.Close())

			if tc.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
				if tc.errIs != nil {
					assert.ErrorIs(t, err, tc.errIs)
				}

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

func TestParseTimestampCursorPagination_NilContext(t *testing.T) {
	t.Parallel()

	cursor, limit, err := ParseTimestampCursorPagination(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
	assert.Nil(t, cursor)
	assert.Zero(t, limit)
}

func TestEncodeTimestampCursor_Success(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	id := uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

	encoded, err := EncodeTimestampCursor(ts, id)
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)

	decoded, err := DecodeTimestampCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, ts, decoded.Timestamp)
	assert.Equal(t, id, decoded.ID)
}
