package observability

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	assert.Equal(t, "unknown-service", config.ServiceName)
	assert.Equal(t, "0.0.0", config.ServiceVersion)
	assert.Equal(t, "production", config.Environment)
	assert.Equal(t, InfoLevel, config.LogLevel)
	assert.Equal(t, 0.1, config.TraceSampleRate)
	assert.True(t, config.EnabledComponents.Tracing)
	assert.True(t, config.EnabledComponents.Metrics)
	assert.True(t, config.EnabledComponents.Logging)
	assert.False(t, config.Insecure)
}

func TestOptions(t *testing.T) {
	tests := []struct {
		name    string
		option  Option
		check   func(*testing.T, *Config)
		wantErr bool
	}{
		{
			name:   "WithServiceName",
			option: WithServiceName("test-service"),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, "test-service", c.ServiceName)
			},
		},
		{
			name:    "WithServiceName empty",
			option:  WithServiceName(""),
			wantErr: true,
		},
		{
			name:   "WithServiceVersion",
			option: WithServiceVersion("1.2.3"),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, "1.2.3", c.ServiceVersion)
			},
		},
		{
			name:   "WithEnvironment",
			option: WithEnvironment("staging"),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, "staging", c.Environment)
			},
		},
		{
			name:   "WithCollectorEndpoint",
			option: WithCollectorEndpoint("localhost:4317"),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, "localhost:4317", c.CollectorEndpoint)
			},
		},
		{
			name:   "WithLogLevel",
			option: WithLogLevel(DebugLevel),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, DebugLevel, c.LogLevel)
			},
		},
		{
			name:    "WithLogLevel invalid",
			option:  WithLogLevel(LogLevel(99)),
			wantErr: true,
		},
		{
			name:   "WithLogOutput",
			option: WithLogOutput(&bytes.Buffer{}),
			check: func(t *testing.T, c *Config) {
				assert.NotNil(t, c.LogOutput)
			},
		},
		{
			name:   "WithTraceSampleRate",
			option: WithTraceSampleRate(0.5),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, 0.5, c.TraceSampleRate)
			},
		},
		{
			name:    "WithTraceSampleRate invalid",
			option:  WithTraceSampleRate(1.5),
			wantErr: true,
		},
		{
			name:   "WithComponentEnabled",
			option: WithComponentEnabled(false, true, false),
			check: func(t *testing.T, c *Config) {
				assert.False(t, c.EnabledComponents.Tracing)
				assert.True(t, c.EnabledComponents.Metrics)
				assert.False(t, c.EnabledComponents.Logging)
			},
		},
		{
			name: "WithAttributes",
			option: WithAttributes(
				attribute.String("test", "value"),
				attribute.Int("count", 42),
			),
			check: func(t *testing.T, c *Config) {
				assert.Len(t, c.Attributes, 2)
			},
		},
		{
			name:   "WithInsecure",
			option: WithInsecure(true),
			check: func(t *testing.T, c *Config) {
				assert.True(t, c.Insecure)
			},
		},
		{
			name:   "WithDevelopmentDefaults",
			option: WithDevelopmentDefaults(),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, "development", c.Environment)
				assert.Equal(t, DebugLevel, c.LogLevel)
				assert.Equal(t, 0.5, c.TraceSampleRate)
				assert.True(t, c.Insecure)
			},
		},
		{
			name:   "WithProductionDefaults",
			option: WithProductionDefaults(),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, "production", c.Environment)
				assert.Equal(t, InfoLevel, c.LogLevel)
				assert.Equal(t, 0.1, c.TraceSampleRate)
				assert.False(t, c.Insecure)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			err := tt.option(config)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.check != nil {
					tt.check(t, config)
				}
			}
		})
	}
}

func TestNewProvider(t *testing.T) {
	ctx := context.Background()

	t.Run("with all components enabled", func(t *testing.T) {
		buf := &bytes.Buffer{}
		provider, err := New(ctx,
			WithServiceName("test-service"),
			WithServiceVersion("1.0.0"),
			WithEnvironment("test"),
			WithLogOutput(buf),
			WithComponentEnabled(true, true, true),
		)

		require.NoError(t, err)
		require.NotNil(t, provider)

		assert.True(t, provider.IsEnabled())
		assert.NotNil(t, provider.Tracer())
		assert.NotNil(t, provider.Meter())
		assert.NotNil(t, provider.Logger())

		// Test logger output
		provider.Logger().Info("test message")
		assert.Contains(t, buf.String(), "test message")
		assert.Contains(t, buf.String(), "INFO")

		// Shutdown
		err = provider.Shutdown(ctx)
		assert.NoError(t, err)
		assert.False(t, provider.IsEnabled())
	})

	t.Run("with all components disabled", func(t *testing.T) {
		provider, err := New(ctx,
			WithServiceName("test-service"),
			WithComponentEnabled(false, false, false),
		)

		require.NoError(t, err)
		require.NotNil(t, provider)

		assert.True(t, provider.IsEnabled())
		assert.NotNil(t, provider.Tracer()) // Returns no-op tracer
		assert.NotNil(t, provider.Meter())  // Returns no-op meter
		assert.NotNil(t, provider.Logger()) // Returns no-op logger
	})

	t.Run("with collector endpoint", func(t *testing.T) {
		provider, err := New(ctx,
			WithServiceName("test-service"),
			WithCollectorEndpoint("localhost:4317"),
			WithInsecure(true),
		)

		require.NoError(t, err)
		require.NotNil(t, provider)

		err = provider.Shutdown(ctx)
		assert.NoError(t, err)
	})
}

func TestWithSpan(t *testing.T) {
	ctx := context.Background()
	buf := &bytes.Buffer{}

	provider, err := New(ctx,
		WithServiceName("test-service"),
		WithLogOutput(buf),
	)
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	t.Run("successful operation", func(t *testing.T) {
		called := false
		err := WithSpan(ctx, provider, "test-span", func(_ context.Context) error {
			called = true
			// Verify we have a span in context
			span := trace.SpanFromContext(ctx)
			assert.NotNil(t, span)
			// Note: span.IsRecording() might be false due to sampling or no exporter
			// but we should still have a valid span context
			assert.True(t, span.SpanContext().IsValid())
			return nil
		})

		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("failed operation", func(t *testing.T) {
		testErr := assert.AnError
		err := WithSpan(ctx, provider, "test-span", func(_ context.Context) error {
			return testErr
		})

		assert.Equal(t, testErr, err)
	})

	t.Run("with disabled provider", func(t *testing.T) {
		_ = provider.Shutdown(ctx)

		called := false
		err := WithSpan(ctx, provider, "test-span", func(_ context.Context) error {
			called = true
			return nil
		})

		assert.NoError(t, err)
		assert.True(t, called)
	})
}

func TestRecordMetric(t *testing.T) {
	ctx := context.Background()

	provider, err := New(ctx,
		WithServiceName("test-service"),
	)
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	// Should not panic
	RecordMetric(ctx, provider, "test.metric", 42,
		attribute.String("test", "value"),
	)
}

func TestRecordDuration(t *testing.T) {
	ctx := context.Background()

	provider, err := New(ctx,
		WithServiceName("test-service"),
	)
	require.NoError(t, err)
	defer func() { _ = provider.Shutdown(ctx) }()

	start := time.Now().Add(-100 * time.Millisecond)

	// Should not panic
	RecordDuration(ctx, provider, "test.duration", start,
		attribute.String("test", "value"),
	)
}
