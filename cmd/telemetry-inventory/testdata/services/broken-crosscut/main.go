package main

import (
	"context"
	"errors"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func broken(ctx context.Context, tracer trace.Tracer, factory *metrics.MetricsFactory, logger log.Logger) error {
	ctx, span := tracer.Start(ctx, "Broken.Op")
	defer span.End()
	counter, err := factory.Counter(metrics.Metric{Name: "broken_total", Unit: "1"})
	if err != nil {
		return err
	}
	if err := counter.WithAttributes(attribute.String("tenant_id", "t-1")).AddOne(ctx); err != nil {
		return err
	}
	logger.Log(ctx, log.LevelInfo, "missing tenant field", log.String("operation", "broken"))
	if err := errors.New("boom"); err != nil {
		return err
	}
	return nil
}

func main() {}
