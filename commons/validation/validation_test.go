package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequired(t *testing.T) {
	tests := []struct {
		name        string
		value       interface{}
		fieldName   string
		expectError bool
	}{
		{
			name:        "non-empty string",
			value:       "test",
			fieldName:   "name",
			expectError: false,
		},
		{
			name:        "empty string",
			value:       "",
			fieldName:   "name",
			expectError: true,
		},
		{
			name:        "whitespace only string",
			value:       "   ",
			fieldName:   "name",
			expectError: true,
		},
		{
			name:        "zero int",
			value:       0,
			fieldName:   "age",
			expectError: true,
		},
		{
			name:        "positive int",
			value:       25,
			fieldName:   "age",
			expectError: false,
		},
		{
			name:        "nil pointer",
			value:       (*string)(nil),
			fieldName:   "pointer",
			expectError: true,
		},
		{
			name:        "empty slice",
			value:       []string{},
			fieldName:   "items",
			expectError: true,
		},
		{
			name:        "non-empty slice",
			value:       []string{"item"},
			fieldName:   "items",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Required(tt.value, tt.fieldName)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.fieldName)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMinLength(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		minLength   int
		fieldName   string
		expectError bool
	}{
		{
			name:        "string meets min length",
			value:       "hello",
			minLength:   3,
			fieldName:   "greeting",
			expectError: false,
		},
		{
			name:        "string exactly min length",
			value:       "hi",
			minLength:   2,
			fieldName:   "greeting",
			expectError: false,
		},
		{
			name:        "string below min length",
			value:       "h",
			minLength:   2,
			fieldName:   "greeting",
			expectError: true,
		},
		{
			name:        "empty string with min length 1",
			value:       "",
			minLength:   1,
			fieldName:   "text",
			expectError: true,
		},
		{
			name:        "unicode string",
			value:       "你好",
			minLength:   2,
			fieldName:   "chinese",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MinLength(tt.value, tt.minLength, tt.fieldName)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.fieldName)
				assert.Contains(t, err.Error(), "minimum length")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMaxLength(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		maxLength   int
		fieldName   string
		expectError bool
	}{
		{
			name:        "string within max length",
			value:       "hello",
			maxLength:   10,
			fieldName:   "greeting",
			expectError: false,
		},
		{
			name:        "string exactly max length",
			value:       "hello",
			maxLength:   5,
			fieldName:   "greeting",
			expectError: false,
		},
		{
			name:        "string exceeds max length",
			value:       "hello world",
			maxLength:   5,
			fieldName:   "greeting",
			expectError: true,
		},
		{
			name:        "empty string",
			value:       "",
			maxLength:   5,
			fieldName:   "text",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MaxLength(tt.value, tt.maxLength, tt.fieldName)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.fieldName)
				assert.Contains(t, err.Error(), "maximum length")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestEmail(t *testing.T) {
	tests := []struct {
		name        string
		email       string
		expectError bool
	}{
		{
			name:        "valid email",
			email:       "user@example.com",
			expectError: false,
		},
		{
			name:        "valid email with subdomain",
			email:       "user@mail.example.com",
			expectError: false,
		},
		{
			name:        "valid email with plus",
			email:       "user+tag@example.com",
			expectError: false,
		},
		{
			name:        "invalid email - no @",
			email:       "userexample.com",
			expectError: true,
		},
		{
			name:        "invalid email - no domain",
			email:       "user@",
			expectError: true,
		},
		{
			name:        "invalid email - no user",
			email:       "@example.com",
			expectError: true,
		},
		{
			name:        "invalid email - spaces",
			email:       "user @example.com",
			expectError: true,
		},
		{
			name:        "empty email",
			email:       "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Email(tt.email, "email")
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "email")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		expectError bool
	}{
		{
			name:        "valid http URL",
			url:         "http://example.com",
			expectError: false,
		},
		{
			name:        "valid https URL",
			url:         "https://example.com",
			expectError: false,
		},
		{
			name:        "valid URL with path",
			url:         "https://example.com/path/to/resource",
			expectError: false,
		},
		{
			name:        "valid URL with query",
			url:         "https://example.com?key=value",
			expectError: false,
		},
		{
			name:        "invalid URL - no scheme",
			url:         "example.com",
			expectError: true,
		},
		{
			name:        "invalid URL - invalid scheme",
			url:         "ftp://example.com",
			expectError: true,
		},
		{
			name:        "invalid URL - spaces",
			url:         "http://example .com",
			expectError: true,
		},
		{
			name:        "empty URL",
			url:         "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := URL(tt.url, "URL")
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "URL")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUUID(t *testing.T) {
	tests := []struct {
		name        string
		uuid        string
		expectError bool
	}{
		{
			name:        "valid UUID v4",
			uuid:        "550e8400-e29b-41d4-a716-446655440000",
			expectError: false,
		},
		{
			name:        "valid UUID v7",
			uuid:        "01902d91-7082-72f9-a011-de1aae71b41a",
			expectError: false,
		},
		{
			name:        "invalid UUID - wrong format",
			uuid:        "550e8400-e29b-41d4-a716",
			expectError: true,
		},
		{
			name:        "invalid UUID - not hex",
			uuid:        "550e8400-e29b-41d4-a716-44665544000g",
			expectError: true,
		},
		{
			name:        "invalid UUID - too short",
			uuid:        "550e8400-e29b",
			expectError: true,
		},
		{
			name:        "empty string",
			uuid:        "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := UUID(tt.uuid, "UUID")
			if tt.expectError {
				assert.Error(t, err)
				if err != nil {
					assert.Contains(t, err.Error(), "UUID")
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestInRange(t *testing.T) {
	tests := []struct {
		name        string
		value       int64
		min         int64
		max         int64
		fieldName   string
		expectError bool
	}{
		{
			name:        "value within range",
			value:       50,
			min:         1,
			max:         100,
			fieldName:   "age",
			expectError: false,
		},
		{
			name:        "value at min boundary",
			value:       1,
			min:         1,
			max:         100,
			fieldName:   "age",
			expectError: false,
		},
		{
			name:        "value at max boundary",
			value:       100,
			min:         1,
			max:         100,
			fieldName:   "age",
			expectError: false,
		},
		{
			name:        "value below min",
			value:       0,
			min:         1,
			max:         100,
			fieldName:   "age",
			expectError: true,
		},
		{
			name:        "value above max",
			value:       101,
			min:         1,
			max:         100,
			fieldName:   "age",
			expectError: true,
		},
		{
			name:        "negative values",
			value:       -5,
			min:         -10,
			max:         -1,
			fieldName:   "temperature",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := InRange(tt.value, tt.min, tt.max, tt.fieldName)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.fieldName)
				assert.Contains(t, err.Error(), "range")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		pattern     string
		fieldName   string
		expectError bool
	}{
		{
			name:        "valid phone pattern",
			value:       "+1234567890",
			pattern:     `^\+\d{10,15}$`,
			fieldName:   "phone",
			expectError: false,
		},
		{
			name:        "invalid phone pattern",
			value:       "1234567890",
			pattern:     `^\+\d{10,15}$`,
			fieldName:   "phone",
			expectError: true,
		},
		{
			name:        "valid alphanumeric",
			value:       "abc123",
			pattern:     `^[a-zA-Z0-9]+$`,
			fieldName:   "code",
			expectError: false,
		},
		{
			name:        "invalid alphanumeric",
			value:       "abc-123",
			pattern:     `^[a-zA-Z0-9]+$`,
			fieldName:   "code",
			expectError: true,
		},
		{
			name:        "empty string with pattern",
			value:       "",
			pattern:     `^[a-zA-Z]+$`,
			fieldName:   "name",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Matches(tt.value, tt.pattern, tt.fieldName)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.fieldName)
				assert.Contains(t, err.Error(), "pattern")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOneOf(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		allowed     []string
		fieldName   string
		expectError bool
	}{
		{
			name:        "value in allowed list",
			value:       "active",
			allowed:     []string{"active", "inactive", "pending"},
			fieldName:   "status",
			expectError: false,
		},
		{
			name:        "value not in allowed list",
			value:       "deleted",
			allowed:     []string{"active", "inactive", "pending"},
			fieldName:   "status",
			expectError: true,
		},
		{
			name:        "empty value",
			value:       "",
			allowed:     []string{"active", "inactive"},
			fieldName:   "status",
			expectError: true,
		},
		{
			name:        "empty allowed list",
			value:       "active",
			allowed:     []string{},
			fieldName:   "status",
			expectError: true,
		},
		{
			name:        "case sensitive check",
			value:       "Active",
			allowed:     []string{"active", "inactive"},
			fieldName:   "status",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := OneOf(tt.value, tt.allowed, tt.fieldName)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.fieldName)
				assert.Contains(t, err.Error(), "one of")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateStruct(t *testing.T) {
	type TestStruct struct {
		Name     string `validate:"required,min=3,max=50"`
		Email    string `validate:"required,email"`
		Age      int    `validate:"required,min=18,max=100"`
		Status   string `validate:"required,oneof=active inactive"`
		Website  string `validate:"url"`
		ID       string `validate:"uuid"`
	}

	tests := []struct {
		name        string
		input       TestStruct
		expectError bool
		errorFields []string
	}{
		{
			name: "valid struct",
			input: TestStruct{
				Name:    "John Doe",
				Email:   "john@example.com",
				Age:     25,
				Status:  "active",
				Website: "https://example.com",
				ID:      "550e8400-e29b-41d4-a716-446655440000",
			},
			expectError: false,
		},
		{
			name: "multiple validation errors",
			input: TestStruct{
				Name:    "Jo", // too short
				Email:   "invalid-email",
				Age:     17, // too young
				Status:  "deleted", // not allowed
				Website: "not-a-url",
				ID:      "not-a-uuid",
			},
			expectError: true,
			errorFields: []string{"Name", "Email", "Age", "Status", "Website", "ID"},
		},
		{
			name: "missing required fields",
			input: TestStruct{
				Website: "https://example.com",
				ID:      "550e8400-e29b-41d4-a716-446655440000",
			},
			expectError: true,
			errorFields: []string{"Name", "Email", "Age", "Status"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateStruct(tt.input)
			
			if tt.expectError {
				assert.NotNil(t, errs)
				assert.True(t, len(errs) > 0)
				
				// Check that all expected fields have errors
				for _, field := range tt.errorFields {
					found := false
					for _, err := range errs {
						if err.Field == field {
							found = true
							break
						}
					}
					assert.True(t, found, "Expected error for field %s", field)
				}
			} else {
				assert.Nil(t, errs)
			}
		})
	}
}

func TestCustomValidator(t *testing.T) {
	// Test custom validator registration
	err := RegisterCustomValidator("customtest", func(value interface{}) error {
		str, ok := value.(string)
		if !ok || str != "valid" {
			return NewValidationError("value must be 'valid'", "custom")
		}
		return nil
	})
	assert.NoError(t, err)

	// Test using custom validator
	type TestStruct struct {
		Custom string `validate:"customtest"`
	}

	tests := []struct {
		name        string
		input       TestStruct
		expectError bool
	}{
		{
			name:        "valid custom value",
			input:       TestStruct{Custom: "valid"},
			expectError: false,
		},
		{
			name:        "invalid custom value",
			input:       TestStruct{Custom: "invalid"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateStruct(tt.input)
			if tt.expectError {
				assert.NotNil(t, errs)
				assert.True(t, len(errs) > 0)
			} else {
				assert.Nil(t, errs)
			}
		})
	}
}

// Benchmarks
func BenchmarkRequired(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Required("test", "field")
		_ = Required("", "field")
		_ = Required(123, "field")
	}
}

func BenchmarkEmail(b *testing.B) {
	emails := []string{
		"user@example.com",
		"invalid-email",
		"user+tag@mail.example.com",
		"@example.com",
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, email := range emails {
			_ = Email(email, "email")
		}
	}
}

func BenchmarkValidateStruct(b *testing.B) {
	type TestStruct struct {
		Name  string `validate:"required,min=3,max=50"`
		Email string `validate:"required,email"`
		Age   int    `validate:"required,min=18,max=100"`
	}
	
	s := TestStruct{
		Name:  "John Doe",
		Email: "john@example.com",
		Age:   25,
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ValidateStruct(s)
	}
}