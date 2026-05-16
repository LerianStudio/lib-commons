package log

import libobslog "github.com/LerianStudio/lib-observability/log"

// SafeError logs errors with explicit production-aware sanitization.
// When production is true, only the error type is logged (no message details).
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.SafeError instead.
var SafeError = libobslog.SafeError

// SanitizeExternalResponse removes potentially sensitive external response data.
// Returns only status code for error messages.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.SanitizeExternalResponse instead.
var SanitizeExternalResponse = libobslog.SanitizeExternalResponse
