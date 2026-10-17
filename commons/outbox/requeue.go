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

// Requeue puts the INVALID or FAILED event id back in the queue for a later cycle, the sole
// publisher, to publish under the same id; ctx carries the tenant and the claim scope applies.
// It does not undo an OnInvalid reaction: restore any domain state OnInvalid changed first.
func (dispatcher *Dispatcher) Requeue(ctx context.Context, id uuid.UUID) error {
	if dispatcher == nil || dispatcher.repo == nil {
		return ErrOutboxDispatcherRequired
	}

	requeuer, ok := dispatcher.repo.(Requeuer)
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

	ctx, span := tracer.Start(ctx, "outbox.dispatcher.requeue")
	defer span.End()

	if err := requeuer.Requeue(dispatcher.withClaimScope(ctx), id); err != nil {
		if errors.Is(err, ErrOutboxEventNotRequeueable) {
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "outbox event is not requeueable", err)
		} else {
			libOpentelemetry.HandleSpanError(span, "failed to requeue outbox event", err)
		}

		return fmt.Errorf("requeue outbox event: %w", err)
	}

	dispatcher.resolvedLogger().Log(ctx, obs.LevelInfo, "outbox event requeued", "event_id", id.String())

	return nil
}
