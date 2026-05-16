// Package redaction provides sensitive field detection utilities.
// This package delegates to github.com/LerianStudio/lib-observability/redaction.
//
// Deprecated: This package is a compatibility shim. Import github.com/LerianStudio/lib-observability/redaction instead.
// This package will be removed in a future major version of lib-commons.
package redaction

import libobsredaction "github.com/LerianStudio/lib-observability/redaction"

// DefaultSensitiveFields is an alias for the default sensitive field names defined in lib-observability.
// It does not guarantee copy-on-access semantics; callers that need an independent copy must copy the slice explicitly.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/redaction.DefaultSensitiveFields instead.
var DefaultSensitiveFields = libobsredaction.DefaultSensitiveFields

// DefaultSensitiveFieldsMap provides a map version of DefaultSensitiveFields for lookup operations.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/redaction.DefaultSensitiveFieldsMap instead.
var DefaultSensitiveFieldsMap = libobsredaction.DefaultSensitiveFieldsMap

// IsSensitiveField checks if a field name is considered sensitive.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/redaction.IsSensitiveField instead.
var IsSensitiveField = libobsredaction.IsSensitiveField
