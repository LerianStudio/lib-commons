//go:build unit

package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestHandleListPendingError_NilLogger covers the threshold branch of
// handleListPendingError on a Dispatcher whose logger field was never set.
// NewDispatcher substitutes obs.Nop(), so this shape is only reachable from a
// struct literal inside the package, but the branch used to call Log on a nil
// interface and panic.
func TestHandleListPendingError_NilLogger(t *testing.T) {
	t.Parallel()

	cfg := DefaultDispatcherConfig()
	cfg.ListPendingFailureThreshold = 1

	dispatcher := &Dispatcher{
		cfg:                      cfg,
		listPendingFailureCounts: make(map[string]int),
	}

	_, span := noop.NewTracerProvider().Tracer("test").Start(context.Background(), "test")

	assert.NotPanics(t, func() {
		dispatcher.handleListPendingError(context.Background(), span, "tenant-a", errors.New("list failed"))
	})

	assert.Equal(t, 1, dispatcher.listPendingFailureCounts["tenant-a"])
}

// TestResolvedLogger_NeverNil pins the substitution itself.
func TestResolvedLogger_NeverNil(t *testing.T) {
	t.Parallel()

	assert.NotNil(t, (&Dispatcher{}).resolvedLogger())
}
