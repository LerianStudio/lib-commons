//go:build unit

package http

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPing_NilContext(t *testing.T) {
	t.Parallel()

	err := Ping(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestExtractTokenFromHeader_NilContext(t *testing.T) {
	t.Parallel()

	assert.Empty(t, ExtractTokenFromHeader(nil))
}

func TestFiberErrorHandler_NilContext(t *testing.T) {
	t.Parallel()

	handlerErr := errors.New("boom")
	err := FiberErrorHandler(nil, handlerErr)
	require.Error(t, err)
	assert.ErrorIs(t, err, handlerErr)
}

func TestFiberErrorHandler_NilContextAndNilError(t *testing.T) {
	t.Parallel()

	err := FiberErrorHandler(nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestWelcome_NilContext(t *testing.T) {
	t.Parallel()

	err := Welcome("svc", "desc")(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestFile_NilContext(t *testing.T) {
	t.Parallel()

	err := File("/tmp/ignored")(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestHealthWithDependencies_NilContext(t *testing.T) {
	t.Parallel()

	err := HealthWithDependencies()(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestEndTracingSpans_NilContext(t *testing.T) {
	t.Parallel()

	middleware := &TelemetryMiddleware{}
	err := middleware.EndTracingSpans(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}
