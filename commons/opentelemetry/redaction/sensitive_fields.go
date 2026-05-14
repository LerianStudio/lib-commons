// Package redaction provides sensitive field detection utilities.
// This package delegates to github.com/LerianStudio/lib-observability/redaction.
package redaction

import libobsredaction "github.com/LerianStudio/lib-observability/redaction"

// DefaultSensitiveFields returns a copy of the default sensitive field names.
var DefaultSensitiveFields = libobsredaction.DefaultSensitiveFields

// DefaultSensitiveFieldsMap provides a map version of DefaultSensitiveFields for lookup operations.
var DefaultSensitiveFieldsMap = libobsredaction.DefaultSensitiveFieldsMap

// IsSensitiveField checks if a field name is considered sensitive.
var IsSensitiveField = libobsredaction.IsSensitiveField
