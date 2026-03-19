package log

import (
	"context"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	tmcore "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

type TenantAwareLogger struct {
	base log.Logger
}

func NewTenantAwareLogger(base log.Logger) *TenantAwareLogger {
	return &TenantAwareLogger{base: base}
}

func (l *TenantAwareLogger) Log(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
	if ctx == nil {
		ctx = context.Background()
	}

	if tenantID := tmcore.GetTenantIDFromContext(ctx); tenantID != "" {
		fields = append(fields, log.String("tenant_id", tenantID))
	}

	l.base.Log(ctx, level, msg, fields...)
}

func (l *TenantAwareLogger) With(fields ...log.Field) log.Logger {
	return l.base.With(fields...)
}

func (l *TenantAwareLogger) WithGroup(name string) log.Logger {
	return l.base.WithGroup(name)
}

func (l *TenantAwareLogger) Enabled(level log.Level) bool {
	return l.base.Enabled(level)
}

func (l *TenantAwareLogger) Sync(ctx context.Context) error {
	return l.base.Sync(ctx)
}
