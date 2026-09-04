package log

import (
	"context"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	tmcore "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
)

// TenantAwareLogger decorates every event with the tenant_id carried by the
// context, when there is one.
//
// It has no With/WithGroup methods: use the free functions obs.With and
// obs.WithGroup, which wrap this logger instead of unwrapping it. The removed
// methods returned l.base.With(...), which discarded the decoration and
// silently dropped tenant_id from every derived logger.
type TenantAwareLogger struct {
	base obs.Logger
}

// NewTenantAwareLogger wraps base so that emitted events carry tenant_id.
// A nil base yields a logger backed by obs.Nop().
func NewTenantAwareLogger(base obs.Logger) *TenantAwareLogger {
	if base == nil {
		base = obs.Nop()
	}

	return &TenantAwareLogger{base: base}
}

func (l *TenantAwareLogger) Log(ctx context.Context, level int, msg string, fields ...any) {
	if ctx == nil {
		ctx = context.Background()
	}

	if tenantID := tmcore.GetTenantIDContext(ctx); tenantID != "" {
		fields = append(fields, "tenant_id", tenantID)
	}

	l.base.Log(ctx, level, msg, fields...)
}

func (l *TenantAwareLogger) Enabled(level int) bool {
	return l.base.Enabled(level)
}

func (l *TenantAwareLogger) Sync(ctx context.Context) error {
	return l.base.Sync(ctx)
}
