package logcompat

import (
	"context"
	"fmt"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	tmlog "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/log"
)

type Logger struct {
	base obs.Logger
}

func New(logger obs.Logger) *Logger {
	if logger == nil {
		logger = obs.Nop()
	}

	return &Logger{base: tmlog.NewTenantAwareLogger(logger)}
}

func (l *Logger) WithFields(kv ...any) *Logger {
	if l == nil || l.base == nil {
		return New(nil)
	}

	return &Logger{base: obs.With(l.base, kv...)}
}

func (l *Logger) enabled(level int) bool {
	return l != nil && l.base != nil && l.base.Enabled(level)
}

func (l *Logger) log(ctx context.Context, level int, msg string) {
	if l == nil || l.base == nil {
		return
	}

	if ctx == nil {
		ctx = context.Background()
	}

	l.base.Log(ctx, level, msg)
}

func (l *Logger) InfoCtx(ctx context.Context, args ...any) {
	if !l.enabled(obs.LevelInfo) {
		return
	}

	l.log(ctx, obs.LevelInfo, fmt.Sprint(args...))
}

func (l *Logger) WarnCtx(ctx context.Context, args ...any) {
	if !l.enabled(obs.LevelWarn) {
		return
	}

	l.log(ctx, obs.LevelWarn, fmt.Sprint(args...))
}

func (l *Logger) ErrorCtx(ctx context.Context, args ...any) {
	if !l.enabled(obs.LevelError) {
		return
	}

	l.log(ctx, obs.LevelError, fmt.Sprint(args...))
}

func (l *Logger) InfofCtx(ctx context.Context, f string, args ...any) {
	if !l.enabled(obs.LevelInfo) {
		return
	}

	l.log(ctx, obs.LevelInfo, fmt.Sprintf(f, args...))
}

func (l *Logger) WarnfCtx(ctx context.Context, f string, args ...any) {
	if !l.enabled(obs.LevelWarn) {
		return
	}

	l.log(ctx, obs.LevelWarn, fmt.Sprintf(f, args...))
}

func (l *Logger) ErrorfCtx(ctx context.Context, f string, args ...any) {
	if !l.enabled(obs.LevelError) {
		return
	}

	l.log(ctx, obs.LevelError, fmt.Sprintf(f, args...))
}

func (l *Logger) Info(args ...any) {
	if !l.enabled(obs.LevelInfo) {
		return
	}

	l.log(context.Background(), obs.LevelInfo, fmt.Sprint(args...))
}

func (l *Logger) Warn(args ...any) {
	if !l.enabled(obs.LevelWarn) {
		return
	}

	l.log(context.Background(), obs.LevelWarn, fmt.Sprint(args...))
}

func (l *Logger) Error(args ...any) {
	if !l.enabled(obs.LevelError) {
		return
	}

	l.log(context.Background(), obs.LevelError, fmt.Sprint(args...))
}

func (l *Logger) Debug(args ...any) {
	if !l.enabled(obs.LevelDebug) {
		return
	}

	l.log(context.Background(), obs.LevelDebug, fmt.Sprint(args...))
}

func (l *Logger) Infof(f string, args ...any) {
	if !l.enabled(obs.LevelInfo) {
		return
	}

	l.log(context.Background(), obs.LevelInfo, fmt.Sprintf(f, args...))
}

func (l *Logger) Warnf(f string, args ...any) {
	if !l.enabled(obs.LevelWarn) {
		return
	}

	l.log(context.Background(), obs.LevelWarn, fmt.Sprintf(f, args...))
}

func (l *Logger) Errorf(f string, args ...any) {
	if !l.enabled(obs.LevelError) {
		return
	}

	l.log(context.Background(), obs.LevelError, fmt.Sprintf(f, args...))
}

func (l *Logger) Debugf(f string, args ...any) {
	if !l.enabled(obs.LevelDebug) {
		return
	}

	l.log(context.Background(), obs.LevelDebug, fmt.Sprintf(f, args...))
}

func (l *Logger) Sync() error {
	if l == nil || l.base == nil {
		return nil
	}

	return l.base.Sync(context.Background())
}

func (l *Logger) Base() obs.Logger {
	if l == nil || l.base == nil {
		return obs.Nop()
	}

	return l.base
}
