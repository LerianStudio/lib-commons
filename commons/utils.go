package commons

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"time"

	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry/metrics"
	"github.com/google/uuid"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
)

var internalServicePattern = regexp.MustCompile(`^[\w-]+/[\d.]+\s+LerianStudio$`)

// Contains checks if an item is in a slice. This function uses type parameters to work with any slice type.
func Contains[T comparable](slice []T, item T) bool {
	return slices.Contains(slice, item)
}

// CheckMetadataKeyAndValueLength check the length of key and value to a limit pass by on field limit
func CheckMetadataKeyAndValueLength(limit int, metadata map[string]any) error {
	for k, v := range metadata {
		if len(k) > limit {
			return cn.ErrMetadataKeyLengthExceeded
		}

		var value string

		switch t := v.(type) {
		case nil:
			continue // nil values are valid, skip length check
		case int:
			value = strconv.Itoa(t)
		case float64:
			value = strconv.FormatFloat(t, 'f', -1, 64)
		case string:
			value = t
		case bool:
			value = strconv.FormatBool(t)
		default:
			value = fmt.Sprintf("%v", t) // convert unknown types to string for length check
		}

		if len(value) > limit {
			return cn.ErrMetadataValueLengthExceeded
		}
	}

	return nil
}

// SafeIntToUint64 converts int to uint64 with safety clamping.
// Negative values are mapped to 1 (not 0) because this function is typically
// used where the result serves as a divisor or count, and zero would cause
// a division-by-zero panic. Using 1 as the safe minimum preserves
// arithmetic safety while signaling an unexpected input.
func SafeIntToUint64(val int) uint64 {
	if val < 0 {
		return uint64(1)
	}

	return uint64(val)
}

// SafeInt64ToInt safely converts int64 to int
func SafeInt64ToInt(val int64) int {
	if val > math.MaxInt {
		return math.MaxInt
	} else if val < math.MinInt {
		return math.MinInt
	}

	return int(val)
}

// SafeUintToInt converts a uint to int64 safely by capping values at math.MaxInt64.
func SafeUintToInt(val uint) int {
	if val > uint(math.MaxInt) {
		return math.MaxInt
	}

	return int(val)
}

// SafeIntToUint32 safely converts int to uint32 with overflow protection.
// Returns the converted value if in valid range [0, MaxUint32], otherwise returns defaultVal.
// This prevents G115 (CWE-190) integer overflow vulnerabilities.
func SafeIntToUint32(value int, defaultVal uint32, logger log.Logger, fieldName string) uint32 {
	if value < 0 {
		if logger != nil {
			logger.Log(
				context.Background(),
				log.LevelDebug,
				"invalid uint32 source value, using default",
				log.String("field_name", fieldName),
				log.Int("value", value),
				log.Int("default", int(defaultVal)),
			)
		}

		return defaultVal
	}

	uv := uint64(value)

	if uv > uint64(math.MaxUint32) {
		if logger != nil {
			logger.Log(
				context.Background(),
				log.LevelDebug,
				"uint32 source value exceeds max, using default",
				log.String("field_name", fieldName),
				log.Int("value", value),
				log.Any("max", uint64(math.MaxUint32)),
				log.Int("default", int(defaultVal)),
			)
		}

		return defaultVal
	}

	return uint32(uv)
}

// IsUUID Validate if the string pass through is an uuid
func IsUUID(s string) bool {
	_, err := uuid.Parse(s)

	return err == nil
}

// GenerateUUIDv7 generates a new UUID v7 using the google/uuid package.
// Returns the generated UUID or an error if crypto/rand fails.
func GenerateUUIDv7() (uuid.UUID, error) {
	return uuid.NewV7()
}

// StructToJSONString convert a struct to json string
func StructToJSONString(s any) (string, error) {
	jsonByte, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("struct to JSON: %w", err)
	}

	return string(jsonByte), nil
}

// MergeMaps Following the JSON Merge Patch.
// If target is nil, a new map is created.
func MergeMaps(source, target map[string]any) map[string]any {
	if target == nil {
		target = make(map[string]any)
	}

	for key, value := range source {
		if value != nil {
			target[key] = value
		} else {
			delete(target, key)
		}
	}

	return target
}

// SyscmdI abstracts command execution for testing and composition.
type SyscmdI interface {
	ExecCmd(ctx context.Context, name string, arg ...string) ([]byte, error)
}

// Syscmd is the default SyscmdI implementation backed by os/exec.
type Syscmd struct{}

// ExecCmd runs a command and returns its stdout bytes.
func (r *Syscmd) ExecCmd(ctx context.Context, name string, arg ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// #nosec G204 -- arguments are passed directly to exec.CommandContext (no shell interpretation); callers are responsible for input validation
	return exec.CommandContext(ctx, name, arg...).Output()
}

// GetCPUUsage reads the current CPU usage and records it through the MetricsFactory gauge.
// If factory is nil, the reading is performed but metric recording is skipped.
func GetCPUUsage(ctx context.Context, factory *metrics.MetricsFactory) {
	logger := NewLoggerFromContext(ctx)

	out, err := cpu.Percent(100*time.Millisecond, false)
	if err != nil {
		logger.Log(ctx, log.LevelWarn, "error getting CPU usage", log.Err(err))
	}

	var percentageCPU int64 = 0
	if len(out) > 0 {
		percentageCPU = int64(out[0])
	}

	if factory == nil {
		logger.Log(ctx, log.LevelWarn, "metrics factory is nil, skipping CPU usage recording")
		return
	}

	if err := factory.RecordSystemCPUUsage(ctx, percentageCPU); err != nil {
		logger.Log(ctx, log.LevelWarn, "error recording CPU gauge", log.Err(err))
	}
}

// GetMemUsage reads the current memory usage and records it through the MetricsFactory gauge.
// If factory is nil, the reading is performed but metric recording is skipped.
func GetMemUsage(ctx context.Context, factory *metrics.MetricsFactory) {
	logger := NewLoggerFromContext(ctx)

	var percentageMem int64 = 0

	out, err := mem.VirtualMemory()
	if err != nil {
		logger.Log(ctx, log.LevelWarn, "error getting memory info", log.Err(err))
	} else {
		percentageMem = int64(out.UsedPercent)
	}

	if factory == nil {
		logger.Log(ctx, log.LevelWarn, "metrics factory is nil, skipping memory usage recording")
		return
	}

	if err := factory.RecordSystemMemUsage(ctx, percentageMem); err != nil {
		logger.Log(ctx, log.LevelWarn, "error recording memory gauge", log.Err(err))
	}
}

// GetMapNumKinds get the map of numeric kinds to use in validations and conversions.
//
// The numeric kinds are:
// - int
// - int8
// - int16
// - int32
// - int64
// - float32
// - float64
func GetMapNumKinds() map[reflect.Kind]bool {
	numKinds := make(map[reflect.Kind]bool)

	numKinds[reflect.Int] = true
	numKinds[reflect.Int8] = true
	numKinds[reflect.Int16] = true
	numKinds[reflect.Int32] = true
	numKinds[reflect.Int64] = true
	numKinds[reflect.Float32] = true
	numKinds[reflect.Float64] = true

	return numKinds
}

// Reverse reverses a slice of any type.
func Reverse[T any](s []T) []T {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}

	return s
}

// UUIDsToStrings converts a slice of UUIDs to a slice of strings.
// It's optimized to minimize allocations and iterations.
func UUIDsToStrings(uuids []uuid.UUID) []string {
	result := make([]string, len(uuids))
	for i := range uuids {
		result[i] = uuids[i].String()
	}

	return result
}

// IsInternalLerianService reports whether a user-agent belongs to a Lerian internal service.
func IsInternalLerianService(userAgent string) bool {
	return internalServicePattern.MatchString(userAgent)
}
