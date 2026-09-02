//go:build unit

package log

import (
	"context"
	"errors"
	"testing"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogger records what reaches the underlying logger.
type captureLogger struct {
	entries []captured
	enabled bool
	syncErr error
}

type captured struct {
	level int
	msg   string
	kv    []any
}

func (l *captureLogger) Log(_ context.Context, level int, msg string, kv ...any) {
	l.entries = append(l.entries, captured{level: level, msg: msg, kv: kv})
}

func (l *captureLogger) Enabled(int) bool { return l.enabled }

func (l *captureLogger) Sync(context.Context) error { return l.syncErr }

func (l *captureLogger) last() captured { return l.entries[len(l.entries)-1] }

func TestTenantAwareLogger_Log(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ctx    context.Context
		kv     []any
		wantKV []any
	}{
		{
			name:   "appends tenant_id when the context carries one",
			ctx:    core.ContextWithTenantID(context.Background(), "tenant-123"),
			kv:     []any{"key", "value"},
			wantKV: []any{"key", "value", "tenant_id", "tenant-123"},
		},
		{
			name:   "leaves attributes untouched without a tenant",
			ctx:    context.Background(),
			kv:     []any{"key", "value"},
			wantKV: []any{"key", "value"},
		},
		{
			name:   "adds tenant_id even with no attributes",
			ctx:    core.ContextWithTenantID(context.Background(), "tenant-123"),
			wantKV: []any{"tenant_id", "tenant-123"},
		},
		{
			name:   "nil context is treated as an empty context",
			ctx:    nil,
			kv:     []any{"key", "value"},
			wantKV: []any{"key", "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := &captureLogger{}
			logger := NewTenantAwareLogger(base)

			logger.Log(tt.ctx, obs.LevelInfo, "test message", tt.kv...)

			require.Len(t, base.entries, 1)
			assert.Equal(t, obs.LevelInfo, base.last().level)
			assert.Equal(t, "test message", base.last().msg)
			assert.Equal(t, tt.wantKV, base.last().kv)
		})
	}
}

func TestTenantAwareLogger_SatisfiesTheObsContract(t *testing.T) {
	t.Parallel()

	var _ obs.Logger = NewTenantAwareLogger(obs.Nop())
}

func TestNewTenantAwareLogger_NilBaseIsSafe(t *testing.T) {
	t.Parallel()

	logger := NewTenantAwareLogger(nil)
	require.NotNil(t, logger)

	assert.NotPanics(t, func() {
		logger.Log(context.Background(), obs.LevelError, "dropped")
	})
	assert.False(t, logger.Enabled(obs.LevelError))
	assert.NoError(t, logger.Sync(context.Background()))
}

func TestTenantAwareLogger_DelegatesEnabledAndSync(t *testing.T) {
	t.Parallel()

	base := &captureLogger{enabled: true, syncErr: errors.New("flush failed")}
	logger := NewTenantAwareLogger(base)

	assert.True(t, logger.Enabled(obs.LevelDebug))
	assert.ErrorIs(t, logger.Sync(context.Background()), base.syncErr)
}

// Regression: the removed (TenantAwareLogger).With / .WithGroup methods
// returned l.base.With(...), which handed back the UNDECORATED base logger and
// silently dropped tenant_id from every derived logger. The free functions
// obs.With / obs.WithGroup wrap the tenant-aware logger instead, so the
// decoration survives.
func TestObsWithAndWithGroup_KeepTenantIDOnDerivedLoggers(t *testing.T) {
	t.Parallel()

	ctx := core.ContextWithTenantID(context.Background(), "tenant-123")

	t.Run("With", func(t *testing.T) {
		t.Parallel()

		base := &captureLogger{}

		obs.With(NewTenantAwareLogger(base), "component", "consumer").
			Log(ctx, obs.LevelInfo, "derived")

		assert.Equal(
			t,
			[]any{"component", "consumer", "tenant_id", "tenant-123"},
			base.last().kv,
		)
	})

	t.Run("WithGroup", func(t *testing.T) {
		t.Parallel()

		base := &captureLogger{}

		obs.WithGroup(NewTenantAwareLogger(base), "db").
			Log(ctx, obs.LevelInfo, "derived", "table", "accounts")

		assert.Equal(
			t,
			[]any{"db.table", "accounts", "tenant_id", "tenant-123"},
			base.last().kv,
		)
	})
}
