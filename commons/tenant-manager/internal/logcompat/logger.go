package logcompat

import (
	"context"
	"fmt"

	libcommons "github.com/LerianStudio/lib-commons/v4/commons"
	liblog "github.com/LerianStudio/lib-commons/v4/commons/log"
	tmlog "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/log"
)

type Logger struct {
	base liblog.Logger
}

func New(logger liblog.Logger) *Logger {
	if logger == nil {
		logger = liblog.NewNop()
	}

	return &Logger{base: tmlog.NewTenantAwareLogger(logger)}
}

func FromContext(ctx context.Context) *Logger {
	baseLogger, _, _, _ := libcommons.NewTrackingFromContext(ctx) //nolint:dogsled

	return New(baseLogger)
}

func Prefer(preferred, fallback *Logger) *Logger {
	if preferred != nil {
		return preferred
	}

	if fallback != nil {
		return fallback
	}

	return New(nil)
}

func (l *Logger) WithFields(kv ...any) *Logger {
	if l == nil || l.base == nil {
		return New(nil)
	}

	return &Logger{base: l.base.With(toFields(kv...)...)}
}

func (l *Logger) enabled(level liblog.Level) bool {
	return l != nil && l.base != nil && l.base.Enabled(level)
}

func (l *Logger) log(ctx context.Context, level liblog.Level, msg string) {
	if l == nil || l.base == nil {
		return
	}

	if ctx == nil {
		ctx = context.Background()
	}

	l.base.Log(ctx, level, msg)
}

func (l *Logger) logArgs(ctx context.Context, level liblog.Level, args ...any) {
	if !l.enabled(level) {
		return
	}

	l.log(ctx, level, fmt.Sprint(args...))
}

func (l *Logger) logf(ctx context.Context, level liblog.Level, format string, args ...any) {
	if !l.enabled(level) {
		return
	}

	l.log(ctx, level, fmt.Sprintf(format, args...))
}

func (l *Logger) InfoCtx(ctx context.Context, args ...any) {
	l.logArgs(ctx, liblog.LevelInfo, args...)
}

func (l *Logger) WarnCtx(ctx context.Context, args ...any) {
	l.logArgs(ctx, liblog.LevelWarn, args...)
}

func (l *Logger) ErrorCtx(ctx context.Context, args ...any) {
	l.logArgs(ctx, liblog.LevelError, args...)
}

func (l *Logger) InfofCtx(ctx context.Context, f string, args ...any) {
	l.logf(ctx, liblog.LevelInfo, f, args...)
}

func (l *Logger) WarnfCtx(ctx context.Context, f string, args ...any) {
	l.logf(ctx, liblog.LevelWarn, f, args...)
}

func (l *Logger) ErrorfCtx(ctx context.Context, f string, args ...any) {
	l.logf(ctx, liblog.LevelError, f, args...)
}

func (l *Logger) Info(args ...any) {
	l.logArgs(context.Background(), liblog.LevelInfo, args...)
}

func (l *Logger) Warn(args ...any) {
	l.logArgs(context.Background(), liblog.LevelWarn, args...)
}

func (l *Logger) Error(args ...any) {
	l.logArgs(context.Background(), liblog.LevelError, args...)
}

func (l *Logger) Infof(f string, args ...any) {
	l.logf(context.Background(), liblog.LevelInfo, f, args...)
}

func (l *Logger) Warnf(f string, args ...any) {
	l.logf(context.Background(), liblog.LevelWarn, f, args...)
}

func (l *Logger) Errorf(f string, args ...any) {
	l.logf(context.Background(), liblog.LevelError, f, args...)
}

func (l *Logger) Base() liblog.Logger {
	if l == nil || l.base == nil {
		return liblog.NewNop()
	}

	return l.base
}

func toFields(kv ...any) []liblog.Field {
	if len(kv) == 0 {
		return nil
	}

	fields := make([]liblog.Field, 0, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
		key := fmt.Sprintf("arg_%d", i)
		if ks, ok := kv[i].(string); ok && ks != "" {
			key = ks
		}

		if i+1 >= len(kv) {
			fields = append(fields, liblog.Any(key, nil))
			continue
		}

		fields = append(fields, liblog.Any(key, kv[i+1]))
	}

	return fields
}
