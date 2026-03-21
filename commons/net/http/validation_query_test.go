//go:build unit

package http

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSortDirection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "uppercase ASC", input: "ASC", want: "ASC"},
		{name: "uppercase DESC", input: "DESC", want: "DESC"},
		{name: "lowercase asc", input: "asc", want: "ASC"},
		{name: "lowercase desc", input: "desc", want: "DESC"},
		{name: "mixed case Asc", input: "Asc", want: "ASC"},
		{name: "mixed case Desc", input: "Desc", want: "DESC"},
		{name: "empty string defaults to ASC", input: "", want: "ASC"},
		{name: "whitespace only defaults to ASC", input: "   ", want: "ASC"},
		{name: "with leading whitespace", input: "  DESC", want: "DESC"},
		{name: "with trailing whitespace", input: "ASC  ", want: "ASC"},
		{name: "invalid value defaults to ASC", input: "INVALID", want: "ASC"},
		{name: "SQL injection attempt defaults to ASC", input: "ASC; DROP TABLE users;--", want: "ASC"},
		{name: "partial match defaults to ASC", input: "ASCENDING", want: "ASC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ValidateSortDirection(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		limit        int
		defaultLimit int
		maxLimit     int
		expected     int
	}{
		{"zero uses default", 0, 20, 100, 20},
		{"negative uses default", -5, 20, 100, 20},
		{"valid limit unchanged", 50, 20, 100, 50},
		{"exceeds max capped", 150, 20, 100, 100},
		{"equals max unchanged", 100, 20, 100, 100},
		{"equals default", 20, 20, 100, 20},
		{"min valid (1)", 1, 20, 100, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := ValidateLimit(tc.limit, tc.defaultLimit, tc.maxLimit)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestValidateQueryParamLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       string
		paramName   string
		maxLen      int
		wantErr     bool
		errContains string
	}{
		{
			name:      "value within limit",
			value:     "CREATE",
			paramName: "action",
			maxLen:    50,
			wantErr:   false,
		},
		{
			name:      "value at exact limit",
			value:     strings.Repeat("a", 50),
			paramName: "action",
			maxLen:    50,
			wantErr:   false,
		},
		{
			name:        "value exceeds limit",
			value:       strings.Repeat("a", 51),
			paramName:   "action",
			maxLen:      50,
			wantErr:     true,
			errContains: "'action' must be at most 50 characters",
		},
		{
			name:      "empty value always valid",
			value:     "",
			paramName: "actor",
			maxLen:    255,
			wantErr:   false,
		},
		{
			name:        "long value exceeds short limit",
			value:       strings.Repeat("x", 256),
			paramName:   "entity_type",
			maxLen:      255,
			wantErr:     true,
			errContains: "'entity_type' must be at most 255 characters",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateQueryParamLength(tc.value, tc.paramName, tc.maxLen)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrQueryParamTooLong)
				assert.Contains(t, err.Error(), tc.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
