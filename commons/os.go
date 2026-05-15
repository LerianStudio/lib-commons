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

// SetConfigFromEnvVars builds a struct by setting its field values using the "env" tag.
// Constraints: s must be a non-nil pointer to an initialized struct.
// Supported types: String, Boolean, Int, Int8, Int16, Int32 and Int64.
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
					switch k := fv.Kind(); k {
					case reflect.Bool:
						fv.SetBool(GetenvBoolOrDefault(values[0], false))
					case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
						fv.SetInt(GetenvIntOrDefault(values[0], 0))
					default:
						fv.SetString(os.Getenv(values[0]))
					}
				}
			}
		}
	}

	return nil
}
