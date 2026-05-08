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

type Logger interface {
	Log(context.Context, Level, string, ...Field)
	With(...Field) Logger
	WithGroup(string) Logger
	Enabled(Level) bool
	Sync(context.Context) error
	Debug(context.Context, string, ...Field)
	Info(context.Context, string, ...Field)
	Warn(context.Context, string, ...Field)
	Error(context.Context, string, ...Field)
}

func String(string, string) Field { return Field{} }
func Int(string, int) Field       { return Field{} }
func Bool(string, bool) Field     { return Field{} }
func Err(error) Field             { return Field{} }
func Any(string, any) Field       { return Field{} }
