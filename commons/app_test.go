//go:build unit

package commons

import (
	"errors"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubApp is a minimal App implementation for testing.
type stubApp struct {
	err error
}

func (s *stubApp) Run(_ *Launcher) error {
	return s.err
}

func TestNewLauncher(t *testing.T) {
	t.Parallel()

	l := NewLauncher()
	require.NotNil(t, l)
	assert.True(t, l.Verbose)
	assert.NotNil(t, l.apps)
}

func TestLauncher_Add(t *testing.T) {
	t.Parallel()

	t.Run("nil_receiver", func(t *testing.T) {
		t.Parallel()

		var l *Launcher
		err := l.Add("app", &stubApp{})
		assert.ErrorIs(t, err, ErrNilLauncher)
	})

	t.Run("nil_app", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher()
		err := l.Add("app", nil)
		assert.ErrorIs(t, err, ErrNilApp)
	})

	t.Run("empty_name", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher()
		err := l.Add("", &stubApp{})
		assert.ErrorIs(t, err, ErrEmptyApp)
	})

	t.Run("whitespace_name", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher()
		err := l.Add("  ", &stubApp{})
		assert.ErrorIs(t, err, ErrEmptyApp)
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher()
		err := l.Add("myapp", &stubApp{})
		assert.NoError(t, err)
	})
}

func TestRunAppOption(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher()
		opt := RunApp("myapp", &stubApp{})
		opt(l)
		assert.Empty(t, l.configErrors)
	})

	t.Run("failure_nil_app", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher(WithLogger(&log.NopLogger{}))
		opt := RunApp("myapp", nil)
		opt(l)
		assert.NotEmpty(t, l.configErrors)
	})
}

func TestWithLoggerOption_NilLauncher(t *testing.T) {
	t.Parallel()

	// WithLogger option applied to nil launcher must not panic.
	opt := WithLogger(&log.NopLogger{})
	assert.NotPanics(t, func() { opt(nil) })
}

func TestRunAppOption_NilLauncher(t *testing.T) {
	t.Parallel()

	// RunApp option applied to nil launcher must not panic.
	opt := RunApp("myapp", &stubApp{})
	assert.NotPanics(t, func() { opt(nil) })
}

func TestWithLoggerOption(t *testing.T) {
	t.Parallel()

	logger := &log.NopLogger{}
	l := NewLauncher(WithLogger(logger))
	assert.Equal(t, logger, l.Logger)
}

func TestRunWithError(t *testing.T) {
	t.Parallel()

	t.Run("nil_logger_returns_ErrLoggerNil", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher()
		err := l.RunWithError()
		assert.ErrorIs(t, err, ErrLoggerNil)
	})

	t.Run("config_errors_surface", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher(WithLogger(&log.NopLogger{}))
		l.configErrors = append(l.configErrors, errors.New("bad config"))

		err := l.RunWithError()
		assert.ErrorIs(t, err, ErrConfigFailed)
	})

	t.Run("no_apps_finishes", func(t *testing.T) {
		t.Parallel()

		l := NewLauncher(WithLogger(&log.NopLogger{}))
		err := l.RunWithError()
		assert.NoError(t, err)
	})

	t.Run("app_run_error_is_handled_gracefully", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("boom")

		l := NewLauncher(WithLogger(&log.NopLogger{}))
		require.NoError(t, l.Add("failing", &stubApp{err: sentinel}))

		// RunWithError launches apps in goroutines; app errors are logged
		// but not propagated, so the launcher completes without error.
		err := l.RunWithError()
		assert.NoError(t, err)
	})
}
