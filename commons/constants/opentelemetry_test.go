//go:build unit

package constant

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeMetricLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string returns empty",
			input: "",
			want:  "",
		},
		{
			name:  "short string returned as-is",
			input: "short",
			want:  "short",
		},
		{
			name:  "exactly 64 chars returned as-is",
			input: strings.Repeat("x", 64),
			want:  strings.Repeat("x", 64),
		},
		{
			name:  "65 chars truncated to 64",
			input: strings.Repeat("y", 65),
			want:  strings.Repeat("y", 64),
		},
		{
			name:  "100 chars truncated to 64",
			input: strings.Repeat("z", 100),
			want:  strings.Repeat("z", 64),
		},
		{
			name:  "single character returned as-is",
			input: "a",
			want:  "a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := SanitizeMetricLabel(tt.input)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, len(got), MaxMetricLabelLength,
				"result length must never exceed MaxMetricLabelLength")
		})
	}
}

func TestMaxMetricLabelLength_Value(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 64, MaxMetricLabelLength,
		"MaxMetricLabelLength must be 64 to match OTEL cardinality safeguards")
}
