//go:build unit

package runtime

import (
	"strings"
	"testing"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/stretchr/testify/assert"
)

// TestSanitizeMetricLabel tests the shared constant.SanitizeMetricLabel function.
func TestSanitizeMetricLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "short string",
			input:    "component",
			expected: "component",
		},
		{
			name:     "exactly max length",
			input:    strings.Repeat("a", constant.MaxMetricLabelLength),
			expected: strings.Repeat("a", constant.MaxMetricLabelLength),
		},
		{
			name:     "exceeds max length",
			input:    strings.Repeat("b", constant.MaxMetricLabelLength+10),
			expected: strings.Repeat("b", constant.MaxMetricLabelLength),
		},
		{
			name:     "much longer than max",
			input:    strings.Repeat("c", 200),
			expected: strings.Repeat("c", constant.MaxMetricLabelLength),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := constant.SanitizeMetricLabel(tt.input)
			assert.Equal(t, tt.expected, result)
			assert.LessOrEqual(t, len(result), constant.MaxMetricLabelLength)
		})
	}
}
