//go:build unit

package dlq

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

func TestWithLogger_IgnoresTypedNil(t *testing.T) {
	t.Parallel()

	var deref *derefLogger

	var typedNil obs.Logger = deref

	//nolint:staticcheck // the point of the fixture is that == nil is false
	require.False(t, typedNil == nil, "the fixture must be a non-nil interface holding a nil pointer")

	h := &Handler{logger: obs.Nop()}
	WithLogger(typedNil)(h)

	assert.Equal(t, obs.Nop(), h.logger)
	assert.NotPanics(t, func() {
		h.logger.Log(context.Background(), obs.LevelError, "msg")
	})
}
