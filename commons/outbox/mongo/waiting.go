package mongo

import (
	"context"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var _ outbox.InvalidRequeuer = (*Repository)(nil)

// RequeueInvalid implements outbox.InvalidRequeuer.
func (repo *Repository) RequeueInvalid(ctx context.Context, id uuid.UUID) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return ErrRepositoryNotInitialized
	}

	if id == uuid.Nil {
		return ErrIDRequired
	}

	ctx, span := repo.tracking(ctx).Start(ctx, "mongo.requeue_outbox_invalid")
	defer span.End()

	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return err
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to resolve mongo collection", err)
		return err
	}

	result, err := collection.UpdateOne(ctx,
		mergeFilters(repo.idTenantFilter(id, tenantID), bson.M{mongoFieldStatus: outbox.OutboxStatusInvalid}),
		bson.M{
			mongoUpdateSet:   bson.M{mongoFieldStatus: outbox.OutboxStatusPending, mongoFieldAttempts: 0, mongoFieldUpdatedAt: time.Now().UTC()},
			mongoUpdateUnset: bson.M{mongoFieldLastError: "", mongoFieldClaimToken: ""},
		},
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to requeue invalid outbox event", err)

		return fmt.Errorf("requeueing invalid event: %w", err)
	}

	if result.ModifiedCount == 0 {
		return fmt.Errorf("requeueing invalid event: %w", outbox.ErrOutboxEventNotInvalid)
	}

	return nil
}
