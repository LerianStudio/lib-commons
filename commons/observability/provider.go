package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Constants for attribute keys
const (
	// Service attributes
	KeyServiceName    = "service.name"
	KeyServiceVersion = "service.version"
	KeyEnvironment    = "environment"

	// Operation attributes
	KeyOperationName = "operation.name"
	KeyOperationType = "operation.type"

	// Resource attributes
	KeyResourceType = "resource.type"
	KeyResourceID   = "resource.id"

	// Organization attributes
	KeyOrganizationID = "organization.id"
	KeyLedgerID       = "ledger.id"
	KeyAccountID      = "account.id"

	// HTTP attributes
	KeyHTTPMethod   = "http.method"
	KeyHTTPPath     = "http.path"
	KeyHTTPStatus   = "http.status_code"
	KeyHTTPHost     = "http.host"
	KeyErrorCode    = "error.code"
	KeyErrorMessage = "error.message"

	// Metric names
	MetricRequestTotal        = "request.total"
	MetricRequestDuration     = "request.duration"
	MetricRequestErrorTotal   = "request.error.total"
	MetricRequestSuccess      = "request.success"
	MetricRequestRetryTotal   = "request.retry.total"
	MetricRequestBatchSize    = "request.batch.size"
	MetricRequestBatchLatency = "request.batch.latency"
)

// Provider is the interface for observability providers
type Provider interface {
	// Tracer returns a tracer for creating spans
	Tracer() trace.Tracer

	// Meter returns a meter for creating metrics
	Meter() metric.Meter

	// TracerProvider returns the underlying tracer provider
	TracerProvider() trace.TracerProvider

	// MeterProvider returns the underlying meter provider
	MeterProvider() metric.MeterProvider

	// Logger returns a logger
	Logger() Logger

	// Shutdown gracefully shuts down the provider
	Shutdown(ctx context.Context) error

	// IsEnabled returns true if observability is enabled
	IsEnabled() bool
}

// Config holds the configuration for the observability provider
type Config struct {
	// ServiceName is the name of the service
	ServiceName string

	// ServiceVersion is the version of the service
	ServiceVersion string

	// Environment is the environment (development, staging, production)
	Environment string

	// CollectorEndpoint is the endpoint for the OpenTelemetry collector
	CollectorEndpoint string

	// LogLevel is the minimum log level
	LogLevel LogLevel

	// LogOutput is where to write logs (defaults to os.Stderr)
	LogOutput io.Writer

	// TraceSampleRate is the sampling rate for traces (0.0 to 1.0)
	TraceSampleRate float64

	// EnabledComponents controls which components are enabled
	EnabledComponents EnabledComponents

	// Attributes are additional attributes to add to all telemetry
	Attributes []attribute.KeyValue

	// Propagators for context propagation
	Propagators []propagation.TextMapPropagator

	// PropagationHeaders to extract for trace context
	PropagationHeaders []string

	// Insecure disables TLS for gRPC connections
	Insecure bool
}

// EnabledComponents controls which observability components are enabled
type EnabledComponents struct {
	Tracing bool
	Metrics bool
	Logging bool
}

// Option defines a function that configures the observability Config
type Option func(*Config) error

// WithServiceName sets the service name
func WithServiceName(name string) Option {
	return func(c *Config) error {
		if name == "" {
			return errors.New("service name cannot be empty")
		}

		c.ServiceName = name

		return nil
	}
}

// WithServiceVersion sets the service version
func WithServiceVersion(version string) Option {
	return func(c *Config) error {
		if version == "" {
			return errors.New("service version cannot be empty")
		}

		c.ServiceVersion = version

		return nil
	}
}

// WithEnvironment sets the environment
func WithEnvironment(env string) Option {
	return func(c *Config) error {
		if env == "" {
			return errors.New("environment cannot be empty")
		}

		c.Environment = env

		return nil
	}
}

// WithCollectorEndpoint sets the OpenTelemetry collector endpoint
func WithCollectorEndpoint(endpoint string) Option {
	return func(c *Config) error {
		if endpoint == "" {
			return errors.New("collector endpoint cannot be empty")
		}

		c.CollectorEndpoint = endpoint

		return nil
	}
}

// WithLogLevel sets the minimum log level
func WithLogLevel(level LogLevel) Option {
	return func(c *Config) error {
		if level < DebugLevel || level > FatalLevel {
			return fmt.Errorf("invalid log level: %d", level)
		}

		c.LogLevel = level

		return nil
	}
}

// WithLogOutput sets the writer for logs
func WithLogOutput(output io.Writer) Option {
	return func(c *Config) error {
		if output == nil {
			return errors.New("log output cannot be nil")
		}

		c.LogOutput = output

		return nil
	}
}

// WithTraceSampleRate sets the sampling rate for traces
func WithTraceSampleRate(rate float64) Option {
	return func(c *Config) error {
		if rate < 0.0 || rate > 1.0 {
			return errors.New("trace sample rate must be between 0.0 and 1.0")
		}

		c.TraceSampleRate = rate

		return nil
	}
}

// WithComponentEnabled enables or disables specific components
func WithComponentEnabled(tracing, metrics, logging bool) Option {
	return func(c *Config) error {
		c.EnabledComponents.Tracing = tracing
		c.EnabledComponents.Metrics = metrics
		c.EnabledComponents.Logging = logging

		return nil
	}
}

// WithAttributes adds additional attributes to all telemetry
func WithAttributes(attrs ...attribute.KeyValue) Option {
	return func(c *Config) error {
		c.Attributes = append(c.Attributes, attrs...)
		return nil
	}
}

// WithPropagators sets the propagators for context propagation
func WithPropagators(propagators ...propagation.TextMapPropagator) Option {
	return func(c *Config) error {
		if len(propagators) == 0 {
			return errors.New("at least one propagator must be provided")
		}

		c.Propagators = propagators

		return nil
	}
}

// WithInsecure disables TLS for gRPC connections
func WithInsecure(insecure bool) Option {
	return func(c *Config) error {
		c.Insecure = insecure
		return nil
	}
}

// WithDevelopmentDefaults sets reasonable defaults for development
func WithDevelopmentDefaults() Option {
	return func(c *Config) error {
		c.Environment = "development"
		c.LogLevel = DebugLevel
		c.TraceSampleRate = 0.5
		c.Insecure = true

		return nil
	}
}

// WithProductionDefaults sets reasonable defaults for production
func WithProductionDefaults() Option {
	return func(c *Config) error {
		c.Environment = "production"
		c.LogLevel = InfoLevel
		c.TraceSampleRate = 0.1
		c.Insecure = false

		return nil
	}
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		ServiceName:     "unknown-service",
		ServiceVersion:  "0.0.0",
		Environment:     "production",
		LogLevel:        InfoLevel,
		LogOutput:       os.Stderr,
		TraceSampleRate: 0.1,
		EnabledComponents: EnabledComponents{
			Tracing: true,
			Metrics: true,
			Logging: true,
		},
		PropagationHeaders: []string{
			"traceparent",
			"tracestate",
			"baggage",
			"x-request-id",
			"x-correlation-id",
		},
		Insecure: false,
	}
}

// ObservabilityProvider is the main implementation of the Provider interface
type ObservabilityProvider struct {
	config            *Config
	tracerProvider    *sdktrace.TracerProvider
	meterProvider     *sdkmetric.MeterProvider
	logger            Logger
	tracer            trace.Tracer
	meter             metric.Meter
	enabled           bool
	shutdownFunctions []func(context.Context) error
}

// New creates a new observability provider with the given options
func New(ctx context.Context, opts ...Option) (Provider, error) {
	// Start with default configuration
	config := DefaultConfig()

	// Apply all options
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	provider := &ObservabilityProvider{
		config:            config,
		shutdownFunctions: []func(context.Context) error{},
		enabled:           true,
	}

	// Create a resource with service information
	res, err := provider.createResource()
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Initialize tracing if enabled
	if config.EnabledComponents.Tracing {
		if err := provider.initTracing(ctx, res); err != nil {
			return nil, fmt.Errorf("failed to initialize tracing: %w", err)
		}
	}

	// Initialize metrics if enabled
	if config.EnabledComponents.Metrics {
		if err := provider.initMetrics(ctx, res); err != nil {
			return nil, fmt.Errorf("failed to initialize metrics: %w", err)
		}
	}

	// Initialize logging if enabled
	if config.EnabledComponents.Logging {
		if err := provider.initLogging(res); err != nil {
			return nil, fmt.Errorf("failed to initialize logging: %w", err)
		}
	}

	// Set up context propagation
	provider.setupPropagation()

	return provider, nil
}

// createResource creates an OpenTelemetry resource with service information
func (p *ObservabilityProvider) createResource() (*sdkresource.Resource, error) {
	attributes := []attribute.KeyValue{
		semconv.ServiceNameKey.String(p.config.ServiceName),
		semconv.ServiceVersionKey.String(p.config.ServiceVersion),
		semconv.DeploymentEnvironmentKey.String(p.config.Environment),
		attribute.String("language", "go"),
	}

	// Add custom attributes
	attributes = append(attributes, p.config.Attributes...)

	// Create and return the resource
	return sdkresource.Merge(
		sdkresource.Default(),
		sdkresource.NewWithAttributes(
			semconv.SchemaURL,
			attributes...,
		),
	)
}

// initTracing initializes OpenTelemetry tracing
func (p *ObservabilityProvider) initTracing(ctx context.Context, res *sdkresource.Resource) error {
	var exporter *otlptrace.Exporter

	var err error

	// Set up exporter if endpoint is provided
	if p.config.CollectorEndpoint != "" {
		// Use OTLP exporter with gRPC
		opts := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(p.config.CollectorEndpoint),
		}
		if p.config.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}

		exporter, err = otlptracegrpc.New(ctx, opts...)
		if err != nil {
			return fmt.Errorf("failed to create trace exporter: %w", err)
		}
	}

	// Configure and create the trace provider
	var tracerOpts []sdktrace.TracerProviderOption
	tracerOpts = append(tracerOpts,
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(p.config.TraceSampleRate)),
	)

	// Add exporter if available
	if exporter != nil {
		tracerOpts = append(tracerOpts, sdktrace.WithBatcher(exporter))
	}

	p.tracerProvider = sdktrace.NewTracerProvider(tracerOpts...)

	// Set the global trace provider
	otel.SetTracerProvider(p.tracerProvider)

	// Create a tracer for this library
	p.tracer = p.tracerProvider.Tracer(
		"github.com/LerianStudio/lib-commons/" + p.config.ServiceName,
	)

	// Add shutdown function
	p.shutdownFunctions = append(p.shutdownFunctions, func(ctx context.Context) error {
		return p.tracerProvider.Shutdown(ctx)
	})

	return nil
}

// initMetrics initializes OpenTelemetry metrics
func (p *ObservabilityProvider) initMetrics(ctx context.Context, res *sdkresource.Resource) error {
	var exporter sdkmetric.Exporter

	var err error

	// Set up exporter
	if p.config.CollectorEndpoint != "" {
		// Use OTLP exporter with gRPC
		opts := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(p.config.CollectorEndpoint),
		}
		if p.config.Insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}

		exporter, err = otlpmetricgrpc.New(ctx, opts...)
		if err != nil {
			return fmt.Errorf("failed to create metric exporter: %w", err)
		}
	} else {
		// No exporter if no endpoint specified
		return nil
	}

	// Configure and create the meter provider
	p.meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)

	// Set the global meter provider
	otel.SetMeterProvider(p.meterProvider)

	// Create a meter for this library
	p.meter = p.meterProvider.Meter(
		"github.com/LerianStudio/lib-commons/" + p.config.ServiceName,
	)

	// Add shutdown function
	p.shutdownFunctions = append(p.shutdownFunctions, func(ctx context.Context) error {
		return p.meterProvider.Shutdown(ctx)
	})

	return nil
}

// initLogging initializes structured logging
func (p *ObservabilityProvider) initLogging(res *sdkresource.Resource) error {
	// Create logger with resource attributes
	p.logger = NewLogger(p.config.LogLevel, p.config.LogOutput, res)
	return nil
}

// setupPropagation configures context propagation for distributed tracing
func (p *ObservabilityProvider) setupPropagation() {
	// Set up propagators if provided, otherwise use defaults
	if len(p.config.Propagators) > 0 {
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			p.config.Propagators...,
		))
	} else {
		// Use default propagators
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
	}
}

// Tracer returns a tracer for creating spans
func (p *ObservabilityProvider) Tracer() trace.Tracer {
	if !p.enabled || !p.config.EnabledComponents.Tracing || p.tracer == nil {
		// Return a no-op tracer if tracing is disabled
		return noop.NewTracerProvider().Tracer("")
	}

	return p.tracer
}

// Meter returns a meter for creating metrics
func (p *ObservabilityProvider) Meter() metric.Meter {
	if !p.enabled || !p.config.EnabledComponents.Metrics || p.meter == nil {
		// Return the default global meter if metrics are disabled
		return otel.GetMeterProvider().Meter("")
	}

	return p.meter
}

// TracerProvider returns the underlying tracer provider
func (p *ObservabilityProvider) TracerProvider() trace.TracerProvider {
	if !p.enabled || !p.config.EnabledComponents.Tracing || p.tracerProvider == nil {
		// Return a no-op tracer provider if tracing is disabled or not initialized
		return noop.NewTracerProvider()
	}

	return p.tracerProvider
}

// MeterProvider returns the underlying meter provider
func (p *ObservabilityProvider) MeterProvider() metric.MeterProvider {
	if !p.enabled || !p.config.EnabledComponents.Metrics || p.meterProvider == nil {
		// Return the default global meter provider if metrics are disabled or not initialized
		return otel.GetMeterProvider()
	}

	return p.meterProvider
}

// Logger returns a logger
func (p *ObservabilityProvider) Logger() Logger {
	if !p.enabled || !p.config.EnabledComponents.Logging || p.logger == nil {
		// Return a no-op logger if logging is disabled
		return NewNoopLogger()
	}

	return p.logger
}

// Shutdown gracefully shuts down the provider and all its components
func (p *ObservabilityProvider) Shutdown(ctx context.Context) error {
	if !p.enabled {
		return nil
	}

	p.enabled = false

	// Call all shutdown functions
	var errors []error

	for _, shutdownFn := range p.shutdownFunctions {
		if err := shutdownFn(ctx); err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errors)
	}

	return nil
}

// IsEnabled returns true if observability is enabled
func (p *ObservabilityProvider) IsEnabled() bool {
	return p.enabled
}

// WithSpan creates a new span and executes the function within that span
func WithSpan(ctx context.Context, provider Provider, name string, fn func(context.Context) error, opts ...trace.SpanStartOption) error {
	// If observability is disabled, just run the function
	if !provider.IsEnabled() {
		return fn(ctx)
	}

	// Start a new span
	ctx, span := provider.Tracer().Start(ctx, name, opts...)
	defer span.End()

	// Run the function and handle errors
	err := fn(ctx)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// RecordMetric records a metric using the provided meter
func RecordMetric(ctx context.Context, provider Provider, name string, value float64, attrs ...attribute.KeyValue) {
	// If observability is disabled, just return
	if !provider.IsEnabled() {
		return
	}

	counter, err := provider.Meter().Float64Counter(name)
	if err != nil {
		provider.Logger().Errorf("Failed to create counter for metric %s: %v", name, err)
		return
	}

	counter.Add(ctx, value, metric.WithAttributes(attrs...))
}

// RecordDuration records a duration metric using the provided meter
func RecordDuration(ctx context.Context, provider Provider, name string, start time.Time, attrs ...attribute.KeyValue) {
	// If observability is disabled, just return
	if !provider.IsEnabled() {
		return
	}

	duration := time.Since(start).Milliseconds()

	histogram, err := provider.Meter().Int64Histogram(name)
	if err != nil {
		provider.Logger().Errorf("Failed to create histogram for metric %s: %v", name, err)
		return
	}

	histogram.Record(ctx, duration, metric.WithAttributes(attrs...))
}
