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
// common sensitive field names using a predefined list.
type DefaultObfuscator struct {
	sensitiveFields map[string]bool
	obfuscatedValue string
}

// NewDefaultObfuscator creates a new DefaultObfuscator with common sensitive field names.
// Uses the shared sensitive fields list from the security package to ensure consistency
// across HTTP logging, OpenTelemetry spans, and other components.
func NewDefaultObfuscator() *DefaultObfuscator {
	return &DefaultObfuscator{
		sensitiveFields: security.DefaultSensitiveFieldsMap(),
		obfuscatedValue: cn.ObfuscatedValue,
	}
}

// NewCustomObfuscator creates a new DefaultObfuscator with custom sensitive field names.
func NewCustomObfuscator(sensitiveFields []string) *DefaultObfuscator {
	fieldMap := make(map[string]bool, len(sensitiveFields))
	for _, field := range sensitiveFields {
		fieldMap[strings.ToLower(field)] = true
	}

	return &DefaultObfuscator{
		sensitiveFields: fieldMap,
		obfuscatedValue: cn.ObfuscatedValue,
	}
}

// ShouldObfuscate returns true if the field name is in the sensitive fields list.
// Uses both exact match and substring matching to catch variations like
// "user_password", "api-key", etc., consistent with security.IsSensitiveField.
func (o *DefaultObfuscator) ShouldObfuscate(fieldName string) bool {
	lowerField := strings.ToLower(fieldName)

	if o.sensitiveFields[lowerField] {
		return true
	}

	for field := range o.sensitiveFields {
		if strings.Contains(lowerField, field) {
			return true
		}
	}

	return false
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
