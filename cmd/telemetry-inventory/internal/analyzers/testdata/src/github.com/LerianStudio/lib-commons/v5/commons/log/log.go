package log

import "context"

type Level uint8

const (
	LevelError Level = iota
	LevelWarn
	LevelInfo
	LevelDebug
)

type Field struct{}

// Logger mirrors the canonical 5-method interface from
// commons/log/log.go. Do NOT add convenience methods (Debug/Info/Warn/Error)
// here: they are not on the real interface, and the logfield analyzer's
// matcher is scoped to the canonical shape so an over-broad stub would
// allow the analyzer to drift from real lib-commons code.
type Logger interface {
	Log(context.Context, Level, string, ...Field)
	With(...Field) Logger
	WithGroup(string) Logger
	Enabled(Level) bool
	Sync(context.Context) error
}

func String(string, string) Field { return Field{} }
func Int(string, int) Field       { return Field{} }
func Bool(string, bool) Field     { return Field{} }
func Err(error) Field             { return Field{} }
func Any(string, any) Field       { return Field{} }
