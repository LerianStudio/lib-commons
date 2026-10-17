package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	mongooptions "go.mongodb.org/mongo-driver/v2/mongo/options"
)

var (
	_ outbox.InvalidRequeuer     = (*Repository)(nil)
	_ outbox.OldestWaitingReader = (*Repository)(nil)
)

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

// OldestWaitingCreatedAt implements outbox.OldestWaitingReader.
func (repo *Repository) OldestWaitingCreatedAt(ctx context.Context) (time.Time, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return time.Time{}, false, ErrRepositoryNotInitialized
	}

	ctx, span := repo.tracking(ctx).Start(ctx, "mongo.oldest_waiting_outbox_event")
	defer span.End()

	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return time.Time{}, false, err
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to resolve mongo collection", err)
		return time.Time{}, false, err
	}

	filter := mergeFilters(bson.M{mongoFieldStatus: bson.M{mongoOperatorIn: []string{
		outbox.OutboxStatusPending, outbox.OutboxStatusProcessing, outbox.OutboxStatusFailed,
	}}}, repo.tenantMatchFilter(tenantID))

	var oldest struct {
		CreatedAt time.Time `bson:"created_at"`
	}

	err = collection.FindOne(ctx, filter, mongooptions.FindOne().
		SetSort(bson.D{{Key: mongoFieldCreatedAt, Value: 1}}).
		SetProjection(bson.M{mongoFieldCreatedAt: 1})).Decode(&oldest)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return time.Time{}, false, nil
	}

	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to read oldest waiting outbox event", err)

		return time.Time{}, false, fmt.Errorf("reading oldest waiting event: %w", err)
	}

	return oldest.CreatedAt, true, nil
}
