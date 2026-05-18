//go:build unit

package http

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons"
	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type grpcRequestWithID struct {
	requestID string
}

func (r grpcRequestWithID) GetRequestId() string {
	return r.requestID
}

type grpcPointerRequestWithID struct {
	requestID string
}

func (r *grpcPointerRequestWithID) GetRequestId() string {
	return r.requestID
}

type capturedLogEntry struct {
	level  libLog.Level
	msg    string
	fields []libLog.Field
}

type capturedLogState struct {
	mu      sync.Mutex
	entries []capturedLogEntry
}

type captureLogger struct {
	state *capturedLogState
	bound []libLog.Field
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{state: &capturedLogState{}}
}

func (l *captureLogger) Log(_ context.Context, level libLog.Level, msg string, fields ...libLog.Field) {
	merged := make([]libLog.Field, 0, len(l.bound)+len(fields))
	merged = append(merged, l.bound...)
	merged = append(merged, fields...)

	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	l.state.entries = append(l.state.entries, capturedLogEntry{level: level, msg: msg, fields: merged})
}

func (l *captureLogger) With(fields ...libLog.Field) libLog.Logger {
	bound := make([]libLog.Field, 0, len(l.bound)+len(fields))
	bound = append(bound, l.bound...)
	bound = append(bound, fields...)

	return &captureLogger{state: l.state, bound: bound}
}

func (l *captureLogger) WithGroup(string) libLog.Logger { return l }
func (l *captureLogger) Enabled(libLog.Level) bool      { return true }
func (l *captureLogger) Sync(context.Context) error     { return nil }

func (l *captureLogger) entries() []capturedLogEntry {
	l.state.mu.Lock()
	defer l.state.mu.Unlock()

	entries := make([]capturedLogEntry, len(l.state.entries))
	copy(entries, l.state.entries)
	return entries
}

func TestWithGrpcLogging_BodyRequestIDOverridesMetadata(t *testing.T) {
	t.Parallel()

	logger := newCaptureLogger()
	interceptor := WithGrpcLogging(WithCustomLogger(logger))
	bodyID := uuid.NewString()
	metadataID := uuid.NewString()

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(cn.MetadataID, metadataID))

	var seenRequestID string
	resp, err := interceptor(ctx, grpcRequestWithID{requestID: bodyID}, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
		_, _, seenRequestID, _ = commons.NewTrackingFromContext(ctx)
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.Equal(t, bodyID, seenRequestID)

	entries := logger.entries()
	require.Len(t, entries, 2)
	assert.Equal(t, libLog.LevelDebug, entries[0].level)
	assert.Contains(t, entries[0].msg, "Overriding correlation id")
	assert.Equal(t, libLog.LevelInfo, entries[1].level)
	assert.Equal(t, "gRPC request finished", entries[1].msg)
	assert.Contains(t, entries[1].fields, libLog.String(cn.HeaderID, bodyID))
	assert.Contains(t, entries[1].fields, libLog.String("message_prefix", bodyID+cn.LoggerDefaultSeparator))
}

func TestWithGrpcLogging_InvalidBodyRequestIDFallsBackToMetadata(t *testing.T) {
	t.Parallel()

	logger := newCaptureLogger()
	interceptor := WithGrpcLogging(WithCustomLogger(logger))
	metadataID := uuid.NewString()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(cn.MetadataID, metadataID))

	var seenRequestID string
	_, err := interceptor(ctx, grpcRequestWithID{requestID: "not-a-uuid"}, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
		_, _, seenRequestID, _ = commons.NewTrackingFromContext(ctx)
		return nil, nil
	})
	require.NoError(t, err)
	assert.Equal(t, metadataID, seenRequestID)

	entries := logger.entries()
	require.Len(t, entries, 1)
	assert.Equal(t, libLog.LevelInfo, entries[0].level)
	assert.Contains(t, entries[0].fields, libLog.String(cn.HeaderID, metadataID))
}

func TestWithGrpcLogging_GeneratesRequestIDWhenMissing(t *testing.T) {
	t.Parallel()

	interceptor := WithGrpcLogging()

	var seenRequestID string
	_, err := interceptor(context.Background(), struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
		_, _, seenRequestID, _ = commons.NewTrackingFromContext(ctx)
		return nil, nil
	})
	require.NoError(t, err)
	assert.NotEmpty(t, seenRequestID)
	_, parseErr := uuid.Parse(seenRequestID)
	require.NoError(t, parseErr)
}

func TestGetValidBodyRequestID_TypedNilRequestReturnsFalse(t *testing.T) {
	t.Parallel()

	var req *grpcPointerRequestWithID

	assert.NotPanics(t, func() {
		requestID, ok := getValidBodyRequestID(req)
		assert.False(t, ok)
		assert.Empty(t, requestID)
	})
}

func TestWithGrpcLogging_TypedNilBodyRequestIDFallsBackToMetadata(t *testing.T) {
	t.Parallel()

	logger := newCaptureLogger()
	interceptor := WithGrpcLogging(WithCustomLogger(logger))
	metadataID := uuid.NewString()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(cn.MetadataID, metadataID))

	var req *grpcPointerRequestWithID
	var seenRequestID string

	assert.NotPanics(t, func() {
		_, err := interceptor(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
			_, _, seenRequestID, _ = commons.NewTrackingFromContext(ctx)
			return nil, nil
		})
		require.NoError(t, err)
	})

	assert.Equal(t, metadataID, seenRequestID)
}

func TestWithGrpcLogging_LogsHandlerErrors(t *testing.T) {
	t.Parallel()

	logger := newCaptureLogger()
	interceptor := WithGrpcLogging(WithCustomLogger(logger))
	handlerErr := errors.New("boom")

	_, err := interceptor(context.Background(), struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
		return nil, handlerErr
	})
	require.ErrorIs(t, err, handlerErr)

	entries := logger.entries()
	require.Len(t, entries, 1)
	assert.Equal(t, libLog.LevelInfo, entries[0].level)
	assert.Contains(t, entries[0].fields, libLog.Err(handlerErr))
}

func TestWithGrpcLogging_NilContextDoesNotPanic(t *testing.T) {
	t.Parallel()

	interceptor := WithGrpcLogging()

	assert.NotPanics(t, func() {
		var seenRequestID string
		_, err := interceptor(nil, struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
			_, _, seenRequestID, _ = commons.NewTrackingFromContext(ctx)
			return nil, nil
		})
		require.NoError(t, err)
		assert.NotEmpty(t, seenRequestID)
	})
}

func TestWithGrpcLogging_NilInfoUsesUnknownMethod(t *testing.T) {
	t.Parallel()

	logger := newCaptureLogger()
	interceptor := WithGrpcLogging(WithCustomLogger(logger))

	_, err := interceptor(context.Background(), struct{}{}, nil, func(ctx context.Context, req any) (any, error) {
		return nil, nil
	})
	require.NoError(t, err)

	entries := logger.entries()
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].fields, libLog.String("method", "unknown"))
}

func TestWithTelemetryInterceptor_NilContextDoesNotPanic(t *testing.T) {
	t.Parallel()

	tp, _ := setupTestTracer()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	telemetry := &opentelemetry.Telemetry{
		TelemetryConfig: opentelemetry.TelemetryConfig{LibraryName: "test-library", EnableTelemetry: true},
		TracerProvider:  tp,
	}
	interceptor := NewTelemetryMiddleware(telemetry).WithTelemetryInterceptor(telemetry)

	assert.NotPanics(t, func() {
		_, err := interceptor(nil, struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
			_, _, requestID, _ := commons.NewTrackingFromContext(ctx)
			assert.NotEmpty(t, requestID)
			return nil, nil
		})
		require.NoError(t, err)
	})
}

func TestWithTelemetryInterceptor_TypedNilBodyRequestIDFallsBackToMetadata(t *testing.T) {
	t.Parallel()

	tp, _ := setupTestTracer()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	telemetry := &opentelemetry.Telemetry{
		TelemetryConfig: opentelemetry.TelemetryConfig{LibraryName: "test-library", EnableTelemetry: true},
		TracerProvider:  tp,
	}
	interceptor := NewTelemetryMiddleware(telemetry).WithTelemetryInterceptor(telemetry)
	metadataID := uuid.NewString()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(cn.MetadataID, metadataID))

	var req *grpcPointerRequestWithID

	assert.NotPanics(t, func() {
		_, err := interceptor(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
			_, _, requestID, _ := commons.NewTrackingFromContext(ctx)
			assert.Equal(t, metadataID, requestID)
			return nil, nil
		})
		require.NoError(t, err)
	})
}

func TestWithGrpcLoggingAndTelemetryInterceptor_ShareResolvedRequestID(t *testing.T) {
	t.Parallel()

	tp, _ := setupTestTracer()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	telemetry := &opentelemetry.Telemetry{
		TelemetryConfig: opentelemetry.TelemetryConfig{LibraryName: "test-library", EnableTelemetry: true},
		TracerProvider:  tp,
	}
	telemetryInterceptor := NewTelemetryMiddleware(telemetry).WithTelemetryInterceptor(telemetry)
	logger := newCaptureLogger()
	loggingInterceptor := WithGrpcLogging(WithCustomLogger(logger))
	bodyID := uuid.NewString()
	metadataID := uuid.NewString()

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(cn.MetadataID, metadataID, "user-agent", "midaz/1.0.0 LerianStudio"))

	var seenRequestID string
	var spanContext trace.SpanContext
	resp, err := loggingInterceptor(ctx, grpcRequestWithID{requestID: bodyID}, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
		return telemetryInterceptor(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/svc.Method"}, func(ctx context.Context, req any) (any, error) {
			_, _, seenRequestID, _ = commons.NewTrackingFromContext(ctx)
			spanContext = trace.SpanContextFromContext(ctx)
			return "ok", nil
		})
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.Equal(t, bodyID, seenRequestID)
	assert.True(t, spanContext.IsValid())

	entries := logger.entries()
	require.NotEmpty(t, entries)
	assert.Contains(t, entries[len(entries)-1].fields, libLog.String(cn.HeaderID, bodyID))
}
