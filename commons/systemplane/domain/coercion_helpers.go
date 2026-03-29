// Copyright 2025 Lerian Studio.

package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type boolStringResult struct {
	value bool
	ok    bool
}

func coerceString(raw any, fallback string) string {
	value, ok := tryCoerceString(raw)
	if !ok {
		return fallback
	}

	return value
}

func tryCoerceString(raw any) (string, bool) {
	if IsNilValue(raw) {
		return "", false
	}

	switch v := raw.(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	case fmt.Stringer:
		return safeStringerString(v)
	default:
		return safeSprint(raw)
	}
}

func safeStringerString(value fmt.Stringer) (result string, ok bool) {
	if IsNilValue(value) {
		return "", false
	}

	defer func() {
		if recover() != nil {
			result = ""
			ok = false
		}
	}()

	return value.String(), true
}

func safeSprint(raw any) (result string, ok bool) {
	defer func() {
		if recover() != nil {
			result = ""
			ok = false
		}
	}()

	return fmt.Sprint(raw), true
}

func coerceInt(raw any, fallback int) int {
	value, ok := tryCoerceInt(raw)
	if !ok {
		return fallback
	}

	return value
}

func tryCoerceInt(raw any) (int, bool) {
	if raw == nil {
		return 0, false
	}

	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return intFromInt64(v)
	case float64:
		return intFromFloat64(v)
	case string:
		return intFromString(v)
	case json.Number:
		return intFromJSONNumber(v)
	default:
		return 0, false
	}
}

func intFromInt64(value int64) (int, bool) {
	if value > math.MaxInt || value < math.MinInt {
		return 0, false
	}

	return int(value), true
}

// intFromFloat64 converts a float64 to int with overflow/NaN protection.
// Uses float64(math.MaxInt)+1 for platform-independent bounds checking.
func intFromFloat64(value float64) (int, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}

	truncated := math.Trunc(value)

	// float64(math.MaxInt) rounds up on 64-bit platforms, so use >= with
	// the exact float representation of MaxInt+1 for platform-independent bounds.
	const maxIntPlusOne = float64(math.MaxInt) + 1
	if truncated >= maxIntPlusOne || truncated < math.MinInt {
		return 0, false
	}

	return int(truncated), true
}

func intFromString(value string) (int, bool) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}

	return parsed, true
}

func intFromJSONNumber(value json.Number) (int, bool) {
	parsed, err := value.Int64()
	if err != nil {
		return 0, false
	}

	return intFromInt64(parsed)
}

func coerceBool(raw any, fallback bool) bool {
	value, ok := tryCoerceBool(raw)
	if !ok {
		return fallback
	}

	return value
}

func tryCoerceBool(raw any) (bool, bool) {
	if raw == nil {
		return false, false
	}

	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		parsed := parseCanonicalBoolString(v)
		return parsed.value, parsed.ok
	case int:
		switch v {
		case 0:
			return false, true
		case 1:
			return true, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func parseCanonicalBoolString(value string) boolStringResult {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1":
		return boolStringResult{value: true, ok: true}
	case "false", "0":
		return boolStringResult{value: false, ok: true}
	default:
		return boolStringResult{}
	}
}

func coerceFloat64(raw any, fallback float64) float64 {
	value, ok := tryCoerceFloat64(raw)
	if !ok {
		return fallback
	}

	return value
}

func tryCoerceFloat64(raw any) (float64, bool) {
	if raw == nil {
		return 0, false
	}

	switch v := raw.(type) {
	case float64:
		return finiteFloat64(v)
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}

		return finiteFloat64(f)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}

		return finiteFloat64(f)
	default:
		return 0, false
	}
}

func finiteFloat64(value float64) (float64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}

	return value, true
}

func coerceDuration(raw any, fallback time.Duration, unit time.Duration) time.Duration {
	value, ok := tryCoerceDuration(raw, unit)
	if !ok {
		return fallback
	}

	return value
}

func tryCoerceDuration(raw any, unit time.Duration) (time.Duration, bool) {
	if raw == nil {
		return 0, false
	}

	switch v := raw.(type) {
	case int:
		return scaleDurationInt64(int64(v), unit)
	case int64:
		return scaleDurationInt64(v, unit)
	case float64:
		return scaleDurationFloat64(v, unit)
	case string:
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, false
		}

		return d, true
	default:
		return 0, false
	}
}

func coerceStringSlice(raw any, fallback []string) []string {
	value, ok := tryCoerceStringSlice(raw)
	if !ok {
		return cloneStringSlice(fallback)
	}

	return value
}

func tryCoerceStringSlice(raw any) ([]string, bool) {
	if IsNilValue(raw) {
		return nil, false
	}

	switch v := raw.(type) {
	case []string:
		result := make([]string, 0, len(v))
		return append(result, v...), true
	case []any:
		result := make([]string, 0, len(v))
		for _, elem := range v {
			stringValue, ok := elem.(string)
			if !ok {
				return nil, false
			}

			trimmed := strings.TrimSpace(stringValue)
			if trimmed == "" {
				continue
			}

			result = append(result, trimmed)
		}

		return result, true
	case string:
		parts := strings.Split(v, ",")

		result := make([]string, 0, len(parts))
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if trimmed == "" {
				continue
			}

			result = append(result, trimmed)
		}

		return result, true
	default:
		return nil, false
	}
}

func scaleDurationInt64(value int64, unit time.Duration) (time.Duration, bool) {
	if value == 0 || unit == 0 {
		return 0, true
	}

	scaled := time.Duration(value) * unit
	if scaled/unit != time.Duration(value) {
		return 0, false
	}

	return scaled, true
}

func scaleDurationFloat64(value float64, unit time.Duration) (time.Duration, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}

	if value == 0 || unit == 0 {
		return 0, true
	}

	scaled := value * float64(unit)

	// Same boundary alias as intFromFloat64: float64(MaxInt64) rounds up
	// on 64-bit. Use float64(math.MaxInt64)+1 for platform-independent bounds.
	const maxInt64PlusOne = float64(math.MaxInt64) + 1
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled >= maxInt64PlusOne || scaled < math.MinInt64 {
		return 0, false
	}

	return time.Duration(scaled), true
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}

	return append([]string(nil), values...)
}
