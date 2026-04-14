//go:build unit

package http

import (
	"net/http/httptest"
	"testing"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/gofiber/fiber/v2"
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
			require.NoError(t, resp.Body.Close())

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

func TestPaginationConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 20, cn.DefaultLimit)
	assert.Equal(t, 0, cn.DefaultOffset)
	assert.Equal(t, 200, cn.MaxLimit)
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
	require.NoError(t, resp.Body.Close())

	require.NoError(t, err)
	assert.Equal(t, 10, limit)
	assert.Equal(t, 0, offset, "negative offset should be coerced to 0 (DefaultOffset)")
}
