//go:build unit

package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func newTestTracerProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	return provider, recorder
}

func TestErrPanic(t *testing.T) {
	t.Parallel()

	assert.NotNil(t, ErrPanic)
	assert.Equal(t, "panic", ErrPanic.Error())
}

func TestPanicSpanEventName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "panic.recovered", PanicSpanEventName)
}

func TestRecordPanicToSpan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		panicValue    any
		stack         []byte
		goroutineName string
		wantEvent     bool
		wantStatus    codes.Code
		wantMessage   string
	}{
		{
			name:          "string panic value",
			panicValue:    "something went wrong",
			stack:         []byte("goroutine 1 [running]:\nmain.main()"),
			goroutineName: "worker-1",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in worker-1",
		},
		{
			name:          "error panic value",
			panicValue:    assert.AnError,
			stack:         []byte("stack trace here"),
			goroutineName: "handler",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in handler",
		},
		{
			name:          "integer panic value",
			panicValue:    42,
			stack:         []byte(""),
			goroutineName: "processor",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in processor",
		},
		{
			name:          "nil panic value",
			panicValue:    nil,
			stack:         []byte("some stack"),
			goroutineName: "main",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in main",
		},
		{
			name:          "empty goroutine name",
			panicValue:    "panic!",
			stack:         []byte("trace"),
			goroutineName: "",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in ",
		},
		{
			name:          "empty stack trace",
			panicValue:    "error",
			stack:         nil,
			goroutineName: "worker",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in worker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, recorder := newTestTracerProvider(t)
			tracer := provider.Tracer("test")
			ctx, span := tracer.Start(context.Background(), "test-span")

			RecordPanicToSpan(ctx, tt.panicValue, tt.stack, tt.goroutineName)
			span.End()

			spans := recorder.Ended()
			require.Len(t, spans, 1)

			recordedSpan := spans[0]

			if tt.wantEvent {
				require.NotEmpty(t, recordedSpan.Events(), "expected panic event to be recorded")

				var foundPanicEvent bool

				for _, event := range recordedSpan.Events() {
					if event.Name == PanicSpanEventName {
						foundPanicEvent = true

						attrMap := make(map[string]string)
						for _, attr := range event.Attributes {
							attrMap[string(attr.Key)] = attr.Value.AsString()
						}

						assert.Contains(t, attrMap, "panic.value")
						assert.Contains(t, attrMap, "panic.stack")
						assert.Contains(t, attrMap, "panic.goroutine_name")
						assert.Equal(t, tt.goroutineName, attrMap["panic.goroutine_name"])
						assert.NotContains(
							t,
							attrMap,
							"panic.component",
							"component should not be present for RecordPanicToSpan",
						)
					}
				}

				assert.True(t, foundPanicEvent, "panic.recovered event not found")
			}

			assert.Equal(t, tt.wantStatus, recordedSpan.Status().Code)
			assert.Equal(t, tt.wantMessage, recordedSpan.Status().Description)
		})
	}
}

func TestRecordPanicToSpanWithComponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		panicValue    any
		stack         []byte
		component     string
		goroutineName string
		wantEvent     bool
		wantStatus    codes.Code
		wantMessage   string
	}{
		{
			name:          "with component",
			panicValue:    "panic error",
			stack:         []byte("stack trace"),
			component:     "transaction",
			goroutineName: "CreateTransaction",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in transaction/CreateTransaction",
		},
		{
			name:          "empty component",
			panicValue:    "error",
			stack:         []byte("trace"),
			component:     "",
			goroutineName: "handler",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in handler",
		},
		{
			name:          "empty goroutine name with component",
			panicValue:    "error",
			stack:         []byte("trace"),
			component:     "auth",
			goroutineName: "",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in auth/",
		},
		{
			name:          "both empty",
			panicValue:    "panic",
			stack:         []byte(""),
			component:     "",
			goroutineName: "",
			wantEvent:     true,
			wantStatus:    codes.Error,
			wantMessage:   "panic recovered in ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, recorder := newTestTracerProvider(t)
			tracer := provider.Tracer("test")
			ctx, span := tracer.Start(context.Background(), "test-span")

			RecordPanicToSpanWithComponent(
				ctx,
				tt.panicValue,
				tt.stack,
				tt.component,
				tt.goroutineName,
			)
			span.End()

			spans := recorder.Ended()
			require.Len(t, spans, 1)

			recordedSpan := spans[0]

			if tt.wantEvent {
				require.NotEmpty(t, recordedSpan.Events(), "expected panic event to be recorded")

				var foundPanicEvent bool

				for _, event := range recordedSpan.Events() {
					if event.Name == PanicSpanEventName {
						foundPanicEvent = true

						attrMap := make(map[string]string)
						for _, attr := range event.Attributes {
							attrMap[string(attr.Key)] = attr.Value.AsString()
						}

						assert.Contains(t, attrMap, "panic.value")
						assert.Contains(t, attrMap, "panic.stack")
						assert.Contains(t, attrMap, "panic.goroutine_name")
						assert.Equal(t, tt.goroutineName, attrMap["panic.goroutine_name"])

						if tt.component != "" {
							assert.Contains(t, attrMap, "panic.component")
							assert.Equal(t, tt.component, attrMap["panic.component"])
						}
					}
				}

				assert.True(t, foundPanicEvent, "panic.recovered event not found")
			}

			assert.Equal(t, tt.wantStatus, recordedSpan.Status().Code)
			assert.Equal(t, tt.wantMessage, recordedSpan.Status().Description)
		})
	}
}

func TestRecordPanicToSpan_NoActiveSpan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	require.NotPanics(t, func() {
		RecordPanicToSpan(ctx, "panic value", []byte("stack"), "goroutine")
	})
}

func TestRecordPanicToSpanWithComponent_NoActiveSpan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	require.NotPanics(t, func() {
		RecordPanicToSpanWithComponent(
			ctx,
			"panic value",
			[]byte("stack"),
			"component",
			"goroutine",
		)
	})
}

func TestRecordPanicToSpan_NonRecordingSpan(t *testing.T) {
	t.Parallel()

	provider := sdktrace.NewTracerProvider()

	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	tracer := provider.Tracer("test")
	_, nonRecordingSpan := tracer.Start(
		context.Background(),
		"test-span",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	nonRecordingSpan.End()

	ctx := trace.ContextWithSpan(context.Background(), nonRecordingSpan)

	require.NotPanics(t, func() {
		RecordPanicToSpan(ctx, "panic value", []byte("stack"), "goroutine")
	})
}

func TestRecordPanicToSpan_NilContext(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		RecordPanicToSpan(context.TODO(), "panic value", []byte("stack"), "goroutine")
	})
}

func TestRecordPanicToSpanWithComponent_NilContext(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		RecordPanicToSpanWithComponent(
			context.TODO(),
			"panic value",
			[]byte("stack"),
			"component",
			"goroutine",
		)
	})
}

func TestRecordPanicToSpan_VerifyErrorRecorded(t *testing.T) {
	t.Parallel()

	provider, recorder := newTestTracerProvider(t)
	tracer := provider.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")

	panicValue := "test panic"
	RecordPanicToSpan(ctx, panicValue, []byte("stack trace"), "worker")
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)

	recordedSpan := spans[0]
	events := recordedSpan.Events()

	var (
		hasExceptionEvent bool
		hasPanicEvent     bool
	)

	for _, event := range events {
		if event.Name == "exception" {
			hasExceptionEvent = true

			attrMap := make(map[string]string)
			for _, attr := range event.Attributes {
				attrMap[string(attr.Key)] = attr.Value.AsString()
			}

			assert.Contains(t, attrMap["exception.message"], "panic")
			assert.Contains(t, attrMap["exception.message"], panicValue)
		}

		if event.Name == PanicSpanEventName {
			hasPanicEvent = true
		}
	}

	assert.True(t, hasExceptionEvent, "expected exception event from RecordError")
	assert.True(t, hasPanicEvent, "expected panic.recovered event")
}

func TestRecordPanicToSpan_VerifySpanAttributes(t *testing.T) {
	t.Parallel()

	provider, recorder := newTestTracerProvider(t)
	tracer := provider.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")

	panicValue := "detailed panic message"
	stackTrace := []byte("goroutine 1 [running]:\nmain.main()\n\t/path/to/file.go:42")
	goroutineName := "main-worker"

	RecordPanicToSpan(ctx, panicValue, stackTrace, goroutineName)
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)

	recordedSpan := spans[0]

	var panicEvent *sdktrace.Event

	for i := range recordedSpan.Events() {
		if recordedSpan.Events()[i].Name == PanicSpanEventName {
			panicEvent = &recordedSpan.Events()[i]

			break
		}
	}

	require.NotNil(t, panicEvent, "panic event not found")

	attrMap := make(map[string]string)
	for _, attr := range panicEvent.Attributes {
		attrMap[string(attr.Key)] = attr.Value.AsString()
	}

	assert.Equal(t, panicValue, attrMap["panic.value"])
	assert.Equal(t, string(stackTrace), attrMap["panic.stack"])
	assert.Equal(t, goroutineName, attrMap["panic.goroutine_name"])
}

func TestRecordPanicToSpanWithComponent_VerifyComponentAttribute(t *testing.T) {
	t.Parallel()

	provider, recorder := newTestTracerProvider(t)
	tracer := provider.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")

	panicValue := "component panic"
	stackTrace := []byte("stack")
	component := "reconciliation"
	goroutineName := "ProcessBatch"

	RecordPanicToSpanWithComponent(ctx, panicValue, stackTrace, component, goroutineName)
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)

	recordedSpan := spans[0]

	var panicEvent *sdktrace.Event

	for i := range recordedSpan.Events() {
		if recordedSpan.Events()[i].Name == PanicSpanEventName {
			panicEvent = &recordedSpan.Events()[i]

			break
		}
	}

	require.NotNil(t, panicEvent, "panic event not found")

	attrMap := make(map[string]string)
	for _, attr := range panicEvent.Attributes {
		attrMap[string(attr.Key)] = attr.Value.AsString()
	}

	assert.Equal(t, panicValue, attrMap["panic.value"])
	assert.Equal(t, string(stackTrace), attrMap["panic.stack"])
	assert.Equal(t, goroutineName, attrMap["panic.goroutine_name"])
	assert.Equal(t, component, attrMap["panic.component"])
}

func TestRecordPanicToSpan_ComplexPanicValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		panicValue any
		wantValue  string
	}{
		{
			name:       "struct panic value",
			panicValue: struct{ Message string }{Message: "error"},
			wantValue:  "{error}",
		},
		{
			name:       "slice panic value",
			panicValue: []string{"a", "b", "c"},
			wantValue:  "[a b c]",
		},
		{
			name:       "map panic value",
			panicValue: map[string]int{"key": 1},
			wantValue:  "map[key:1]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, recorder := newTestTracerProvider(t)
			tracer := provider.Tracer("test")
			ctx, span := tracer.Start(context.Background(), "test-span")

			RecordPanicToSpan(ctx, tt.panicValue, []byte("stack"), "goroutine")
			span.End()

			spans := recorder.Ended()
			require.Len(t, spans, 1)

			recordedSpan := spans[0]

			var panicEvent *sdktrace.Event

			for i := range recordedSpan.Events() {
				if recordedSpan.Events()[i].Name == PanicSpanEventName {
					panicEvent = &recordedSpan.Events()[i]

					break
				}
			}

			require.NotNil(t, panicEvent)

			var panicValueAttr string

			for _, attr := range panicEvent.Attributes {
				if string(attr.Key) == "panic.value" {
					panicValueAttr = attr.Value.AsString()

					break
				}
			}

			assert.Equal(t, tt.wantValue, panicValueAttr)
		})
	}
}
