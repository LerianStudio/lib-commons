package commons

import (
	"math"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestCheckMetadataKeyAndValueLength(t *testing.T) {
	tests := []struct {
		name        string
		limit       int
		metadata    map[string]any
		expectError bool
		errorCode   string
	}{
		{
			name:  "Valid metadata",
			limit: 10,
			metadata: map[string]any{
				"key1": "value1",
				"key2": 123,
				"key3": true,
			},
			expectError: false,
		},
		{
			name:  "Key too long",
			limit: 5,
			metadata: map[string]any{
				"verylongkey": "value",
			},
			expectError: true,
			errorCode:   "0050",
		},
		{
			name:  "Value too long",
			limit: 5,
			metadata: map[string]any{
				"key": "verylongvalue",
			},
			expectError: true,
			errorCode:   "0051",
		},
		{
			name:  "Float value",
			limit: 10,
			metadata: map[string]any{
				"key": 123.456,
			},
			expectError: false,
		},
		{
			name:  "Boolean value",
			limit: 10,
			metadata: map[string]any{
				"key": false,
			},
			expectError: false,
		},
		{
			name:        "Empty metadata",
			limit:       10,
			metadata:    map[string]any{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckMetadataKeyAndValueLength(tt.limit, tt.metadata)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, tt.errorCode, err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCountryAddress(t *testing.T) {
	tests := []struct {
		name        string
		country     string
		expectError bool
	}{
		{
			name:        "Valid country - US",
			country:     "US",
			expectError: false,
		},
		{
			name:        "Valid country - BR",
			country:     "BR",
			expectError: false,
		},
		{
			name:        "Valid country - GB",
			country:     "GB",
			expectError: false,
		},
		{
			name:        "Invalid country code",
			country:     "XX",
			expectError: true,
		},
		{
			name:        "Lowercase country code",
			country:     "us",
			expectError: true,
		},
		{
			name:        "Empty string",
			country:     "",
			expectError: true,
		},
		{
			name:        "Too long code",
			country:     "USA",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCountryAddress(tt.country)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "0032", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateAccountType(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		expectError bool
	}{
		{
			name:        "Valid type - deposit",
			accountType: "deposit",
			expectError: false,
		},
		{
			name:        "Valid type - savings",
			accountType: "savings",
			expectError: false,
		},
		{
			name:        "Valid type - loans",
			accountType: "loans",
			expectError: false,
		},
		{
			name:        "Valid type - marketplace",
			accountType: "marketplace",
			expectError: false,
		},
		{
			name:        "Valid type - creditCard",
			accountType: "creditCard",
			expectError: false,
		},
		{
			name:        "Invalid type",
			accountType: "checking",
			expectError: true,
		},
		{
			name:        "Empty string",
			accountType: "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAccountType(tt.accountType)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "0066", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateType(t *testing.T) {
	tests := []struct {
		name        string
		assetType   string
		expectError bool
	}{
		{
			name:        "Valid type - crypto",
			assetType:   "crypto",
			expectError: false,
		},
		{
			name:        "Valid type - currency",
			assetType:   "currency",
			expectError: false,
		},
		{
			name:        "Valid type - commodity",
			assetType:   "commodity",
			expectError: false,
		},
		{
			name:        "Valid type - others",
			assetType:   "others",
			expectError: false,
		},
		{
			name:        "Invalid type",
			assetType:   "stock",
			expectError: true,
		},
		{
			name:        "Empty string",
			assetType:   "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateType(tt.assetType)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "0040", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCode(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		expectError bool
		errorCode   string
	}{
		{
			name:        "Valid uppercase code",
			code:        "USD",
			expectError: false,
		},
		{
			name:        "Valid single letter",
			code:        "A",
			expectError: false,
		},
		{
			name:        "Lowercase letters",
			code:        "usd",
			expectError: true,
			errorCode:   "0004",
		},
		{
			name:        "Mixed case",
			code:        "UsD",
			expectError: true,
			errorCode:   "0004",
		},
		{
			name:        "Contains numbers",
			code:        "US1",
			expectError: true,
			errorCode:   "0033",
		},
		{
			name:        "Contains special characters",
			code:        "US-D",
			expectError: true,
			errorCode:   "0033",
		},
		{
			name:        "Empty string",
			code:        "",
			expectError: false, // Empty string passes as it has no characters
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCode(tt.code)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, tt.errorCode, err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCurrency(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		expectError bool
	}{
		{
			name:        "Valid currency - USD",
			code:        "USD",
			expectError: false,
		},
		{
			name:        "Valid currency - EUR",
			code:        "EUR",
			expectError: false,
		},
		{
			name:        "Valid currency - BRL",
			code:        "BRL",
			expectError: false,
		},
		{
			name:        "Invalid currency code",
			code:        "XXX",
			expectError: true,
		},
		{
			name:        "Lowercase currency",
			code:        "usd",
			expectError: true,
		},
		{
			name:        "Empty string",
			code:        "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCurrency(tt.code)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "0005", err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSafeIntToUint64(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected uint64
	}{
		{
			name:     "Positive number",
			input:    100,
			expected: 100,
		},
		{
			name:     "Zero",
			input:    0,
			expected: 0,
		},
		{
			name:     "Negative number",
			input:    -100,
			expected: 1,
		},
		{
			name:     "Large positive number",
			input:    math.MaxInt32,
			expected: uint64(math.MaxInt32),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafeIntToUint64(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSafeInt64ToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected int
	}{
		{
			name:     "Normal positive value",
			input:    100,
			expected: 100,
		},
		{
			name:     "Zero",
			input:    0,
			expected: 0,
		},
		{
			name:     "Negative value",
			input:    -100,
			expected: -100,
		},
		{
			name:     "Max int value",
			input:    int64(math.MaxInt),
			expected: math.MaxInt,
		},
		{
			name:     "Min int value",
			input:    int64(math.MinInt),
			expected: math.MinInt,
		},
		{
			name:     "Overflow positive",
			input:    math.MaxInt64,
			expected: math.MaxInt,
		},
		{
			name:     "Overflow negative",
			input:    math.MinInt64,
			expected: math.MinInt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafeInt64ToInt(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSafeUintToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    uint
		expected int
	}{
		{
			name:     "Normal value",
			input:    100,
			expected: 100,
		},
		{
			name:     "Zero",
			input:    0,
			expected: 0,
		},
		{
			name:     "Max safe value",
			input:    uint(math.MaxInt),
			expected: math.MaxInt,
		},
		{
			name:     "Overflow",
			input:    uint(math.MaxInt) + 1,
			expected: math.MaxInt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafeUintToInt(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

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
		assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$`, uuid.String())
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

func TestInternalKey(t *testing.T) {
	orgID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	ledgerID := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

	tests := []struct {
		name     string
		orgID    uuid.UUID
		ledgerID uuid.UUID
		key      string
		expected string
	}{
		{
			name:     "Normal key",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "test-key",
			expected: "550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:test-key",
		},
		{
			name:     "Empty key",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "",
			expected: "550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:",
		},
		{
			name:     "Key with special characters",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "key:with:colons",
			expected: "550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:key:with:colons",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := InternalKey(tt.orgID, tt.ledgerID, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLockInternalKey(t *testing.T) {
	orgID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	ledgerID := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

	tests := []struct {
		name     string
		orgID    uuid.UUID
		ledgerID uuid.UUID
		key      string
		expected string
	}{
		{
			name:     "Normal key",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "test-key",
			expected: "lock:550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:test-key",
		},
		{
			name:     "Empty key",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "",
			expected: "lock:550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := LockInternalKey(tt.orgID, tt.ledgerID, tt.key)
			assert.Equal(t, tt.expected, result)
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

func BenchmarkReverse(b *testing.B) {
	slice := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sliceCopy := make([]int, len(slice))
		copy(sliceCopy, slice)
		_ = Reverse(sliceCopy)
	}
}
