package logcompat

import (
	"context"
	"fmt"

	liblog "github.com/LerianStudio/lib-commons/v5/commons/log"
	tmlog "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/log"
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

func (l *Logger) InfoCtx(ctx context.Context, args ...any) {
	if !l.enabled(liblog.LevelInfo) {
		return
	}

	l.log(ctx, liblog.LevelInfo, fmt.Sprint(args...))
}

func (l *Logger) WarnCtx(ctx context.Context, args ...any) {
	if !l.enabled(liblog.LevelWarn) {
		return
	}

	l.log(ctx, liblog.LevelWarn, fmt.Sprint(args...))
}

func (l *Logger) ErrorCtx(ctx context.Context, args ...any) {
	if !l.enabled(liblog.LevelError) {
		return
	}

	l.log(ctx, liblog.LevelError, fmt.Sprint(args...))
}

func (l *Logger) InfofCtx(ctx context.Context, f string, args ...any) {
	if !l.enabled(liblog.LevelInfo) {
		return
	}

	l.log(ctx, liblog.LevelInfo, fmt.Sprintf(f, args...))
}

func (l *Logger) WarnfCtx(ctx context.Context, f string, args ...any) {
	if !l.enabled(liblog.LevelWarn) {
		return
	}

	l.log(ctx, liblog.LevelWarn, fmt.Sprintf(f, args...))
}

func (l *Logger) ErrorfCtx(ctx context.Context, f string, args ...any) {
	if !l.enabled(liblog.LevelError) {
		return
	}

	l.log(ctx, liblog.LevelError, fmt.Sprintf(f, args...))
}

func (l *Logger) Info(args ...any) {
	if !l.enabled(liblog.LevelInfo) {
		return
	}

	l.log(context.Background(), liblog.LevelInfo, fmt.Sprint(args...))
}

func (l *Logger) Warn(args ...any) {
	if !l.enabled(liblog.LevelWarn) {
		return
	}

	l.log(context.Background(), liblog.LevelWarn, fmt.Sprint(args...))
}

func (l *Logger) Error(args ...any) {
	if !l.enabled(liblog.LevelError) {
		return
	}

	l.log(context.Background(), liblog.LevelError, fmt.Sprint(args...))
}

func (l *Logger) Debug(args ...any) {
	if !l.enabled(liblog.LevelDebug) {
		return
	}

	l.log(context.Background(), liblog.LevelDebug, fmt.Sprint(args...))
}

func (l *Logger) Infof(f string, args ...any) {
	if !l.enabled(liblog.LevelInfo) {
		return
	}

	l.log(context.Background(), liblog.LevelInfo, fmt.Sprintf(f, args...))
}

func (l *Logger) Warnf(f string, args ...any) {
	if !l.enabled(liblog.LevelWarn) {
		return
	}

	l.log(context.Background(), liblog.LevelWarn, fmt.Sprintf(f, args...))
}

func (l *Logger) Errorf(f string, args ...any) {
	if !l.enabled(liblog.LevelError) {
		return
	}

	l.log(context.Background(), liblog.LevelError, fmt.Sprintf(f, args...))
}

func (l *Logger) Debugf(f string, args ...any) {
	if !l.enabled(liblog.LevelDebug) {
		return
	}

	l.log(context.Background(), liblog.LevelDebug, fmt.Sprintf(f, args...))
}

func (l *Logger) Sync() error {
	if l == nil || l.base == nil {
		return nil
	}

	return l.base.Sync(context.Background())
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
