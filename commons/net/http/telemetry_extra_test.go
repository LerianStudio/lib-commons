//go:build unit

package http

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// TestEndTracingSpansInterceptor_ReturnsInterceptor covers the function.
func TestEndTracingSpansInterceptor_ReturnsInterceptor(t *testing.T) {
	t.Parallel()

	tm := &TelemetryMiddleware{}
	interceptor := tm.EndTracingSpansInterceptor()
	require.NotNil(t, interceptor)
}

// TestEndTracingSpansInterceptor_InvokesHandler covers the handler invocation.
func TestEndTracingSpansInterceptor_InvokesHandler(t *testing.T) {
	t.Parallel()

	tm := &TelemetryMiddleware{}
	interceptor := tm.EndTracingSpansInterceptor()

	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return "response", nil
	}

	resp, err := interceptor(
		context.Background(),
		"request",
		&grpc.UnaryServerInfo{FullMethod: "/test"},
		handler,
	)

	require.NoError(t, err)
	assert.Equal(t, "response", resp)
	assert.True(t, handlerCalled)
}
