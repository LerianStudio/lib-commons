package opentelemetry

import (
	"encoding/json"
	"strings"

	cn "github.com/LerianStudio/lib-commons/v2/commons/constants"
	"github.com/LerianStudio/lib-commons/v2/commons/security"
)

// FieldObfuscator defines the interface for obfuscating sensitive fields in structs.
// Implementations can provide custom logic for determining which fields to obfuscate
// and how to obfuscate them.
type FieldObfuscator interface {
	// ShouldObfuscate returns true if the given field name should be obfuscated
	ShouldObfuscate(fieldName string) bool
	// GetObfuscatedValue returns the value to use for obfuscated fields
	GetObfuscatedValue() string
}

// DefaultObfuscator provides a simple implementation that obfuscates
// common sensitive field names using the security package's word-boundary matching.
type DefaultObfuscator struct {
	obfuscatedValue string
}

// NewDefaultObfuscator creates a new DefaultObfuscator with common sensitive field names.
// Uses the shared sensitive fields list from the security package to ensure consistency
// across HTTP logging, OpenTelemetry spans, and other components.
func NewDefaultObfuscator() *DefaultObfuscator {
	return &DefaultObfuscator{
		obfuscatedValue: cn.ObfuscatedValue,
	}
}

// ShouldObfuscate returns true if the field name is in the sensitive fields list.
// Delegates to security.IsSensitiveField for consistent word-boundary matching
// across all components (HTTP logging, OpenTelemetry spans, URL sanitization).
func (o *DefaultObfuscator) ShouldObfuscate(fieldName string) bool {
	return security.IsSensitiveField(fieldName)
}

// CustomObfuscator provides an implementation that obfuscates
// only the specific field names provided during creation.
type CustomObfuscator struct {
	sensitiveFields map[string]bool
	obfuscatedValue string
}

// NewCustomObfuscator creates a new CustomObfuscator with custom sensitive field names.
// Uses simple case-insensitive matching against the provided fields only.
func NewCustomObfuscator(sensitiveFields []string) *CustomObfuscator {
	fieldMap := make(map[string]bool, len(sensitiveFields))
	for _, field := range sensitiveFields {
		fieldMap[strings.ToLower(field)] = true
	}

	return &CustomObfuscator{
		sensitiveFields: fieldMap,
		obfuscatedValue: cn.ObfuscatedValue,
	}
}

// ShouldObfuscate returns true if the field name matches one of the custom sensitive fields.
// Uses simple case-insensitive matching (not word-boundary matching).
func (o *CustomObfuscator) ShouldObfuscate(fieldName string) bool {
	return o.sensitiveFields[strings.ToLower(fieldName)]
}

// GetObfuscatedValue returns the obfuscated value.
func (o *CustomObfuscator) GetObfuscatedValue() string {
	return o.obfuscatedValue
}

// GetObfuscatedValue returns the obfuscated value.
func (o *DefaultObfuscator) GetObfuscatedValue() string {
	return o.obfuscatedValue
}

// obfuscateStructFields recursively obfuscates sensitive fields in a struct or map.
func obfuscateStructFields(data any, obfuscator FieldObfuscator) any {
	switch v := data.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))

		for key, value := range v {
			if obfuscator.ShouldObfuscate(key) {
				result[key] = obfuscator.GetObfuscatedValue()
			} else {
				result[key] = obfuscateStructFields(value, obfuscator)
			}
		}

		return result

	case []any:
		result := make([]any, len(v))

		for i, item := range v {
			result[i] = obfuscateStructFields(item, obfuscator)
		}

		return result

	default:
		return data
	}
}

// ObfuscateStruct applies obfuscation to a struct and returns the obfuscated data.
// This is a utility function that can be used independently of OpenTelemetry spans.
func ObfuscateStruct(valueStruct any, obfuscator FieldObfuscator) (any, error) {
	if obfuscator == nil {
		return valueStruct, nil
	}

	// Convert to JSON and back to get a map[string]any representation
	jsonBytes, err := json.Marshal(valueStruct)
	if err != nil {
		return nil, err
	}

	var structData map[string]any
	if err := json.Unmarshal(jsonBytes, &structData); err != nil {
		return nil, err
	}

	return obfuscateStructFields(structData, obfuscator), nil
}
