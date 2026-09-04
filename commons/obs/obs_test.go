//go:build unit

package obs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingLogger captures every event so tests can assert on the kv that
// actually reached the sink.
type recordingLogger struct {
	entries  []entry
	minLevel int
	syncErr  error
}

type entry struct {
	level int
	msg   string
	kv    []any
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{minLevel: obs.LevelDebug}
}

func (l *recordingLogger) Log(_ context.Context, level int, msg string, kv ...any) {
	l.entries = append(l.entries, entry{level: level, msg: msg, kv: kv})
}

func (l *recordingLogger) Enabled(level int) bool { return level <= l.minLevel }

func (l *recordingLogger) Sync(context.Context) error { return l.syncErr }

func (l *recordingLogger) last() entry {
	return l.entries[len(l.entries)-1]
}

func TestLevelConstants_MatchLibObservabilityScale(t *testing.T) {
	t.Parallel()

	// Lower value == more severe. This ordering is load-bearing: adapters map
	// these onto lib-observability's log.Level by numeric identity.
	assert.Equal(t, 0, obs.LevelError)
	assert.Equal(t, 1, obs.LevelWarn)
	assert.Equal(t, 2, obs.LevelInfo)
	assert.Equal(t, 3, obs.LevelDebug)
}

func TestNop_DiscardsEverythingAndNeverReportsEnabled(t *testing.T) {
	t.Parallel()

	logger := obs.Nop()
	require.NotNil(t, logger)

	assert.NotPanics(t, func() {
		logger.Log(context.Background(), obs.LevelError, "dropped", "key", "value")
		logger.Log(nil, obs.LevelError, "dropped with nil ctx") //nolint:staticcheck // nil ctx is the case under test
	})

	for _, level := range []int{obs.LevelError, obs.LevelWarn, obs.LevelInfo, obs.LevelDebug} {
		assert.False(t, logger.Enabled(level))
	}

	assert.NoError(t, logger.Sync(context.Background()))
}

func TestNormalizeKV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kv   []any
		want []any
	}{
		{name: "empty yields nil", kv: nil, want: nil},
		{name: "well formed pair is preserved", kv: []any{"key", 1}, want: []any{"key", 1}},
		{
			name: "odd length pairs the trailing key with nil",
			kv:   []any{"key", 1, "orphan"},
			want: []any{"key", 1, "orphan", nil},
		},
		{
			name: "non string key becomes a positional placeholder",
			kv:   []any{42, "value"},
			want: []any{"arg_0", "value"},
		},
		{
			name: "empty string key becomes a positional placeholder",
			kv:   []any{"a", 1, "", 2},
			want: []any{"a", 1, "arg_2", 2},
		},
		{
			name: "nil key becomes a positional placeholder",
			kv:   []any{nil, "value"},
			want: []any{"arg_0", "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, obs.NormalizeKV(tt.kv...))
		})
	}
}

func TestWith_AttachesBoundAttributesToEveryEvent(t *testing.T) {
	t.Parallel()

	base := newRecordingLogger()
	logger := obs.With(base, "tenant_id", "acme")

	logger.Log(context.Background(), obs.LevelInfo, "first", "op", "create")
	logger.Log(context.Background(), obs.LevelWarn, "second")

	require.Len(t, base.entries, 2)
	assert.Equal(t, []any{"tenant_id", "acme", "op", "create"}, base.entries[0].kv)
	assert.Equal(t, []any{"tenant_id", "acme"}, base.entries[1].kv)
}

func TestWith_NilLoggerYieldsNop(t *testing.T) {
	t.Parallel()

	logger := obs.With(nil, "key", "value")
	require.NotNil(t, logger)

	assert.NotPanics(t, func() {
		logger.Log(context.Background(), obs.LevelError, "dropped")
	})
	assert.False(t, logger.Enabled(obs.LevelError))
	assert.NoError(t, logger.Sync(context.Background()))
}

func TestWith_EmptyAttributesReturnsTheSameLogger(t *testing.T) {
	t.Parallel()

	base := newRecordingLogger()

	assert.Same(t, base, obs.With(base))
}

func TestWith_NormalizesMalformedAttributes(t *testing.T) {
	t.Parallel()

	base := newRecordingLogger()
	logger := obs.With(base, "orphan")

	logger.Log(context.Background(), obs.LevelInfo, "msg", 7, "value")

	assert.Equal(t, []any{"orphan", nil, "arg_0", "value"}, base.last().kv)
}

func TestWithGroup_NamespacesCallSiteKeysOnly(t *testing.T) {
	t.Parallel()

	base := newRecordingLogger()

	// Attributes bound before the group are not namespaced; those bound after
	// it are. This mirrors slog/zap group semantics.
	logger := obs.WithGroup(obs.With(base, "bound_before", 1), "db")
	logger = obs.With(logger, "bound_after", 2)

	logger.Log(context.Background(), obs.LevelInfo, "query", "table", "accounts")

	assert.Equal(
		t,
		[]any{"bound_before", 1, "db.bound_after", 2, "db.table", "accounts"},
		base.last().kv,
	)
}

func TestWithGroup_EmptyNameReturnsTheSameLogger(t *testing.T) {
	t.Parallel()

	base := newRecordingLogger()

	assert.Same(t, base, obs.WithGroup(base, ""))
}

func TestWithGroup_NilLoggerYieldsNop(t *testing.T) {
	t.Parallel()

	logger := obs.WithGroup(nil, "db")
	require.NotNil(t, logger)

	assert.NotPanics(t, func() {
		logger.Log(context.Background(), obs.LevelInfo, "dropped")
	})
}

// decoratingLogger stands in for a wrapper such as TenantAwareLogger: it adds
// an attribute of its own on every event.
type decoratingLogger struct {
	base *recordingLogger
}

func (l decoratingLogger) Log(ctx context.Context, level int, msg string, kv ...any) {
	l.base.Log(ctx, level, msg, append([]any{"tenant_id", "acme"}, kv...)...)
}

func (l decoratingLogger) Enabled(level int) bool { return l.base.Enabled(level) }

func (l decoratingLogger) Sync(ctx context.Context) error { return l.base.Sync(ctx) }

// Regression: the previous (TenantAwareLogger).With returned l.base.With(...),
// which discarded the wrapper and silently dropped tenant_id from the derived
// logger. The free functions delegate instead of unwrapping, so the decoration
// survives.
func TestWithAndWithGroup_PreserveTheDecoratingWrapper(t *testing.T) {
	t.Parallel()

	base := newRecordingLogger()
	decorated := decoratingLogger{base: base}

	obs.With(decorated, "op", "create").Log(context.Background(), obs.LevelInfo, "with")
	obs.WithGroup(decorated, "db").Log(context.Background(), obs.LevelInfo, "group", "table", "accounts")

	require.Len(t, base.entries, 2)
	assert.Equal(t, []any{"tenant_id", "acme", "op", "create"}, base.entries[0].kv)
	assert.Equal(t, []any{"tenant_id", "acme", "db.table", "accounts"}, base.entries[1].kv)
}

func TestWith_DelegatesEnabledAndSync(t *testing.T) {
	t.Parallel()

	base := newRecordingLogger()
	base.minLevel = obs.LevelWarn
	base.syncErr = errors.New("flush failed")

	logger := obs.WithGroup(obs.With(base, "key", "value"), "db")

	assert.True(t, logger.Enabled(obs.LevelError))
	assert.True(t, logger.Enabled(obs.LevelWarn))
	assert.False(t, logger.Enabled(obs.LevelInfo))
	assert.ErrorIs(t, logger.Sync(context.Background()), base.syncErr)
}

func TestWith_NilContextIsReplaced(t *testing.T) {
	t.Parallel()

	base := newRecordingLogger()
	logger := obs.With(base, "key", "value")

	assert.NotPanics(t, func() {
		logger.Log(nil, obs.LevelInfo, "msg") //nolint:staticcheck // nil ctx is the case under test
	})
	assert.Len(t, base.entries, 1)
}
