package commons

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		slice    any
		item     any
		expected bool
	}{
		{
			name:     "String slice - item exists",
			slice:    []string{"apple", "banana", "orange"},
			item:     "banana",
			expected: true,
		},
		{
			name:     "String slice - item not exists",
			slice:    []string{"apple", "banana", "orange"},
			item:     "grape",
			expected: false,
		},
		{
			name:     "Int slice - item exists",
			slice:    []int{1, 2, 3, 4, 5},
			item:     3,
			expected: true,
		},
		{
			name:     "Empty slice",
			slice:    []string{},
			item:     "test",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switch s := tt.slice.(type) {
			case []string:
				result := Contains(s, tt.item.(string))
				assert.Equal(t, tt.expected, result)
			case []int:
				result := Contains(s, tt.item.(int))
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestReverse(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected any
	}{
		{
			name:     "Int slice",
			input:    []int{1, 2, 3, 4, 5},
			expected: []int{5, 4, 3, 2, 1},
		},
		{
			name:     "String slice",
			input:    []string{"a", "b", "c", "d"},
			expected: []string{"d", "c", "b", "a"},
		},
		{
			name:     "Empty slice",
			input:    []int{},
			expected: []int{},
		},
		{
			name:     "Single element",
			input:    []string{"a"},
			expected: []string{"a"},
		},
		{
			name:     "Two elements",
			input:    []int{1, 2},
			expected: []int{2, 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switch v := tt.input.(type) {
			case []int:
				result := Reverse(v)
				assert.Equal(t, tt.expected, result)
			case []string:
				result := Reverse(v)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// Benchmarks
func BenchmarkContains(b *testing.B) {
	slice := []string{"apple", "banana", "orange", "grape", "kiwi", "mango", "peach", "pear"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Contains(slice, "mango")
		_ = Contains(slice, "notfound")
	}
}

func BenchmarkReverse(b *testing.B) {
	slice := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sliceCopy := make([]int, len(slice))
		copy(sliceCopy, slice)
		_ = Reverse(sliceCopy)
	}
}
