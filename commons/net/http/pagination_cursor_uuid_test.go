//go:build unit

package http

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOpaqueCursorPagination(t *testing.T) {
	t.Parallel()

	opaqueCursor := "opaque-cursor-value"

	tests := []struct {
		name           string
		queryString    string
		expectedLimit  int
		expectedCursor string
		errContains    string
		errIs          error
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
			name:           "limit zero uses default limit",
			queryString:    "limit=0",
			expectedLimit:  20,
			expectedCursor: "",
		},
		{
			name:           "negative limit uses default limit",
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

func TestParseOpaqueCursorPagination_NilContext(t *testing.T) {
	t.Parallel()

	cursor, limit, err := ParseOpaqueCursorPagination(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
	assert.Empty(t, cursor)
	assert.Zero(t, limit)
}
