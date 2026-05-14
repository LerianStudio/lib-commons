package log

import libobslog "github.com/LerianStudio/lib-observability/log"

// NopLogger is a no-op logger implementation.
type NopLogger = libobslog.NopLogger

// NewNop creates a no-op logger implementation.
func NewNop() Logger {
	return libobslog.NewNop()
}
