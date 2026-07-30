//go:build unit

package commons

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestSetConfigFromEnvVars_NilInterface(t *testing.T) {
	err := SetConfigFromEnvVars(nil)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNilConfig)
}

func TestSetConfigFromEnvVars_TypedNilPointer(t *testing.T) {
	type Config struct {
		Field string `env:"TEST_FIELD"`
	}

	var config *Config // typed nil

	err := SetConfigFromEnvVars(config)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNilConfig)
}

func TestSetConfigFromEnvVars_PointerToNonStruct(t *testing.T) {
	s := "not a struct"

	err := SetConfigFromEnvVars(&s)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNotStruct)
}

func TestSetConfigFromEnvVars_StringSlice(t *testing.T) {
	type Config struct {
		Principals []string `env:"TEST_STRING_SLICE_FIELD"`
	}

	t.Setenv("TEST_STRING_SLICE_FIELD", "alice, bob ,carol")

	config := &Config{}
	err := SetConfigFromEnvVars(config)

	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob", "carol"}, config.Principals)
}

func TestSetConfigFromEnvVars_StringSlice_Empty(t *testing.T) {
	type Config struct {
		Principals []string `env:"TEST_EMPTY_SLICE_FIELD"`
	}

	t.Setenv("TEST_EMPTY_SLICE_FIELD", "")
	os.Unsetenv("TEST_EMPTY_SLICE_FIELD")

	config := &Config{}
	err := SetConfigFromEnvVars(config)

	require.NoError(t, err)
	assert.Empty(t, config.Principals, "missing env var should yield an empty slice, not a panic")
}

func TestSetConfigFromEnvVars_UnsupportedSlice_ReturnsErrorNotPanic(t *testing.T) {
	type Config struct {
		Ports []int `env:"TEST_INT_SLICE_FIELD"`
	}

	t.Setenv("TEST_INT_SLICE_FIELD", "8080,9090")

	config := &Config{}

	var err error
	assert.NotPanics(t, func() { err = SetConfigFromEnvVars(config) })
	assert.ErrorIs(t, err, ErrUnsupportedFieldType)
}

func TestSetConfigFromEnvVars_UnsupportedScalar_ReturnsErrorNotPanic(t *testing.T) {
	type Config struct {
		Ratio float64 `env:"TEST_FLOAT_FIELD"`
	}

	t.Setenv("TEST_FLOAT_FIELD", "1.5")

	config := &Config{}

	var err error
	assert.NotPanics(t, func() { err = SetConfigFromEnvVars(config) })
	assert.ErrorIs(t, err, ErrUnsupportedFieldType)
}

func TestInitLocalEnvConfig_NonLocalReturnsNonNil(t *testing.T) {
	t.Setenv("VERSION", "1.0.0")
	t.Setenv("ENV_NAME", "production")

	// Reset the once guard so we can test fresh.
	localEnvConfig = nil
	localEnvConfigOnce = sync.Once{}

	result := InitLocalEnvConfig()

	require.NotNil(t, result, "InitLocalEnvConfig must return non-nil even for non-local env")
	assert.False(t, result.Initialized)
}

func TestInitLocalEnvConfigPrintsVersionAndEnvironment(t *testing.T) {
	t.Setenv("VERSION", "NO-VERSION")
	t.Setenv("ENV_NAME", "development")

	localEnvConfig = nil
	localEnvConfigOnce = sync.Once{}

	stdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}

	os.Stdout = writer

	var output bytes.Buffer
	copyDone := make(chan struct{})
	copyErrCh := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&output, reader)
		copyErrCh <- copyErr
		close(copyDone)
	}()

	defer func() {
		require.NoError(t, reader.Close())
		os.Stdout = stdout
	}()

	InitLocalEnvConfig()

	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}

	<-copyDone
	require.NoError(t, <-copyErrCh)

	result := output.String()

	want := "VERSION: NO-VERSION\n\nENVIRONMENT NAME: development\n\n"
	if !strings.Contains(result, want) {
		t.Fatalf("unexpected output. got: %q", result)
	}
}

// --- envDefault tag -------------------------------------------------------
//
// The loader previously had no default mechanism at all: an unset variable
// yielded the field's zero value, and any envDefault/default struct tag was
// read by nothing. For a bool named "...Enabled" that meant OFF, silently,
// while a reviewer looking at the tag believed a default was in force.

func TestSetConfigFromEnvVars_EnvDefault_AppliesWhenUnset(t *testing.T) {
	type Config struct {
		Enabled bool     `env:"TEST_ED_BOOL" envDefault:"true"`
		Port    int      `env:"TEST_ED_INT" envDefault:"8080"`
		Host    string   `env:"TEST_ED_STR" envDefault:"localhost"`
		Origins []string `env:"TEST_ED_SLICE" envDefault:"a, b ,c"`
	}

	for _, k := range []string{"TEST_ED_BOOL", "TEST_ED_INT", "TEST_ED_STR", "TEST_ED_SLICE"} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.True(t, config.Enabled, "an unset variable must take the declared default, not the zero value")
	assert.Equal(t, 8080, config.Port)
	assert.Equal(t, "localhost", config.Host)
	assert.Equal(t, []string{"a", "b", "c"}, config.Origins)
}

func TestSetConfigFromEnvVars_EnvDefault_ExplicitValueWins(t *testing.T) {
	type Config struct {
		Enabled bool     `env:"TEST_ED_W_BOOL" envDefault:"true"`
		Port    int      `env:"TEST_ED_W_INT" envDefault:"8080"`
		Host    string   `env:"TEST_ED_W_STR" envDefault:"localhost"`
		Origins []string `env:"TEST_ED_W_SLICE" envDefault:"a,b"`
	}

	t.Setenv("TEST_ED_W_BOOL", "false")
	t.Setenv("TEST_ED_W_INT", "9090")
	t.Setenv("TEST_ED_W_STR", "example.com")
	t.Setenv("TEST_ED_W_SLICE", "x,y,z")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.False(t, config.Enabled, "an operator setting false explicitly must win over the default")
	assert.Equal(t, 9090, config.Port)
	assert.Equal(t, "example.com", config.Host)
	assert.Equal(t, []string{"x", "y", "z"}, config.Origins)
}

func TestSetConfigFromEnvVars_EnvDefault_AppliesWhenValueIsBlank(t *testing.T) {
	type Config struct {
		Enabled bool     `env:"TEST_ED_B_BOOL" envDefault:"true"`
		Host    string   `env:"TEST_ED_B_STR" envDefault:"localhost"`
		Origins []string `env:"TEST_ED_B_SLICE" envDefault:"a,b"`
	}

	t.Setenv("TEST_ED_B_BOOL", "")
	t.Setenv("TEST_ED_B_STR", "   ")
	t.Setenv("TEST_ED_B_SLICE", "  ")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.True(t, config.Enabled, "a blank value means the operator supplied nothing")
	assert.Equal(t, "localhost", config.Host)
	assert.Equal(t, []string{"a", "b"}, config.Origins)
}

func TestSetConfigFromEnvVars_EnvDefault_AppliesWhenValueIsUnparseable(t *testing.T) {
	type Config struct {
		Enabled bool `env:"TEST_ED_U_BOOL" envDefault:"true"`
		Port    int  `env:"TEST_ED_U_INT" envDefault:"8080"`
	}

	t.Setenv("TEST_ED_U_BOOL", "maybe")
	t.Setenv("TEST_ED_U_INT", "not-a-number")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.True(t, config.Enabled, "a garbage value must fall back to the default, matching GetenvBoolOrDefault")
	assert.Equal(t, 8080, config.Port)
}

func TestSetConfigFromEnvVars_EnvDefault_UnparseableDefaultIsAnError(t *testing.T) {
	t.Run("bool", func(t *testing.T) {
		type Config struct {
			Enabled bool `env:"TEST_ED_E_BOOL" envDefault:"yes-please"`
		}

		err := SetConfigFromEnvVars(&Config{})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidDefaultValue)
		assert.Contains(t, err.Error(), "Enabled")
	})

	t.Run("int", func(t *testing.T) {
		type Config struct {
			Port int `env:"TEST_ED_E_INT" envDefault:"eight-thousand"`
		}

		err := SetConfigFromEnvVars(&Config{})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidDefaultValue)
		assert.Contains(t, err.Error(), "Port")
	})
}

func TestSetConfigFromEnvVars_EnvDefault_OutOfRangeDefaultIsAnError(t *testing.T) {
	type Config struct {
		Retries int8 `env:"TEST_ED_O_INT" envDefault:"999"`
	}

	err := SetConfigFromEnvVars(&Config{})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidDefaultValue)
	assert.Contains(t, err.Error(), "Retries")
}

func TestSetConfigFromEnvVars_NoEnvDefault_KeepsOriginalBehaviour(t *testing.T) {
	type Config struct {
		Enabled bool     `env:"TEST_ED_N_BOOL"`
		Port    int      `env:"TEST_ED_N_INT"`
		Host    string   `env:"TEST_ED_N_STR"`
		Origins []string `env:"TEST_ED_N_SLICE"`
		Spaces  string   `env:"TEST_ED_N_SPACES"`
	}

	for _, k := range []string{"TEST_ED_N_BOOL", "TEST_ED_N_INT", "TEST_ED_N_STR", "TEST_ED_N_SLICE"} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	// A whitespace-only value with no default is preserved verbatim, exactly as
	// os.Getenv returned it before this tag existed. Trimming it here would be a
	// silent behaviour change for every service already deployed.
	t.Setenv("TEST_ED_N_SPACES", "   ")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.False(t, config.Enabled)
	assert.Equal(t, 0, config.Port)
	assert.Empty(t, config.Host)
	assert.Equal(t, []string{}, config.Origins)
	assert.Equal(t, "   ", config.Spaces)
}
