package log

import libobslog "github.com/LerianStudio/lib-observability/log"

// NopLogger is a no-op logger implementation.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.NopLogger instead.
type NopLogger = libobslog.NopLogger

// NewNop creates a no-op logger implementation.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/log.NewNop instead.
func NewNop() Logger {
	return libobslog.NewNop()
}
