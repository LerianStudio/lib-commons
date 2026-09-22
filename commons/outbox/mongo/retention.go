package mongo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongooptions "go.mongodb.org/mongo-driver/v2/mongo/options"
)

var _ outbox.PublishedPurger = (*Repository)(nil)

// DeletePublishedBefore deletes at most limit PUBLISHED events created before
// the cutoff, oldest first, skipping event types listed in keepEventTypes, and
// returns how many documents were deleted.
//
// The age bound reads created_at rather than published_at: the status and
// created_at index serves it, and a PUBLISHED event's published_at is never
// earlier than its created_at, so the bound only ever keeps an event longer.
// PENDING, PROCESSING, FAILED and INVALID events are never selected. A
// limit <= 0 deletes nothing, so a sweep is always bounded.
func (repo *Repository) DeletePublishedBefore(
	ctx context.Context,
	before time.Time,
	keepEventTypes []string,
	limit int,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return 0, ErrRepositoryNotInitialized
	}

	if limit <= 0 {
		return 0, nil
	}

	tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.delete_published_outbox_events")
	defer span.End()

	deleted, err := repo.deletePublishedBefore(ctx, before, keepEventTypes, limit)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to delete published outbox events", err)

		return 0, fmt.Errorf("deleting published events: %w", err)
	}

	return deleted, nil
}

func (repo *Repository) deletePublishedBefore(
	ctx context.Context,
	before time.Time,
	keepEventTypes []string,
	limit int,
) (int64, error) {
	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return 0, err
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return 0, err
	}

	filter := mergeFilters(publishedBeforeFilter(before, keepEventTypes), repo.tenantMatchFilter(tenantID))

	cursor, err := collection.Find(ctx, filter, mongooptions.Find().
		SetSort(bson.D{{Key: mongoFieldCreatedAt, Value: 1}, {Key: "id", Value: 1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{"id": 1}))
	if err != nil {
		return 0, fmt.Errorf("finding published events: %w", err)
	}

	var selected []struct {
		ID string `bson:"id"`
	}

	if err := cursor.All(ctx, &selected); err != nil {
		return 0, fmt.Errorf("decoding published events: %w", err)
	}

	if len(selected) == 0 {
		return 0, nil
	}

	ids := make([]string, 0, len(selected))
	for _, doc := range selected {
		ids = append(ids, doc.ID)
	}

	// Re-assert the status and tenant so a delete can never reach a document the
	// selection did not qualify.
	result, err := collection.DeleteMany(ctx, mergeFilters(filter, bson.M{"id": bson.M{mongoOperatorIn: ids}}))
	if err != nil {
		return 0, fmt.Errorf("deleting published events: %w", err)
	}

	return result.DeletedCount, nil
}

func publishedBeforeFilter(before time.Time, keepEventTypes []string) bson.M {
	filter := bson.M{
		mongoFieldStatus:    outbox.OutboxStatusPublished,
		mongoFieldCreatedAt: bson.M{mongoOperatorLT: before},
	}

	keep := make([]string, 0, len(keepEventTypes))

	for _, eventType := range keepEventTypes {
		if trimmed := strings.TrimSpace(eventType); trimmed != "" {
			keep = append(keep, trimmed)
		}
	}

	if len(keep) > 0 {
		filter["event_type"] = bson.M{"$nin": keep}
	}

	return filter
}
