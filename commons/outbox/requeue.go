package outbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace/noop"
)

// RequeueInvalid puts the INVALID event id back in the queue so a later dispatch cycle, the
// sole publisher, publishes it again with the same id, event type and payload. ctx carries
// the tenant; the dispatcher's claim scope applies, as to every claim.
func (dispatcher *Dispatcher) RequeueInvalid(ctx context.Context, id uuid.UUID) error {
	if dispatcher == nil || dispatcher.repo == nil {
		return ErrOutboxDispatcherRequired
	}

	requeuer, ok := dispatcher.repo.(InvalidRequeuer)
	if !ok {
		return ErrOutboxRequeueUnsupported
	}

	if ctx == nil {
		ctx = context.Background()
	}

	tracer := dispatcher.tracer
	if nilcheck.Interface(tracer) {
		tracer = noop.NewTracerProvider().Tracer("commons.noop")
	}

	ctx, span := tracer.Start(ctx, "outbox.dispatcher.requeue_invalid")
	defer span.End()

	if err := requeuer.RequeueInvalid(dispatcher.withClaimScope(ctx), id); err != nil {
		if errors.Is(err, ErrOutboxEventNotInvalid) {
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "outbox event is not invalid", err)
		} else {
			libOpentelemetry.HandleSpanError(span, "failed to requeue invalid outbox event", err)
		}

		return fmt.Errorf("requeue invalid outbox event: %w", err)
	}

	tenantKey := tenantKeyFromContext(ctx)
	dispatcher.resolvedLogger().Log(ctx, obs.LevelInfo, "outbox event requeued from INVALID", "event_id", id.String())

	if counter := dispatcher.metrics.eventsRequeued; counter != nil {
		if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
			counter = counter.WithAttributes(attr)
		}

		if err := counter.AddOne(ctx); err != nil {
			dispatcher.logMetricError(ctx, "record outbox.events.requeued", err)
		}
	}

	return nil
}
