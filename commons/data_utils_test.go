package commons

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsUUID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "Valid UUID v4",
			input:    "550e8400-e29b-41d4-a716-446655440000",
			expected: true,
		},
		{
			name:     "Valid UUID v7",
			input:    "01902d91-7082-72f9-a011-de1aae71b41a",
			expected: true,
		},
		{
			name:     "Invalid UUID - wrong format",
			input:    "550e8400-e29b-41d4-a716",
			expected: false,
		},
		{
			name:     "Invalid UUID - not hex",
			input:    "550e8400-e29b-41d4-a716-44665544000g",
			expected: false,
		},
		{
			name:     "Empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "Random string",
			input:    "not-a-uuid",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsUUID(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateUUIDv7(t *testing.T) {
	// Generate multiple UUIDs
	uuids := make(map[string]bool)
	count := 100

	for i := 0; i < count; i++ {
		uuid := GenerateUUIDv7()

		// Check it's not nil
		require.NotNil(t, uuid)

		// Check it's not empty
		assert.NotEmpty(t, uuid.String())

		// Check for uniqueness
		_, exists := uuids[uuid.String()]
		assert.False(t, exists, "Duplicate UUID generated: %s", uuid.String())
		uuids[uuid.String()] = true

		// Check it's a valid UUID v7 format (version bits should be 0111)
		assert.Regexp(
			t,
			`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$`,
			uuid.String(),
		)
	}
}

func TestStructToJSONString(t *testing.T) {
	type testStruct struct {
		Name  string `json:"name"`
		Age   int    `json:"age"`
		Email string `json:"email"`
	}

	tests := []struct {
		name        string
		input       any
		expected    string
		expectError bool
	}{
		{
			name: "Valid struct",
			input: testStruct{
				Name:  "John Doe",
				Age:   30,
				Email: "john@example.com",
			},
			expected:    `{"name":"John Doe","age":30,"email":"john@example.com"}`,
			expectError: false,
		},
		{
			name:        "Empty struct",
			input:       testStruct{},
			expected:    `{"name":"","age":0,"email":""}`,
			expectError: false,
		},
		{
			name:        "Map",
			input:       map[string]string{"key": "value"},
			expected:    `{"key":"value"}`,
			expectError: false,
		},
		{
			name:        "Slice",
			input:       []int{1, 2, 3},
			expected:    `[1,2,3]`,
			expectError: false,
		},
		{
			name:        "Nil",
			input:       nil,
			expected:    `null`,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := StructToJSONString(tt.input)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.JSONEq(t, tt.expected, result)
			}
		})
	}
}

func TestMergeMaps(t *testing.T) {
	tests := []struct {
		name     string
		source   map[string]any
		target   map[string]any
		expected map[string]any
	}{
		{
			name:     "Simple merge",
			source:   map[string]any{"a": 1, "b": 2},
			target:   map[string]any{"c": 3, "d": 4},
			expected: map[string]any{"a": 1, "b": 2, "c": 3, "d": 4},
		},
		{
			name:     "Override existing keys",
			source:   map[string]any{"a": 5, "b": 6},
			target:   map[string]any{"a": 1, "b": 2, "c": 3},
			expected: map[string]any{"a": 5, "b": 6, "c": 3},
		},
		{
			name:     "Delete with nil value",
			source:   map[string]any{"a": nil, "b": 2},
			target:   map[string]any{"a": 1, "c": 3},
			expected: map[string]any{"b": 2, "c": 3},
		},
		{
			name:     "Empty source",
			source:   map[string]any{},
			target:   map[string]any{"a": 1, "b": 2},
			expected: map[string]any{"a": 1, "b": 2},
		},
		{
			name:     "Empty target",
			source:   map[string]any{"a": 1, "b": 2},
			target:   map[string]any{},
			expected: map[string]any{"a": 1, "b": 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeMaps(tt.source, tt.target)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetMapNumKinds(t *testing.T) {
	numKinds := GetMapNumKinds()

	// Check all expected numeric kinds are present
	expectedKinds := []reflect.Kind{
		reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Float32,
		reflect.Float64,
	}

	for _, kind := range expectedKinds {
		assert.True(t, numKinds[kind], "Expected kind %v to be in map", kind)
	}

	// Check map has exactly the expected number of entries
	assert.Len(t, numKinds, len(expectedKinds))

	// Check non-numeric kinds are not present
	assert.False(t, numKinds[reflect.String])
	assert.False(t, numKinds[reflect.Bool])
	assert.False(t, numKinds[reflect.Uint])
}

// Benchmarks
func BenchmarkGenerateUUIDv7(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GenerateUUIDv7()
	}
}

func BenchmarkStructToJSONString(b *testing.B) {
	type testStruct struct {
		Name  string `json:"name"`
		Age   int    `json:"age"`
		Email string `json:"email"`
	}

	data := testStruct{
		Name:  "John Doe",
		Age:   30,
		Email: "john@example.com",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = StructToJSONString(data)
	}
}

func BenchmarkMergeMaps(b *testing.B) {
	source := map[string]any{
		"a": 1, "b": 2, "c": 3, "d": 4, "e": 5,
	}
	target := map[string]any{
		"f": 6, "g": 7, "h": 8, "i": 9, "j": 10,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = MergeMaps(source, target)
	}
}
