//go:build unit

package obs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// derefLogger models the common shape of a real adapter: a pointer type whose
// methods dereference the receiver. A nil *derefLogger stored in a Logger is
// not == nil, so an interface nil check alone lets it through and the first
// delegated call panics.
type derefLogger struct{ calls int }

func (l *derefLogger) Log(context.Context, int, string, ...any) { l.calls++ }
func (l *derefLogger) Enabled(int) bool                         { return l.calls >= 0 }
func (l *derefLogger) Sync(context.Context) error               { l.calls++; return nil }

func typedNilLogger() Logger {
	var l *derefLogger

	return l
}

func TestWith_TypedNilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // the point of the fixture is that == nil is false
	require.False(t, typedNilLogger() == nil, "the fixture must be a non-nil interface holding a nil pointer")

	got := With(typedNilLogger(), "k", "v")
	require.NotNil(t, got)

	assert.NotPanics(t, func() {
		got.Log(context.Background(), LevelInfo, "msg")
	})
	assert.False(t, got.Enabled(LevelError))
	assert.NoError(t, got.Sync(context.Background()))
}

func TestWith_TypedNilLoggerWithoutAttributesYieldsNop(t *testing.T) {
	t.Parallel()

	assert.Equal(t, Nop(), With(typedNilLogger()))
}

func TestWithGroup_TypedNilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	got := WithGroup(typedNilLogger(), "group")
	require.NotNil(t, got)

	assert.NotPanics(t, func() {
		got.Log(context.Background(), LevelInfo, "msg", "k", "v")
	})
	assert.False(t, got.Enabled(LevelError))
	assert.NoError(t, got.Sync(context.Background()))
}

func TestWithGroup_TypedNilLoggerWithoutNameYieldsNop(t *testing.T) {
	t.Parallel()

	assert.Equal(t, Nop(), WithGroup(typedNilLogger(), ""))
}
