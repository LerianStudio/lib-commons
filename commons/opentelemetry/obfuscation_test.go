package opentelemetry

import (
	"context"
	"strings"
	"testing"

	cn "github.com/LerianStudio/lib-commons/v2/commons/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestStruct represents a test struct with sensitive and non-sensitive fields
type TestStruct struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Email       string `json:"email"`
	Token       string `json:"token"`
	PublicData  string `json:"publicData"`
	Credentials struct {
		APIKey    string `json:"apikey"`
		SecretKey string `json:"secret"`
	} `json:"credentials"`
	Metadata map[string]any `json:"metadata"`
}

// NestedTestStruct represents a more complex nested structure
type NestedTestStruct struct {
	User     TestStruct `json:"user"`
	Settings struct {
		Theme       string   `json:"theme"`
		PrivateKey  string   `json:"private_key"`
		Preferences []string `json:"preferences"`
	} `json:"settings"`
	Tokens []string `json:"tokens"`
}

func TestNewDefaultObfuscator(t *testing.T) {
	obfuscator := NewDefaultObfuscator()

	assert.NotNil(t, obfuscator)
	assert.Equal(t, cn.ObfuscatedValue, obfuscator.GetObfuscatedValue())

	// Test common sensitive fields (should match security.DefaultSensitiveFields)
	sensitiveFields := []string{
		"password", "token", "secret", "key", "authorization",
		"auth", "credential", "credentials", "apikey", "api_key",
		"access_token", "refresh_token", "private_key", "privatekey",
	}

	for _, field := range sensitiveFields {
		assert.True(t, obfuscator.ShouldObfuscate(field), "Field %s should be obfuscated", field)
		assert.True(t, obfuscator.ShouldObfuscate(strings.ToUpper(field)), "Field %s (uppercase) should be obfuscated", field)
	}

	// Test non-sensitive fields
	nonSensitiveFields := []string{
		"username", "email", "name", "id", "status", "created_at", "updated_at",
	}

	for _, field := range nonSensitiveFields {
		assert.False(t, obfuscator.ShouldObfuscate(field), "Field %s should not be obfuscated", field)
	}
}

func TestNewCustomObfuscator(t *testing.T) {
	customFields := []string{"customSecret", "internalToken", "SENSITIVE_DATA"}
	obfuscator := NewCustomObfuscator(customFields)

	assert.NotNil(t, obfuscator)
	assert.Equal(t, cn.ObfuscatedValue, obfuscator.GetObfuscatedValue())

	// Test custom sensitive fields (case insensitive)
	assert.True(t, obfuscator.ShouldObfuscate("customSecret"))
	assert.True(t, obfuscator.ShouldObfuscate("CUSTOMSECRET"))
	assert.True(t, obfuscator.ShouldObfuscate("internalToken"))
	assert.True(t, obfuscator.ShouldObfuscate("sensitive_data"))

	// Test that default fields are not included
	assert.False(t, obfuscator.ShouldObfuscate("password"))
	assert.False(t, obfuscator.ShouldObfuscate("token"))
}

func TestObfuscateStructFields(t *testing.T) {
	obfuscator := NewDefaultObfuscator()

	tests := []struct {
		name     string
		input    any
		expected any
	}{
		{
			name: "simple map with sensitive fields",
			input: map[string]any{
				"username": "john_doe",
				"password": "secret123",
				"email":    "john@example.com",
				"token":    "abc123xyz",
			},
			expected: map[string]any{
				"username": "john_doe",
				"password": cn.ObfuscatedValue,
				"email":    "john@example.com",
				"token":    cn.ObfuscatedValue,
			},
		},
		{
			name: "nested map with sensitive fields",
			input: map[string]any{
				"user": map[string]any{
					"name":     "John",
					"password": "secret",
				},
				"config": map[string]any{
					"theme":   "dark",
					"api_key": "key123",
				},
			},
			expected: map[string]any{
				"user": map[string]any{
					"name":     "John",
					"password": cn.ObfuscatedValue,
				},
				"config": map[string]any{
					"theme":   "dark",
					"api_key": cn.ObfuscatedValue,
				},
			},
		},
		{
			name: "array with sensitive data",
			input: []any{
				map[string]any{
					"id":       1,
					"password": "secret1",
				},
				map[string]any{
					"id":       2,
					"password": "secret2",
				},
			},
			expected: []any{
				map[string]any{
					"id":       1,
					"password": cn.ObfuscatedValue,
				},
				map[string]any{
					"id":       2,
					"password": cn.ObfuscatedValue,
				},
			},
		},
		{
			name:     "primitive value unchanged",
			input:    "simple string",
			expected: "simple string",
		},
		{
			name:     "number unchanged",
			input:    42,
			expected: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := obfuscateStructFields(tt.input, obfuscator)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestObfuscateStruct(t *testing.T) {
	testStruct := TestStruct{
		Username:   "john_doe",
		Password:   "secret123",
		Email:      "john@example.com",
		Token:      "abc123xyz",
		PublicData: "public info",
		Credentials: struct {
			APIKey    string `json:"apikey"`
			SecretKey string `json:"secret"`
		}{
			APIKey:    "key123",
			SecretKey: "secret456",
		},
		Metadata: map[string]any{
			"theme":       "dark",
			"private_key": "private123",
		},
	}

	tests := []struct {
		name       string
		obfuscator FieldObfuscator
		wantError  bool
	}{
		{
			name:       "with default obfuscator",
			obfuscator: NewDefaultObfuscator(),
			wantError:  false,
		},
		{
			name:       "with custom obfuscator",
			obfuscator: NewCustomObfuscator([]string{"username", "email"}),
			wantError:  false,
		},
		{
			name:       "without obfuscator (nil)",
			obfuscator: nil,
			wantError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ObfuscateStruct(testStruct, tt.obfuscator)

			if tt.wantError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestObfuscateStruct_InvalidJSON(t *testing.T) {
	// Create a struct that cannot be marshaled to JSON (contains a channel)
	invalidStruct := struct {
		Name    string
		Channel chan int
	}{
		Name:    "test",
		Channel: make(chan int),
	}

	obfuscator := NewDefaultObfuscator()
	result, err := ObfuscateStruct(invalidStruct, obfuscator)

	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestSetSpanAttributesFromStructWithObfuscation_Default(t *testing.T) {
	// Create a no-op tracer for testing
	tracer := noop.NewTracerProvider().Tracer("test")
	_, span := tracer.Start(context.TODO(), "test-span")

	testStruct := TestStruct{
		Username:   "john_doe",
		Password:   "secret123",
		Email:      "john@example.com",
		Token:      "abc123xyz",
		PublicData: "public info",
	}

	err := SetSpanAttributesFromStructWithObfuscation(&span, "test_data", testStruct)
	require.NoError(t, err)

	// The span should contain the obfuscated data (noop span doesn't store attributes)
}

func TestSetSpanAttributesFromStructWithObfuscation(t *testing.T) {
	// Create a no-op tracer for testing
	tracer := noop.NewTracerProvider().Tracer("test")
	_, span := tracer.Start(context.TODO(), "test-span")

	testStruct := TestStruct{
		Username:   "john_doe",
		Password:   "secret123",
		Email:      "john@example.com",
		Token:      "abc123xyz",
		PublicData: "public info",
		Credentials: struct {
			APIKey    string `json:"apikey"`
			SecretKey string `json:"secret"`
		}{
			APIKey:    "key123",
			SecretKey: "secret456",
		},
		Metadata: map[string]any{
			"theme":       "dark",
			"private_key": "private123",
		},
	}

	tests := []struct {
		name       string
		obfuscator FieldObfuscator
		wantError  bool
	}{
		{
			name:       "with default obfuscator",
			obfuscator: NewDefaultObfuscator(),
			wantError:  false,
		},
		{
			name:       "with custom obfuscator",
			obfuscator: NewCustomObfuscator([]string{"username", "email"}),
			wantError:  false,
		},
		{
			name:       "without obfuscator (nil)",
			obfuscator: nil,
			wantError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
		if tt.obfuscator == nil || tt.name == "with default obfuscator" {
			err = SetSpanAttributesFromStructWithObfuscation(&span, "test_data", testStruct)
		} else {
			err = SetSpanAttributesFromStructWithCustomObfuscation(&span, "test_data", testStruct, tt.obfuscator)
		}

			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSetSpanAttributesFromStructWithObfuscation_NestedStruct(t *testing.T) {
	// Create a no-op tracer for testing
	tracer := noop.NewTracerProvider().Tracer("test")
	_, span := tracer.Start(context.TODO(), "test-span")

	nestedStruct := NestedTestStruct{
		User: TestStruct{
			Username: "john_doe",
			Password: "secret123",
			Token:    "token456",
		},
		Settings: struct {
			Theme       string   `json:"theme"`
			PrivateKey  string   `json:"private_key"`
			Preferences []string `json:"preferences"`
		}{
			Theme:       "dark",
			PrivateKey:  "private789",
			Preferences: []string{"notifications", "dark_mode"},
		},
		Tokens: []string{"token1", "token2"},
	}

	err := SetSpanAttributesFromStructWithObfuscation(&span, "nested_data", nestedStruct)

	assert.NoError(t, err)
}

func TestSetSpanAttributesFromStructWithObfuscation_InvalidJSON(t *testing.T) {
	// Create a no-op tracer for testing
	tracer := noop.NewTracerProvider().Tracer("test")
	_, span := tracer.Start(context.TODO(), "test-span")

	// Create a struct that cannot be marshaled to JSON (contains a channel)
	invalidStruct := struct {
		Name    string
		Channel chan int
	}{
		Name:    "test",
		Channel: make(chan int),
	}

	err := SetSpanAttributesFromStructWithObfuscation(&span, "invalid_data", invalidStruct)

	assert.Error(t, err)
}

// MockObfuscator is a custom obfuscator for testing
type MockObfuscator struct {
	shouldObfuscateFunc func(string) bool
	obfuscatedValue     string
}

func (m *MockObfuscator) ShouldObfuscate(fieldName string) bool {
	if m.shouldObfuscateFunc != nil {
		return m.shouldObfuscateFunc(fieldName)
	}
	return false
}

func (m *MockObfuscator) GetObfuscatedValue() string {
	return m.obfuscatedValue
}

func TestCustomObfuscatorInterface(t *testing.T) {
	// Create a no-op tracer for testing
	tracer := noop.NewTracerProvider().Tracer("test")
	_, span := tracer.Start(context.TODO(), "test-span")

	testStruct := map[string]any{
		"public":  "visible",
		"private": "hidden",
		"secret":  "classified",
	}

	mockObfuscator := &MockObfuscator{
		shouldObfuscateFunc: func(fieldName string) bool {
			return fieldName == "private" || fieldName == "secret"
		},
		obfuscatedValue: "[REDACTED]",
	}

	err := SetSpanAttributesFromStructWithCustomObfuscation(&span, "test_data", testStruct, mockObfuscator)
	assert.NoError(t, err)
}

func TestObfuscatedValueConstant(t *testing.T) {
	assert.Equal(t, "***", cn.ObfuscatedValue)
}
