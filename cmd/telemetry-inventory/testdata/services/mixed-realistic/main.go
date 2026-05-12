package main

import (
	"context"
	"net/http"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	libhttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

func wireFrameworks(handler http.Handler) {
	_ = otelhttp.NewHandler(handler, "GET /accounts")
	_ = otelgrpc.NewServerHandler()
	_ = libhttp.NewTelemetryMiddleware(nil)
}

func emit(ctx context.Context, meter metric.Meter, tracer trace.Tracer, factory *metrics.MetricsFactory, logger log.Logger) error {
	ctx, span := tracer.Start(ctx, "Accounts.Create", trace.WithAttributes(attribute.String("tenant_id", "t-1")))
	defer span.End()
	logger.Log(ctx, log.LevelInfo, "creating account", log.String("tenant_id", "t-1"), log.String("trace_id", "abc"))

	direct, _ := meter.Int64Counter("direct_accounts_total")
	direct.Add(ctx, 1, metric.WithAttributes(attribute.String("tenant_id", "t-1")))

	duration, _ := meter.Int64Histogram("direct_request_duration_ms", metric.WithExplicitBucketBoundaries(1, 5, 10))
	duration.Record(ctx, 5, metric.WithAttributes(attribute.String("tenant_id", "t-1")))

	builder, err := factory.Counter(metrics.Metric{Name: "builder_accounts_total", Unit: "1", Description: "builder accounts"})
	if err != nil {
		span.RecordError(err)
		return err
	}
	if err := builder.WithAttributes(attribute.String("tenant_id", "t-1")).AddOne(ctx); err != nil {
		span.RecordError(err)
		return err
	}
	return factory.RecordAccountCreated(ctx, attribute.String("tenant_id", "t-1"))
}

func main() {}
