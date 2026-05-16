//go:build unit

package logcompat

import (
	"context"
	"testing"

	liblog "github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNew covers the New constructor.
func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	l := New(nil)
	require.NotNil(t, l)
}

func TestNew_WithLogger(t *testing.T) {
	t.Parallel()

	logger := liblog.NewNop()
	l := New(logger)
	require.NotNil(t, l)
	assert.NotNil(t, l.base)
}

// TestLogger_Info covers the Info logging methods.
func TestLogger_Info(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.Info("test message")
	})
}

func TestLogger_Infof(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.Infof("test %s", "message")
	})
}

func TestLogger_InfoCtx(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.InfoCtx(context.Background(), "test message")
	})
}

func TestLogger_InfofCtx(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.InfofCtx(context.Background(), "test %s", "message")
	})
}

// TestLogger_Warn covers the Warn logging methods.
func TestLogger_Warn(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.Warn("test warning")
	})
}

func TestLogger_Warnf(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.Warnf("test %s", "warning")
	})
}

func TestLogger_WarnCtx(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.WarnCtx(context.Background(), "test warning")
	})
}

func TestLogger_WarnfCtx(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.WarnfCtx(context.Background(), "test %s", "warning")
	})
}

// TestLogger_Error covers the Error logging methods.
func TestLogger_Error(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.Error("test error")
	})
}

func TestLogger_Errorf(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.Errorf("test %s", "error")
	})
}

func TestLogger_ErrorCtx(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.ErrorCtx(context.Background(), "test error")
	})
}

// TestLogger_Base covers the Base method.
func TestLogger_Base(t *testing.T) {
	t.Parallel()

	logger := liblog.NewNop()
	l := New(logger)
	result := l.Base()
	require.NotNil(t, result)
}

// TestLogger_NilReceiver covers nil receiver safety.
func TestLogger_Info_NilReceiver(t *testing.T) {
	t.Parallel()

	var l *Logger
	assert.NotPanics(t, func() {
		l.Info("nil receiver")
	})
}

// TestWithFields covers the WithFields method.
func TestWithFields_BasicCall(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	child := l.WithFields("key", "value")
	require.NotNil(t, child)
}

func TestWithFields_NilReceiver(t *testing.T) {
	t.Parallel()

	var l *Logger
	child := l.WithFields("key", "value")
	require.NotNil(t, child)
}

// TestLogger_Debug covers the Debug method.
func TestLogger_Debug(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.Debug("debug message")
	})
}

// TestLogger_Debugf covers the Debugf method.
func TestLogger_Debugf(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.Debugf("debug %s", "message")
	})
}

// TestLogger_ErrorfCtx covers the ErrorfCtx method.
func TestLogger_ErrorfCtx(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	assert.NotPanics(t, func() {
		l.ErrorfCtx(context.Background(), "error %s", "message")
	})
}

// TestLogger_Sync covers the Sync method.
func TestLogger_Sync(t *testing.T) {
	t.Parallel()

	l := New(liblog.NewNop())
	err := l.Sync()
	assert.NoError(t, err)
}

// TestLogger_Sync_NilReceiver covers the nil receiver guard.
func TestLogger_Sync_NilReceiver(t *testing.T) {
	t.Parallel()

	var l *Logger
	err := l.Sync()
	assert.NoError(t, err)
}

// recordingLogCompat is a logger that records messages and enables all levels.
type recordingLogCompat struct {
	messages []string
}

func (r *recordingLogCompat) Log(_ context.Context, _ liblog.Level, msg string, _ ...liblog.Field) {
	r.messages = append(r.messages, msg)
}
func (r *recordingLogCompat) With(_ ...liblog.Field) liblog.Logger { return r }
func (r *recordingLogCompat) WithGroup(_ string) liblog.Logger     { return r }
func (r *recordingLogCompat) Enabled(_ liblog.Level) bool          { return true }
func (r *recordingLogCompat) Sync(_ context.Context) error         { return nil }

// TestLogger_InfoWithEnabledLogger covers the path where logger is enabled.
func TestLogger_InfoWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.Info("test info message")
	// Verify that the message was actually logged (log() was called)
	assert.Greater(t, len(recorder.messages), 0, "log should have been called")
}

// TestLogger_InfoCtxWithEnabledLogger covers the InfoCtx path with enabled logger.
func TestLogger_InfoCtxWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.InfoCtx(context.Background(), "test ctx message")
}

// TestLogger_WarnWithEnabledLogger covers the Warn path.
func TestLogger_WarnWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.Warn("warning")
}

// TestLogger_ErrorWithEnabledLogger covers the Error path.
func TestLogger_ErrorWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.Error("error")
}

// TestLogger_DebugWithEnabledLogger covers the Debug path.
func TestLogger_DebugWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.Debug("debug")
}

// TestLogger_InfofWithEnabledLogger covers the Infof path.
func TestLogger_InfofWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.Infof("info %s", "fmt")
}

// TestLogger_WarnfWithEnabledLogger covers the Warnf path.
func TestLogger_WarnfWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.Warnf("warn %s", "fmt")
}

// TestLogger_ErrorfWithEnabledLogger covers the Errorf path.
func TestLogger_ErrorfWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.Errorf("error %s", "fmt")
}

// TestLogger_DebugfWithEnabledLogger covers the Debugf path.
func TestLogger_DebugfWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.Debugf("debug %s", "fmt")
}

// TestLogger_WarnCtxWithEnabledLogger covers the WarnCtx path.
func TestLogger_WarnCtxWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.WarnCtx(context.Background(), "ctx warn")
}

// TestLogger_ErrorCtxWithEnabledLogger covers the ErrorCtx path.
func TestLogger_ErrorCtxWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.ErrorCtx(context.Background(), "ctx error")
}

// TestLogger_WarnfCtxWithEnabledLogger covers the WarnfCtx path.
func TestLogger_WarnfCtxWithEnabledLogger(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	l.WarnfCtx(context.Background(), "ctx warn %s", "fmt")
}

// TestLogger_log_WithNilCtx covers the nil ctx branch inside log().
func TestLogger_log_NilCtxBranch(t *testing.T) {
	t.Parallel()

	recorder := &recordingLogCompat{}
	l := New(recorder)
	// Call log directly with nil context - should handle gracefully
	l.log(nil, liblog.LevelInfo, "nil ctx test")
}
