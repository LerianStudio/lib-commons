//go:build unit

package commons

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestSetConfigFromEnvVars_NamedStringSlice_ConvertsWithoutPanic(t *testing.T) {
	type Origins []string

	type Config struct {
		Origins Origins `env:"TEST_NAMED_SLICE_FIELD"`
	}

	t.Setenv("TEST_NAMED_SLICE_FIELD", "https://a.example, https://b.example")

	config := &Config{}

	var err error
	assert.NotPanics(t, func() { err = SetConfigFromEnvVars(config) })
	require.NoError(t, err)
	assert.Equal(t, Origins{"https://a.example", "https://b.example"}, config.Origins)
}

func TestSetConfigFromEnvVars_IntEnvValueOverflowsField_FallsBackToDefault(t *testing.T) {
	type Config struct {
		Retries int8 `env:"TEST_INT8_OVERFLOW" envDefault:"5"`
	}

	t.Setenv("TEST_INT8_OVERFLOW", "999")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.Equal(t, int8(5), config.Retries,
		"a parseable value that does not fit the field must fall back to the default, not truncate to -25")
}

func TestSetConfigFromEnvVars_IntEnvValueOverflowsField_NoDefaultKeepsZero(t *testing.T) {
	type Config struct {
		Retries int8 `env:"TEST_INT8_OVERFLOW_NODEF"`
	}

	t.Setenv("TEST_INT8_OVERFLOW_NODEF", "999")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.Equal(t, int8(0), config.Retries)
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

// --- time.Duration fields -------------------------------------------------
//
// time.Duration is an int64, so a Kind-based switch put it in the integer case:
// envDefault:"30" on a timeout meant 30 NANOSECONDS, and the unit-bearing
// envDefault:"30s" that every service in the fleet writes failed the load
// outright, before the environment was read at all.

func TestSetConfigFromEnvVars_Duration_DefaultCarriesItsUnit(t *testing.T) {
	type Config struct {
		Timeout   time.Duration `env:"TEST_ED_D_TIMEOUT" envDefault:"30s"`
		Retention time.Duration `env:"TEST_ED_D_RETENTION" envDefault:"720h"`
		Poll      time.Duration `env:"TEST_ED_D_POLL" envDefault:"150ms"`
		Port      int           `env:"TEST_ED_D_PORT" envDefault:"30"`
	}

	for _, k := range []string{"TEST_ED_D_TIMEOUT", "TEST_ED_D_RETENTION", "TEST_ED_D_POLL", "TEST_ED_D_PORT"} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	// The two halves of the defect, asserted side by side: a duration keeps its unit,
	// and a plain integer in the same struct is still a plain integer.
	assert.Equal(t, 30*time.Second, config.Timeout, "envDefault:\"30s\" on a duration must be 30 seconds, not 30 nanoseconds")
	assert.Equal(t, 720*time.Hour, config.Retention)
	assert.Equal(t, 150*time.Millisecond, config.Poll)
	assert.Equal(t, 30, config.Port, "a real int field must still read 30 as the integer 30")
}

func TestSetConfigFromEnvVars_Duration_EnvValueWinsAndCarriesItsUnit(t *testing.T) {
	type Config struct {
		Timeout time.Duration `env:"TEST_ED_DE_TIMEOUT" envDefault:"30s"`
	}

	t.Setenv("TEST_ED_DE_TIMEOUT", "2m")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.Equal(t, 2*time.Minute, config.Timeout)
}

// A unit-less integer stays NANOSECONDS. lerian-internal-gitops ships
// STREAMING_HEALTH_CHECK_TIMEOUT "2000000000" for br-slc, written in raw nanoseconds
// because that is what this loader has always read. Under an "integer means seconds"
// convention that two-second readiness timeout becomes roughly 63 years, with nothing
// in the logs to say so.
func TestSetConfigFromEnvVars_Duration_UnitlessIntegerStaysNanoseconds(t *testing.T) {
	type Config struct {
		FromEnv     time.Duration `env:"TEST_ED_DN_ENV" envDefault:"2s"`
		FromDefault time.Duration `env:"TEST_ED_DN_DEF" envDefault:"2000000000"`
	}

	t.Setenv("TEST_ED_DN_ENV", "2000000000")
	require.NoError(t, os.Unsetenv("TEST_ED_DN_DEF"))

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.Equal(t, 2*time.Second, config.FromEnv, "2000000000 nanoseconds is two seconds, not two billion")
	assert.Equal(t, 2*time.Second, config.FromDefault)
}

func TestSetConfigFromEnvVars_Duration_SurroundingWhitespaceIsTolerated(t *testing.T) {
	type Config struct {
		Timeout time.Duration `env:"TEST_ED_DW_TIMEOUT" envDefault:"30s"`
	}

	// A Helm value or ConfigMap entry routinely arrives with a trailing newline.
	t.Setenv("TEST_ED_DW_TIMEOUT", " 45s\n")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.Equal(t, 45*time.Second, config.Timeout)
}

func TestSetConfigFromEnvVars_Duration_UnparseableEnvValueTakesTheDefault(t *testing.T) {
	type Config struct {
		Timeout time.Duration `env:"TEST_ED_DU_TIMEOUT" envDefault:"30s"`
	}

	t.Setenv("TEST_ED_DU_TIMEOUT", "half a minute")

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.Equal(t, 30*time.Second, config.Timeout, "a garbage value falls back to the default, as it does for bool and int")
}

func TestSetConfigFromEnvVars_Duration_UnparseableDefaultIsAnError(t *testing.T) {
	type Config struct {
		Timeout time.Duration `env:"TEST_ED_DX_TIMEOUT" envDefault:"half a minute"`
	}

	err := SetConfigFromEnvVars(&Config{})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidDefaultValue)
	assert.Contains(t, err.Error(), "Timeout")
}

func TestSetConfigFromEnvVars_Duration_NoDefaultKeepsZero(t *testing.T) {
	type Config struct {
		Timeout time.Duration `env:"TEST_ED_DZ_TIMEOUT"`
	}

	require.NoError(t, os.Unsetenv("TEST_ED_DZ_TIMEOUT"))

	config := &Config{}
	require.NoError(t, SetConfigFromEnvVars(config))

	assert.Equal(t, time.Duration(0), config.Timeout)
}

// Every distinct duration spelling declared on a time.Duration field across the
// consuming services (audit-trail, br-slc, deepwell, go-boilerplate-ddd, the pix
// plugins, tenant-manager, vault-lerian and the severino scaffolds) must load. The list
// is harvested from those repositories rather than invented here: all of them carry a
// unit, and before this fix every one of them failed the load outright.
func TestSetConfigFromEnvVars_Duration_FleetDeclarationsAllLoad(t *testing.T) {
	fleet := map[string]time.Duration{
		"150ms": 150 * time.Millisecond,
		"0s":    0,
		"1s":    time.Second,
		"2s":    2 * time.Second,
		"3s":    3 * time.Second,
		"5s":    5 * time.Second,
		"10s":   10 * time.Second,
		"15s":   15 * time.Second,
		"20s":   20 * time.Second,
		"30s":   30 * time.Second,
		"60s":   60 * time.Second,
		"90s":   90 * time.Second,
		"5m":    5 * time.Minute,
		"30m":   30 * time.Minute,
		"1h":    time.Hour,
		"168h":  168 * time.Hour,
		"720h":  720 * time.Hour,
	}

	for spelling, want := range fleet {
		t.Run(spelling, func(t *testing.T) {
			require.NoError(t, os.Unsetenv("TEST_ED_DF_VALUE"))

			got, err := parseEnvDuration(spelling)
			require.NoError(t, err, "a duration spelling in production configuration must load")
			assert.Equal(t, want, got)
		})
	}
}

func TestGetenvDurationOrDefault(t *testing.T) {
	tests := []struct {
		name  string
		value string
		set   bool
		want  time.Duration
	}{
		{name: "unset takes the default", want: 30 * time.Second},
		{name: "blank takes the default", value: "   ", set: true, want: 30 * time.Second},
		{name: "unit is honoured", value: "90s", set: true, want: 90 * time.Second},
		{name: "sub-second unit is honoured", value: "150ms", set: true, want: 150 * time.Millisecond},
		{name: "unit-less integer is nanoseconds", value: "2000000000", set: true, want: 2 * time.Second},
		{name: "garbage takes the default", value: "soon", set: true, want: 30 * time.Second},
		{name: "negative duration is honoured", value: "-5m", set: true, want: -5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const key = "TEST_GETENV_DURATION"

			if tt.set {
				t.Setenv(key, tt.value)
			} else {
				require.NoError(t, os.Unsetenv(key))
			}

			assert.Equal(t, tt.want, GetenvDurationOrDefault(key, 30*time.Second))
		})
	}
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
