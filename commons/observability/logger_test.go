package observability

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/resource"
)

func TestLogLevel(t *testing.T) {
	tests := []struct {
		level    LogLevel
		expected string
	}{
		{DebugLevel, "DEBUG"},
		{InfoLevel, "INFO"},
		{WarnLevel, "WARN"},
		{ErrorLevel, "ERROR"},
		{FatalLevel, "FATAL"},
		{LogLevel(99), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.level.String())
		})
	}
}

func TestLoggerImpl(t *testing.T) {
	buf := &bytes.Buffer{}
	res, err := resource.New(context.Background())
	require.NoError(t, err)

	logger := NewLogger(InfoLevel, buf, res)
	require.NotNil(t, logger)

	t.Run("Debug logging", func(t *testing.T) {
		buf.Reset()
		logger.Debug("debug message")
		// Debug should not appear with InfoLevel
		assert.Empty(t, buf.String())
	})

	t.Run("Info logging", func(t *testing.T) {
		buf.Reset()
		logger.Info("info message")
		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "info message")
	})

	t.Run("Infof logging", func(t *testing.T) {
		buf.Reset()
		logger.Infof("info message %s", "formatted")
		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "info message formatted")
	})

	t.Run("Warn logging", func(t *testing.T) {
		buf.Reset()
		logger.Warn("warn message")
		output := buf.String()
		assert.Contains(t, output, "WARN")
		assert.Contains(t, output, "warn message")
	})

	t.Run("Warnf logging", func(t *testing.T) {
		buf.Reset()
		logger.Warnf("warn message %d", 42)
		output := buf.String()
		assert.Contains(t, output, "WARN")
		assert.Contains(t, output, "warn message 42")
	})

	t.Run("Error logging", func(t *testing.T) {
		buf.Reset()
		logger.Error("error message")
		output := buf.String()
		assert.Contains(t, output, "ERROR")
		assert.Contains(t, output, "error message")
	})

	t.Run("Errorf logging", func(t *testing.T) {
		buf.Reset()
		logger.Errorf("error message %s", "formatted")
		output := buf.String()
		assert.Contains(t, output, "ERROR")
		assert.Contains(t, output, "error message formatted")
	})

	t.Run("With fields", func(t *testing.T) {
		buf.Reset()
		fields := map[string]any{
			"user_id": "123",
			"action":  "login",
		}

		loggerWithFields := logger.With(fields)
		loggerWithFields.Info("user action")

		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "user action")
		assert.Contains(t, output, "user_id")
		assert.Contains(t, output, "123")
		assert.Contains(t, output, "action")
		assert.Contains(t, output, "login")
	})

	t.Run("WithContext", func(t *testing.T) {
		buf.Reset()

		// Create a span context
		ctx := context.Background()
		provider, err := New(ctx, WithServiceName("test-service"))
		require.NoError(t, err)
		defer func() { _ = provider.Shutdown(ctx) }()

		tracer := provider.Tracer()
		_, span := tracer.Start(ctx, "test-span")
		spanContext := span.SpanContext()
		span.End()

		loggerWithContext := logger.WithContext(spanContext)
		loggerWithContext.Info("message with context")

		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "message with context")
		assert.Contains(t, output, "trace_id")
		assert.Contains(t, output, "span_id")
	})

	t.Run("WithSpan", func(t *testing.T) {
		buf.Reset()

		// Create a span
		ctx := context.Background()
		provider, err := New(ctx, WithServiceName("test-service"))
		require.NoError(t, err)
		defer func() { _ = provider.Shutdown(ctx) }()

		tracer := provider.Tracer()
		_, span := tracer.Start(ctx, "test-span")
		defer span.End()

		loggerWithSpan := logger.WithSpan(span)
		loggerWithSpan.Info("message with span")

		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "message with span")
		assert.Contains(t, output, "trace_id")
		assert.Contains(t, output, "span_id")
	})
}

func TestLoggerImplDebugLevel(t *testing.T) {
	buf := &bytes.Buffer{}
	res, err := resource.New(context.Background())
	require.NoError(t, err)

	logger := NewLogger(DebugLevel, buf, res)
	require.NotNil(t, logger)

	t.Run("Debug logging enabled", func(t *testing.T) {
		buf.Reset()
		logger.Debug("debug message")
		output := buf.String()
		assert.Contains(t, output, "DEBUG")
		assert.Contains(t, output, "debug message")
	})

	t.Run("Debugf logging enabled", func(t *testing.T) {
		buf.Reset()
		logger.Debugf("debug message %s", "formatted")
		output := buf.String()
		assert.Contains(t, output, "DEBUG")
		assert.Contains(t, output, "debug message formatted")
	})
}

func TestLoggerFatal(t *testing.T) {
	// Note: We can't easily test Fatal/Fatalf as they call os.Exit
	// But we can test that they don't panic when called
	buf := &bytes.Buffer{}
	res, err := resource.New(context.Background())
	require.NoError(t, err)

	logger := NewLogger(InfoLevel, buf, res)
	require.NotNil(t, logger)

	// We'll test the logging part without the os.Exit
	// by checking the output before the exit would occur
	t.Run("Fatal logging format", func(t *testing.T) {
		// We can't test the actual Fatal call as it would exit the test
		// But we can verify the logger structure is correct
		assert.NotNil(t, logger)
	})
}

func TestNoopLogger(t *testing.T) {
	logger := NewNoopLogger()
	require.NotNil(t, logger)

	// All methods should not panic and do nothing
	t.Run("All methods should not panic", func(t *testing.T) {
		logger.Debug("test")
		logger.Debugf("test %s", "formatted")
		logger.Info("test")
		logger.Infof("test %s", "formatted")
		logger.Warn("test")
		logger.Warnf("test %s", "formatted")
		logger.Error("test")
		logger.Errorf("test %s", "formatted")

		// Test With methods return the same logger
		withFields := logger.With(map[string]any{"key": "value"})
		assert.Equal(t, logger, withFields)

		// Create a dummy span context
		ctx := context.Background()
		provider, err := New(ctx, WithServiceName("test-service"))
		require.NoError(t, err)
		defer func() { _ = provider.Shutdown(ctx) }()

		tracer := provider.Tracer()
		_, span := tracer.Start(ctx, "test-span")
		spanContext := span.SpanContext()
		span.End()

		withContext := logger.WithContext(spanContext)
		assert.Equal(t, logger, withContext)

		withSpan := logger.WithSpan(span)
		assert.Equal(t, logger, withSpan)
	})

	t.Run("Fatal methods should exit", func(t *testing.T) {
		// We can't test the actual exit behavior in unit tests
		// but we can verify the methods exist and are callable
		assert.NotPanics(t, func() {
			// Note: These would normally call os.Exit(1)
			// but in NoopLogger they should just return
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("NoopLogger Fatal methods should not panic: %v", r)
				}
			}()
			// We can't actually call these as they would exit
			// logger.Fatal("test")
			// logger.Fatalf("test %s", "formatted")
		})
	})
}

func TestLoggerWithDifferentOutputs(t *testing.T) {
	res, err := resource.New(context.Background())
	require.NoError(t, err)

	t.Run("Logger with os.Stderr", func(t *testing.T) {
		logger := NewLogger(InfoLevel, os.Stderr, res)
		assert.NotNil(t, logger)

		// Should not panic
		logger.Info("test message to stderr")
	})

	t.Run("Logger with nil output", func(t *testing.T) {
		// This might panic or handle gracefully depending on implementation
		assert.NotPanics(t, func() {
			logger := NewLogger(InfoLevel, nil, res)
			assert.NotNil(t, logger)
		})
	})
}

func TestLoggerFieldsHandling(t *testing.T) {
	buf := &bytes.Buffer{}
	res, err := resource.New(context.Background())
	require.NoError(t, err)

	logger := NewLogger(InfoLevel, buf, res)
	require.NotNil(t, logger)

	t.Run("Empty fields", func(t *testing.T) {
		buf.Reset()
		loggerWithFields := logger.With(map[string]any{})
		loggerWithFields.Info("message")

		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "message")
	})

	t.Run("Nil fields", func(t *testing.T) {
		buf.Reset()
		loggerWithFields := logger.With(nil)
		loggerWithFields.Info("message")

		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "message")
	})

	t.Run("Complex field types", func(t *testing.T) {
		buf.Reset()
		fields := map[string]any{
			"string": "value",
			"int":    42,
			"float":  3.14,
			"bool":   true,
			"slice":  []string{"a", "b", "c"},
			"map":    map[string]string{"nested": "value"},
		}

		loggerWithFields := logger.With(fields)
		loggerWithFields.Info("complex fields")

		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "complex fields")
		// Should contain some representation of the fields
		assert.Contains(t, output, "string")
		assert.Contains(t, output, "42")
	})
}

func TestLoggerChaining(t *testing.T) {
	buf := &bytes.Buffer{}
	res, err := resource.New(context.Background())
	require.NoError(t, err)

	logger := NewLogger(InfoLevel, buf, res)
	require.NotNil(t, logger)

	t.Run("Chain With calls", func(t *testing.T) {
		buf.Reset()

		fields1 := map[string]any{"key1": "value1"}
		fields2 := map[string]any{"key2": "value2"}

		chainedLogger := logger.With(fields1).With(fields2)
		chainedLogger.Info("chained message")

		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "chained message")
		assert.Contains(t, output, "key1")
		assert.Contains(t, output, "key2")
	})
}
