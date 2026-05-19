package security

import "github.com/LerianStudio/lib-observability/redaction"

// DefaultSensitiveFields returns a copy of the default sensitive field names.
// The canonical sensitive-field taxonomy is owned by lib-observability.
func DefaultSensitiveFields() []string {
	return redaction.DefaultSensitiveFields()
}

// DefaultSensitiveFieldsMap provides a map version of DefaultSensitiveFields
// for lookup operations. Each call returns a shallow clone so callers cannot
// mutate shared state.
func DefaultSensitiveFieldsMap() map[string]bool {
	return redaction.DefaultSensitiveFieldsMap()
}

// IsSensitiveField checks if a field name is considered sensitive.
// It delegates to lib-observability/redaction so lib-commons does not drift
// from the shared credentials, PII, and financial-identifier taxonomy.
func IsSensitiveField(fieldName string) bool {
	return redaction.IsSensitiveField(fieldName)
}
