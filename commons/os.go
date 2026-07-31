package commons

import (
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// ErrNotPointer indicates that a non-pointer value was passed where a pointer was required.
var ErrNotPointer = errors.New("argument must be a pointer")

// GetenvOrDefault encapsulate built-in os.Getenv behavior but if key is not present it returns the defaultValue.
func GetenvOrDefault(key string, defaultValue string) string {
	str := os.Getenv(key)
	if strings.TrimSpace(str) == "" {
		return defaultValue
	}

	return str
}

// GetenvBoolOrDefault returns the value of os.Getenv(key string) value as bool or defaultValue if error.
// If the environment variable (key) is not defined, it returns the given defaultValue.
// If the environment variable (key) is not a valid bool format, it returns the given defaultValue.
// If any error occurring during bool parse, it returns the given defaultValue.
// A warning is printed to stderr when a non-empty value fails to parse, providing
// visibility into misconfigured environment variables.
func GetenvBoolOrDefault(key string, defaultValue bool) bool {
	str := os.Getenv(key)

	val, err := strconv.ParseBool(str)
	if err != nil {
		if str != "" {
			fmt.Fprintf(os.Stderr, "WARN: env var %s=%q is not a valid bool, using default %v\n", key, str, defaultValue)
		}

		return defaultValue
	}

	return val
}

// GetenvIntOrDefault returns the value of os.Getenv(key string) value as int or defaultValue if error.
// If the environment variable (key) is not defined, it returns the given defaultValue.
// If the environment variable (key) is not a valid int format, it returns the given defaultValue.
// If any error occurring during int parse, it returns the given defaultValue.
// A warning is printed to stderr when a non-empty value fails to parse, providing
// visibility into misconfigured environment variables.
func GetenvIntOrDefault(key string, defaultValue int64) int64 {
	str := os.Getenv(key)

	val, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		if str != "" {
			fmt.Fprintf(os.Stderr, "WARN: env var %s=%q is not a valid int, using default %v\n", key, str, defaultValue)
		}

		return defaultValue
	}

	return val
}

// parseEnvDuration reads a duration the way an operator writes one: "30s", "2m",
// "720h", "150ms". Surrounding whitespace is trimmed, because a value arriving from a
// Helm value or a ConfigMap routinely carries a trailing newline, and silently falling
// back on account of it is the exact failure mode a declared default exists to remove.
//
// A unit-less integer is deliberately read as NANOSECONDS rather than seconds. That is
// what the numeric value of a time.Duration means, and it is what this loader has
// always done: lerian-internal-gitops ships STREAMING_HEALTH_CHECK_TIMEOUT
// "2000000000" for br-slc, written in raw nanoseconds precisely because of it.
// Re-reading that as seconds would turn a two-second readiness timeout into roughly 63
// years, and a readiness probe that never expires leaves nothing in the logs to say so.
// Unit-less is legacy spelling that keeps working; a unit is the spelling to write.
func parseEnvDuration(raw string) (time.Duration, error) {
	str := strings.TrimSpace(raw)

	if val, err := time.ParseDuration(str); err == nil {
		return val, nil
	}

	nanos, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", raw, err)
	}

	return time.Duration(nanos), nil
}

// GetenvDurationOrDefault returns the value of os.Getenv(key string) as a time.Duration,
// or defaultValue when the variable is undefined, blank, or not a valid duration.
// Accepts a unit ("30s", "2m", "720h"); a unit-less integer is read as nanoseconds, for
// the reason given on parseEnvDuration.
// A warning is printed to stderr when a non-blank value fails to parse, matching
// GetenvBoolOrDefault and GetenvIntOrDefault, so a misconfigured variable is visible
// rather than merely absorbed.
func GetenvDurationOrDefault(key string, defaultValue time.Duration) time.Duration {
	str := os.Getenv(key)

	val, err := parseEnvDuration(str)
	if err != nil {
		if strings.TrimSpace(str) != "" {
			fmt.Fprintf(os.Stderr, "WARN: env var %s=%q is not a valid duration, using default %v\n", key, str, defaultValue)
		}

		return defaultValue
	}

	return val
}

// GetenvFloat64OrDefault returns the value of os.Getenv(key string) value as float64 or defaultValue if error.
// If the environment variable (key) is not defined, it returns the given defaultValue.
// strconv.ParseFloat is strict — trailing garbage (e.g. "0.5abc") fails and the caller receives
// the default rather than a silently truncated value.
// A warning is printed to stderr when a non-empty value fails to parse, providing
// visibility into misconfigured environment variables.
func GetenvFloat64OrDefault(key string, defaultValue float64) float64 {
	str := strings.TrimSpace(os.Getenv(key))
	if str == "" {
		return defaultValue
	}

	val, err := strconv.ParseFloat(str, 64)
	if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
		fmt.Fprintf(os.Stderr, "WARN: env var %s is not a valid float, using default %v\n", key, defaultValue)

		return defaultValue
	}

	return val
}

// LocalEnvConfig is used to automatically call the InitLocalEnvConfig method using Dependency Injection
// So, if a func parameter or a struct field depends on LocalEnvConfig, when DI starts, it will call InitLocalEnvConfig as the LocalEnvConfig provider.
type LocalEnvConfig struct {
	Initialized bool
}

var (
	localEnvConfig     *LocalEnvConfig
	localEnvConfigOnce sync.Once
)

// InitLocalEnvConfig load a .env file to set up local environment vars.
// It's called once per application process.
// Version and environment are always logged in a plain startup banner format.
func InitLocalEnvConfig() *LocalEnvConfig {
	version := GetenvOrDefault("VERSION", "NO-VERSION")
	envName := GetenvOrDefault("ENV_NAME", "local")

	fmt.Printf("VERSION: %s\n\n", version)
	fmt.Printf("ENVIRONMENT NAME: %s\n\n", envName)

	if envName == "local" {
		localEnvConfigOnce.Do(func() {
			if err := godotenv.Load(); err != nil {
				fmt.Printf("Skipping .env file; using environment: %s\n", envName)

				localEnvConfig = &LocalEnvConfig{
					Initialized: false,
				}
			} else {
				fmt.Println("Env vars loaded from .env file on process", os.Getpid())

				localEnvConfig = &LocalEnvConfig{
					Initialized: true,
				}
			}
		})
	}

	// Always return a non-nil config with safe defaults so callers never
	// need to nil-check. Non-local environments get Initialized=false.
	if localEnvConfig == nil {
		return &LocalEnvConfig{Initialized: false}
	}

	return localEnvConfig
}

// ErrNilConfig indicates that a nil configuration value was passed to SetConfigFromEnvVars.
var ErrNilConfig = errors.New("config must not be nil")

// ErrNotStruct indicates that the pointer target is not a struct.
var ErrNotStruct = errors.New("pointer must reference a struct")

// ErrUnsupportedFieldType indicates that a struct field carrying an "env" tag
// has a type SetConfigFromEnvVars cannot populate (for example a non-string
// slice, or a float/uint scalar). It is returned instead of panicking.
var ErrUnsupportedFieldType = errors.New("unsupported field type for env tag")

// ErrInvalidDefaultValue indicates that a struct field's "envDefault" tag holds a
// value that cannot be parsed into the field's type, or that is out of range for
// it. It is returned rather than silently ignored, because a default that does not
// apply is indistinguishable from no default at all — and that indistinguishability
// is the whole failure mode this tag exists to remove.
var ErrInvalidDefaultValue = errors.New("invalid envDefault value for field type")

// envDefaultTag is the struct tag holding the value a field takes when its
// environment variable is unset, blank, or unparseable for the field's type.
//
// The name matches the widely used github.com/caarlos0/env convention so that a
// reader coming from any other Go codebase reads it correctly on sight. "default"
// is NOT an accepted spelling: two spellings for one meaning is how a decorative
// tag goes unnoticed in the first place.
const envDefaultTag = "envDefault"

// durationType is time.Duration's reflect type, held here so setFieldFromEnv can
// recognise a duration field before it looks at Kind. time.Duration is defined as an
// int64, so its Kind is reflect.Int64 and no Kind-based dispatch can distinguish the
// two — which is precisely how a timeout declaring envDefault:"30" came to mean 30
// nanoseconds.
var durationType = reflect.TypeFor[time.Duration]()

// SetConfigFromEnvVars builds a struct by setting its field values using the "env" tag.
// Constraints: s must be a non-nil pointer to an initialized struct.
// Supported field types: string, bool, int/int8/int16/int32/int64, time.Duration, and
// []string (comma-separated, each element whitespace-trimmed, empty elements dropped).
// A field whose type is none of these (for example a non-string slice or a float)
// yields ErrUnsupportedFieldType rather than a panic.
//
// A time.Duration field reads a unit ("30s", "2m", "720h"), not a bare number of
// seconds; a unit-less integer is read as nanoseconds, matching time.Duration's own
// numeric meaning and this loader's historical behaviour.
//
// A field may also carry an "envDefault" tag giving the value to use when its
// variable is unset, blank, or unparseable for the field's type. Without that tag
// the field takes its zero value, which for a bool is false — so a flag that must
// be on unless an operator turns it off MUST declare the default rather than rely
// on the variable being present. An envDefault the field's type cannot hold is an
// error at load time, not a silent fall back to zero.
func SetConfigFromEnvVars(s any) error {
	if s == nil {
		return ErrNilConfig
	}

	v := reflect.ValueOf(s)

	t := v.Type()
	if t.Kind() != reflect.Pointer {
		return ErrNotPointer
	}

	// Guard against typed-nil pointers (e.g. (*MyStruct)(nil)).
	if v.IsNil() {
		return ErrNilConfig
	}

	// The pointer must reference a struct.
	if t.Elem().Kind() != reflect.Struct {
		return ErrNotStruct
	}

	e := t.Elem()
	for i := range e.NumField() {
		f := e.Field(i)
		if tag, ok := f.Tag.Lookup("env"); ok {
			values := strings.Split(tag, ",")
			if len(values) > 0 {
				fv := v.Elem().FieldByName(f.Name)
				if fv.CanSet() {
					if err := setFieldFromEnv(fv, f, values[0]); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

// setFieldFromEnv populates one struct field from the environment variable named
// name, falling back to the field's envDefault tag when the variable supplies
// nothing usable.
//
// A field with no envDefault tag behaves exactly as it did before the tag existed,
// down to a whitespace-only string being preserved verbatim: every deployed service
// reads this loader, so the no-tag path must not shift underneath them.
func setFieldFromEnv(fv reflect.Value, f reflect.StructField, name string) error {
	def, hasDefault := f.Tag.Lookup(envDefaultTag)

	// time.Duration's Kind is reflect.Int64, so a Kind-based switch cannot tell it
	// apart from a plain integer: envDefault:"30" on a timeout field meant 30
	// NANOSECONDS, and the unit-bearing envDefault:"30s" that every service in the
	// fleet actually writes failed the load outright, before the environment was even
	// read. Duration is therefore matched on its type, ahead of the Kind switch.
	if fv.Type() == durationType {
		return setDurationFromEnv(fv, f, name, def, hasDefault)
	}

	switch fv.Kind() {
	case reflect.Bool:
		fallback := false

		if hasDefault {
			parsed, err := strconv.ParseBool(def)
			if err != nil {
				return fmt.Errorf("%w: field %q declares %s=%q, which is not a bool",
					ErrInvalidDefaultValue, f.Name, envDefaultTag, def)
			}

			fallback = parsed
		}

		fv.SetBool(GetenvBoolOrDefault(name, fallback))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var fallback int64

		if hasDefault {
			parsed, err := strconv.ParseInt(def, 10, 64)
			if err != nil {
				return fmt.Errorf("%w: field %q declares %s=%q, which is not an integer",
					ErrInvalidDefaultValue, f.Name, envDefaultTag, def)
			}

			if fv.OverflowInt(parsed) {
				return fmt.Errorf("%w: field %q declares %s=%q, which does not fit in %s",
					ErrInvalidDefaultValue, f.Name, envDefaultTag, def, fv.Type())
			}

			fallback = parsed
		}

		fv.SetInt(GetenvIntOrDefault(name, fallback))
	case reflect.String:
		// GetenvOrDefault treats a blank value as absent; os.Getenv does not. Only
		// the defaulted path may take that trimming, or the no-tag path changes.
		if hasDefault {
			fv.SetString(GetenvOrDefault(name, def))

			return nil
		}

		fv.SetString(os.Getenv(name))
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("%w: field %q is %s", ErrUnsupportedFieldType, f.Name, fv.Type())
		}

		raw := os.Getenv(name)
		if hasDefault && strings.TrimSpace(raw) == "" {
			raw = def
		}

		fv.Set(reflect.ValueOf(parseEnvStringSlice(raw)))
	default:
		return fmt.Errorf("%w: field %q is %s", ErrUnsupportedFieldType, f.Name, fv.Type())
	}

	return nil
}

// setDurationFromEnv populates one time.Duration field. It mirrors the integer case
// of setFieldFromEnv — an unparseable envDefault is an error, an unparseable
// environment value falls back to the default — differing only in reading a unit.
func setDurationFromEnv(fv reflect.Value, f reflect.StructField, name, def string, hasDefault bool) error {
	var fallback time.Duration

	if hasDefault {
		parsed, err := parseEnvDuration(def)
		if err != nil {
			return fmt.Errorf("%w: field %q declares %s=%q, which is not a duration",
				ErrInvalidDefaultValue, f.Name, envDefaultTag, def)
		}

		fallback = parsed
	}

	fv.SetInt(int64(GetenvDurationOrDefault(name, fallback)))

	return nil
}

// parseEnvStringSlice splits a comma-separated environment value into a
// []string, trimming surrounding whitespace from each element and dropping
// empty elements. An empty or whitespace-only value yields an empty (non-nil)
// slice.
func parseEnvStringSlice(raw string) []string {
	out := []string{}

	for part := range strings.SplitSeq(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}

	return out
}
