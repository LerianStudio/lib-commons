package log

import (
	"context"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	tmcore "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

type TenantAwareLogger struct {
	base log.Logger
}

// NewTenantAwareLogger wraps base so that every Log call automatically
// injects the tenant_id field from context.  A nil base is replaced with
// a no-op logger to prevent nil-dereference panics.
func NewTenantAwareLogger(base log.Logger) *TenantAwareLogger {
	if base == nil {
		base = log.NewNop()
	}

	return &TenantAwareLogger{base: base}
}

func (l *TenantAwareLogger) Log(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
	if ctx == nil {
		ctx = context.Background()
	}

	if tenantID := tmcore.GetTenantID(ctx); tenantID != "" {
		fields = append(fields, log.String("tenant_id", tenantID))
	}

	l.base.Log(ctx, level, msg, fields...)
}

// With returns a new TenantAwareLogger that carries the additional fields
// while preserving the tenant_id injection behavior on every Log call.
func (l *TenantAwareLogger) With(fields ...log.Field) log.Logger {
	return &TenantAwareLogger{base: l.base.With(fields...)}
}

// WithGroup returns a new TenantAwareLogger scoped under the named group
// while preserving the tenant_id injection behavior on every Log call.
func (l *TenantAwareLogger) WithGroup(name string) log.Logger {
	return &TenantAwareLogger{base: l.base.WithGroup(name)}
}

func (l *TenantAwareLogger) Enabled(level log.Level) bool {
	return l.base.Enabled(level)
}

func (l *TenantAwareLogger) Sync(ctx context.Context) error {
	return l.base.Sync(ctx)
}
