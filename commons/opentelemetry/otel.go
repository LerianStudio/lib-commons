package opentelemetry

import (
	"context"
	"net/http"
	"os"

	"github.com/LerianStudio/lib-commons/commons"
	constant "github.com/LerianStudio/lib-commons/commons/constants"
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"
)

type Telemetry struct {
	LibraryName               string
	ServiceName               string
	ServiceVersion            string
	DeploymentEnv             string
	CollectorExporterEndpoint string
	TracerProvider            *sdktrace.TracerProvider
	MetricProvider            *sdkmetric.MeterProvider
	LoggerProvider            *sdklog.LoggerProvider
	shutdown                  func()
	EnableTelemetry           bool
}

// NewResource creates a new resource with custom attributes.
func (tl *Telemetry) newResource() *sdkresource.Resource {
	// Create a resource with only our custom attributes to avoid schema URL conflicts
	r := sdkresource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(tl.ServiceName),
		semconv.ServiceVersion(tl.ServiceVersion),
		semconv.DeploymentEnvironmentName(tl.DeploymentEnv),
		semconv.TelemetrySDKName(constant.TelemetrySDKName),
		semconv.TelemetrySDKLanguageGo,
	)

	return r
}

// NewLoggerExporter creates a new logger exporter that writes to stdout.
func (tl *Telemetry) newLoggerExporter(ctx context.Context) (*otlploggrpc.Exporter, error) {
	exporter, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpoint(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")), otlploggrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return exporter, nil
}

// newMetricExporter creates a new metric exporter that writes to stdout.
func (tl *Telemetry) newMetricExporter(ctx context.Context) (*otlpmetricgrpc.Exporter, error) {
	exp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpoint(tl.CollectorExporterEndpoint), otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return exp, nil
}

// newTracerExporter creates a new tracer exporter that writes to stdout.
func (tl *Telemetry) newTracerExporter(ctx context.Context) (*otlptrace.Exporter, error) {
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(tl.CollectorExporterEndpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return exporter, nil
}

// NewLoggerProvider creates a new logger provider with stdout exporter and default resource.
func (tl *Telemetry) newLoggerProvider(rsc *sdkresource.Resource, exp *otlploggrpc.Exporter) *sdklog.LoggerProvider {
	bp := sdklog.NewBatchProcessor(exp)
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(rsc), sdklog.WithProcessor(bp))

	return lp
}

// newMeterProvider creates a new meter provider with stdout exporter and default resource.
func (tl *Telemetry) newMeterProvider(res *sdkresource.Resource, exp *otlpmetricgrpc.Exporter) *sdkmetric.MeterProvider {
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
	)

	return mp
}

// newTracerProvider creates a new tracer provider with stdout exporter and default resource.
func (tl *Telemetry) newTracerProvider(rsc *sdkresource.Resource, exp *otlptrace.Exporter) *sdktrace.TracerProvider {
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(rsc),
	)

	return tp
}

// ShutdownTelemetry shuts down the telemetry providers and exporters.
func (tl *Telemetry) ShutdownTelemetry() {
	tl.shutdown()
}

// InitializeTelemetry initializes the telemetry providers and sets them globally. (Logger is being passed as a parameter because it not exists in the global context at this point to be injected)
func (tl *Telemetry) InitializeTelemetry(logger log.Logger) *Telemetry {
	ctx := context.Background()

	if !tl.EnableTelemetry {
		logger.Warn("Telemetry turned off ⚠️ ")

		return nil
	}

	logger.Infof("Initializing telemetry...")

	r := tl.newResource()

	tExp, err := tl.newTracerExporter(ctx)
	if err != nil {
		logger.Fatalf("can't initialize tracer exporter: %v", err)
	}

	mExp, err := tl.newMetricExporter(ctx)
	if err != nil {
		logger.Fatalf("can't initialize metric exporter: %v", err)
	}

	lExp, err := tl.newLoggerExporter(ctx)
	if err != nil {
		logger.Fatalf("can't initialize logger exporter: %v", err)
	}

	mp := tl.newMeterProvider(r, mExp)
	otel.SetMeterProvider(mp)
	tl.MetricProvider = mp

	tp := tl.newTracerProvider(r, tExp)
	otel.SetTracerProvider(tp)
	tl.TracerProvider = tp

	lp := tl.newLoggerProvider(r, lExp)
	global.SetLoggerProvider(lp)

	tl.shutdown = func() {
		err := tExp.Shutdown(ctx)
		if err != nil {
			logger.Fatalf("can't shutdown tracer exporter: %v", err)
		}

		err = mExp.Shutdown(ctx)
		if err != nil {
			logger.Fatalf("can't shutdown metric exporter: %v", err)
		}

		err = lExp.Shutdown(ctx)
		if err != nil {
			logger.Fatalf("can't shutdown logger exporter: %v", err)
		}

		err = mp.Shutdown(ctx)
		if err != nil {
			logger.Fatalf("can't shutdown metric provider: %v", err)
		}

		err = tp.Shutdown(ctx)
		if err != nil {
			logger.Fatalf("can't shutdown tracer provider: %v", err)
		}

		err = lp.Shutdown(ctx)
		if err != nil {
			logger.Fatalf("can't shutdown logger provider: %v", err)
		}
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	logger.Infof("Telemetry initialized ✅ ")

	return &Telemetry{
		LibraryName:    tl.LibraryName,
		TracerProvider: tp,
		MetricProvider: mp,
		shutdown:       tl.shutdown,
	}
}

// SetSpanAttributesFromStruct converts a struct to a JSON string and sets it as an attribute on the span.
func SetSpanAttributesFromStruct(span *trace.Span, key string, valueStruct any) error {
	vStr, err := commons.StructToJSONString(valueStruct)
	if err != nil {
		return err
	}

	(*span).SetAttributes(attribute.KeyValue{
		Key:   attribute.Key(key),
		Value: attribute.StringValue(vStr),
	})

	return nil
}

// HandleSpanError sets the status of the span to error and records the error.
func HandleSpanError(span *trace.Span, message string, err error) {
	(*span).SetStatus(codes.Error, message+": "+err.Error())
	(*span).RecordError(err)
}

// InjectHTTPContext modifies HTTP headers for trace propagation in outgoing client requests
func InjectHTTPContext(headers *http.Header, ctx context.Context) {
	carrier := propagation.HeaderCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	for k, v := range carrier {
		if len(v) > 0 {
			headers.Set(k, v[0])
		}
	}
}

// ExtractHTTPContext extracts OpenTelemetry trace context from incoming HTTP headers
// and injects it into the context. It works with Fiber's HTTP context.
func ExtractHTTPContext(c *fiber.Ctx) context.Context {
	// Create a carrier from the HTTP headers
	carrier := propagation.HeaderCarrier{}

	// Extract headers that might contain trace information
	c.Request().Header.VisitAll(func(key, value []byte) {
		carrier.Set(string(key), string(value))
	})

	// Extract the trace context
	return otel.GetTextMapPropagator().Extract(c.UserContext(), carrier)
}

// InjectGRPCContext injects OpenTelemetry trace context into outgoing gRPC metadata.
// It normalizes W3C trace headers to lowercase for gRPC compatibility.
func InjectGRPCContext(ctx context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	if md == nil {
		md = metadata.New(nil)
	}

	// Returns the canonical format of the MIME header key s.
	// The canonicalization converts the first letter and any letter
	// following a hyphen to upper case; the rest are converted to lowercase.
	// For example, the canonical key for "accept-encoding" is "Accept-Encoding".
	// MIME header keys are assumed to be ASCII only.
	// If s contains a space or invalid header field bytes, it is
	// returned without modifications.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(md))

	if traceparentValues, exists := md["Traceparent"]; exists && len(traceparentValues) > 0 {
		md[constant.MetadataTraceparent] = traceparentValues
		delete(md, "Traceparent")
	}

	if tracestateValues, exists := md["Tracestate"]; exists && len(tracestateValues) > 0 {
		md[constant.MetadataTracestate] = tracestateValues
		delete(md, "Tracestate")
	}

	return metadata.NewOutgoingContext(ctx, md)
}

// ExtractGRPCContext extracts OpenTelemetry trace context from incoming gRPC metadata
// and injects it into the context. It handles case normalization for W3C trace headers.
func ExtractGRPCContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || md == nil {
		return ctx
	}

	mdCopy := md.Copy()

	if traceparentValues, exists := mdCopy["traceparent"]; exists && len(traceparentValues) > 0 {
		mdCopy["Traceparent"] = traceparentValues
		delete(mdCopy, "traceparent")
	}

	if tracestateValues, exists := mdCopy["tracestate"]; exists && len(tracestateValues) > 0 {
		mdCopy["Tracestate"] = tracestateValues
		delete(mdCopy, "tracestate")
	}

	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(mdCopy))
}
