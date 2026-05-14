package log

import libobslog "github.com/LerianStudio/lib-observability/log"

// SafeError logs errors with explicit production-aware sanitization.
// When production is true, only the error type is logged (no message details).
var SafeError = libobslog.SafeError

// SanitizeExternalResponse removes potentially sensitive external response data.
// Returns only status code for error messages.
var SanitizeExternalResponse = libobslog.SanitizeExternalResponse
