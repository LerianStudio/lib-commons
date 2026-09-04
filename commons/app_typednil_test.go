//go:build unit

package commons

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// derefLogger dereferences its receiver, so a nil *derefLogger held in an
// obs.Logger panics on first use while still comparing != nil.
type derefLogger struct{ calls int }

func (l *derefLogger) Log(context.Context, int, string, ...any) { l.calls++ }
func (l *derefLogger) Enabled(int) bool                         { return l.calls >= 0 }
func (l *derefLogger) Sync(context.Context) error               { l.calls++; return nil }

func typedNilLogger() obs.Logger {
	var l *derefLogger

	return l
}

func TestWithLogger_IgnoresTypedNil(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // the point of the fixture is that == nil is false
	require.False(t, typedNilLogger() == nil, "the fixture must be a non-nil interface holding a nil pointer")

	launcher := NewLauncher(WithLogger(typedNilLogger()))

	assert.ErrorIs(t, launcher.RunWithError(), ErrLoggerNil)
}

func TestRunWithError_RejectsTypedNilAssignedDirectly(t *testing.T) {
	t.Parallel()

	launcher := NewLauncher()
	launcher.Logger = typedNilLogger()

	assert.ErrorIs(t, launcher.RunWithError(), ErrLoggerNil)
}

func TestRun_DoesNotPanicOnTypedNilLogger(t *testing.T) {
	t.Parallel()

	launcher := NewLauncher()
	launcher.Logger = typedNilLogger()

	assert.NotPanics(t, launcher.Run)
}
