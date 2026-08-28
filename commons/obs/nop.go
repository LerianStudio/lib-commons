package obs

import "context"

// nopLogger drops every event. It is the zero-value logger used by the
// withDefaults paths across lib-commons.
type nopLogger struct{}

// Nop returns a Logger that discards every event and reports every level as
// disabled. It never returns nil, so callers can use it unconditionally as a
// default.
func Nop() Logger { return nopLogger{} }

func (nopLogger) Log(context.Context, int, string, ...any) {}

func (nopLogger) Enabled(int) bool { return false }

func (nopLogger) Sync(context.Context) error { return nil }
