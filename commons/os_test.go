package commons

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetenvOrDefault_WithValue(t *testing.T) {
	key := "TEST_GETENV_OR_DEFAULT"
	expected := "test-value"

	t.Setenv(key, expected)

	result := GetenvOrDefault(key, "default")

	assert.Equal(t, expected, result)
}

func TestGetenvOrDefault_WithDefault(t *testing.T) {
	key := "TEST_GETENV_OR_DEFAULT_MISSING"
	expected := "default-value"

	// Register cleanup, then unset
	t.Setenv(key, "")
	os.Unsetenv(key)

	result := GetenvOrDefault(key, expected)

	assert.Equal(t, expected, result)
}

func TestGetenvOrDefault_WithEmptyValue(t *testing.T) {
	key := "TEST_GETENV_OR_DEFAULT_EMPTY"
	expected := "default-value"

	t.Setenv(key, "")

	result := GetenvOrDefault(key, expected)

	assert.Equal(t, expected, result, "empty string should return default")
}

func TestGetenvOrDefault_WithWhitespace(t *testing.T) {
	key := "TEST_GETENV_OR_DEFAULT_WHITESPACE"
	expected := "default-value"

	t.Setenv(key, "   ")

	result := GetenvOrDefault(key, expected)

	assert.Equal(t, expected, result, "whitespace-only string should return default")
}

func TestGetenvBoolOrDefault_True(t *testing.T) {
	key := "TEST_GETENV_BOOL_TRUE"

	t.Setenv(key, "true")

	result := GetenvBoolOrDefault(key, false)

	assert.True(t, result)
}

func TestGetenvBoolOrDefault_False(t *testing.T) {
	key := "TEST_GETENV_BOOL_FALSE"

	t.Setenv(key, "false")

	result := GetenvBoolOrDefault(key, true)

	assert.False(t, result)
}

func TestGetenvBoolOrDefault_InvalidValue(t *testing.T) {
	key := "TEST_GETENV_BOOL_INVALID"

	t.Setenv(key, "not-a-bool")

	result := GetenvBoolOrDefault(key, true)

	assert.True(t, result, "invalid bool should return default")
}

func TestGetenvBoolOrDefault_MissingKey(t *testing.T) {
	key := "TEST_GETENV_BOOL_MISSING"

	t.Setenv(key, "")
	os.Unsetenv(key)

	result := GetenvBoolOrDefault(key, true)

	assert.True(t, result, "missing key should return default")
}

func TestGetenvIntOrDefault_ValidInt(t *testing.T) {
	key := "TEST_GETENV_INT_VALID"

	t.Setenv(key, "42")

	result := GetenvIntOrDefault(key, 0)

	assert.Equal(t, int64(42), result)
}

func TestGetenvIntOrDefault_NegativeInt(t *testing.T) {
	key := "TEST_GETENV_INT_NEGATIVE"

	t.Setenv(key, "-100")

	result := GetenvIntOrDefault(key, 0)

	assert.Equal(t, int64(-100), result)
}

func TestGetenvIntOrDefault_InvalidValue(t *testing.T) {
	key := "TEST_GETENV_INT_INVALID"

	t.Setenv(key, "not-a-number")

	result := GetenvIntOrDefault(key, 99)

	assert.Equal(t, int64(99), result, "invalid int should return default")
}

func TestGetenvIntOrDefault_MissingKey(t *testing.T) {
	key := "TEST_GETENV_INT_MISSING"

	t.Setenv(key, "")
	os.Unsetenv(key)

	result := GetenvIntOrDefault(key, 99)

	assert.Equal(t, int64(99), result, "missing key should return default")
}

func TestSetConfigFromEnvVars_Success(t *testing.T) {
	type Config struct {
		StringField string `env:"TEST_STRING_FIELD"`
		BoolField   bool   `env:"TEST_BOOL_FIELD"`
		IntField    int64  `env:"TEST_INT_FIELD"`
	}

	t.Setenv("TEST_STRING_FIELD", "test-value")
	t.Setenv("TEST_BOOL_FIELD", "true")
	t.Setenv("TEST_INT_FIELD", "123")

	config := &Config{}
	err := SetConfigFromEnvVars(config)

	assert.NoError(t, err)
	assert.Equal(t, "test-value", config.StringField)
	assert.True(t, config.BoolField)
	assert.Equal(t, int64(123), config.IntField)
}

func TestSetConfigFromEnvVars_NonPointer(t *testing.T) {
	type Config struct {
		Field string `env:"TEST_FIELD"`
	}

	config := Config{}
	err := SetConfigFromEnvVars(config)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNotPointer)
}

func TestSetConfigFromEnvVars_MissingEnvVars(t *testing.T) {
	type Config struct {
		Field string `env:"TEST_MISSING_FIELD_XYZ"`
	}

	t.Setenv("TEST_MISSING_FIELD_XYZ", "")
	os.Unsetenv("TEST_MISSING_FIELD_XYZ")

	config := &Config{}
	err := SetConfigFromEnvVars(config)

	assert.NoError(t, err)
	assert.Empty(t, config.Field, "missing env var should result in zero value")
}

func TestEnsureConfigFromEnvVars_Success(t *testing.T) {
	type Config struct {
		Field string `env:"TEST_ENSURE_FIELD"`
	}

	t.Setenv("TEST_ENSURE_FIELD", "value")

	config := &Config{}
	result := EnsureConfigFromEnvVars(config)

	assert.NotNil(t, result)
	assert.Equal(t, "value", config.Field)
}

func TestEnsureConfigFromEnvVars_PanicOnNonPointer(t *testing.T) {
	type Config struct {
		Field string `env:"TEST_FIELD"`
	}

	config := Config{}

	assert.Panics(t, func() {
		EnsureConfigFromEnvVars(config)
	}, "EnsureConfigFromEnvVars should panic on non-pointer")
}
