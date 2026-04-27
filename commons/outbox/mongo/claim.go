package mongo

import (
	"context"
	"fmt"
	"time"

	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	mongooptions "go.mongodb.org/mongo-driver/mongo/options"
)

func (repo *Repository) claimPending(ctx context.Context, limit int, eventType string, spanName string) ([]*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if limit <= 0 {
		return nil, ErrLimitMustBePositive
	}

	tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, spanName)
	defer span.End()

	filter := bson.M{"status": outbox.OutboxStatusPending}
	if eventType != "" {
		filter["event_type"] = eventType
	}

	claimed, err := repo.claimMatching(ctx, filter, bson.D{{Key: "created_at", Value: 1}, {Key: "id", Value: 1}}, limit, outbox.OutboxStatusPending, outbox.OutboxStatusProcessing, func(_ document) bson.M {
		return bson.M{"status": outbox.OutboxStatusProcessing, "updated_at": time.Now().UTC()}
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list outbox events", err)

		return nil, fmt.Errorf("listing pending events: %w", err)
	}

	return docsToEvents(claimed)
}

func (repo *Repository) claimMatching(
	ctx context.Context,
	filter bson.M,
	sortSpec bson.D,
	limit int,
	fromStatus string,
	returnStatus string,
	setForCandidate func(document) bson.M,
) ([]document, error) {
	claimed := make([]document, 0, limit)

	for attempts := 0; len(claimed) < limit && attempts < maxListScanMultiplier; attempts++ {
		remaining := limit - len(claimed)

		candidates, err := repo.findClaimCandidates(ctx, filter, sortSpec, remaining)
		if err != nil {
			return nil, err
		}

		if len(candidates) == 0 {
			break
		}

		batch, modified, claimToken, err := repo.claimDocuments(ctx, candidates, fromStatus, returnStatus, sortSpec, setForCandidate)
		if err != nil {
			return nil, err
		}

		claimed = append(claimed, batch...)
		if modified != int64(len(batch)) {
			if err := repo.rollbackMissingClaims(ctx, candidates, batch, claimToken, returnStatus, fromStatus); err != nil {
				return nil, err
			}

			continue
		}

		if fromStatus == returnStatus {
			break
		}

		if len(batch) == 0 && len(candidates) < remaining {
			break
		}
	}

	return claimed, nil
}

func (repo *Repository) rollbackMissingClaims(ctx context.Context, candidates []document, claimed []document, claimToken string, returnStatus string, fromStatus string) error {
	if claimToken == "" {
		return nil
	}

	claimedIDs := make(map[string]struct{}, len(claimed))
	for _, doc := range claimed {
		claimedIDs[doc.ID] = struct{}{}
	}

	models := make([]mongodriver.WriteModel, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := claimedIDs[candidate.ID]; ok {
			continue
		}

		models = append(models, mongodriver.NewUpdateOneModel().
			SetFilter(mergeFilters(bson.M{"id": candidate.ID, "status": returnStatus, "claim_token": claimToken}, repo.tenantMatchFilter(candidate.TenantID))).
			SetUpdate(bson.M{"$set": bson.M{"status": fromStatus, "updated_at": time.Now().UTC()}, "$unset": bson.M{"claim_token": ""}}))
	}

	if len(models) == 0 {
		return nil
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return err
	}

	if _, err := collection.BulkWrite(ctx, models, mongooptions.BulkWrite().SetOrdered(false)); err != nil {
		return fmt.Errorf("rolling back missing claims: %w", err)
	}

	return nil
}

func (repo *Repository) claimDocuments(
	ctx context.Context,
	candidates []document,
	fromStatus string,
	returnStatus string,
	sortSpec bson.D,
	setForCandidate func(document) bson.M,
) ([]document, int64, string, error) {
	if len(candidates) == 0 {
		return []document{}, 0, "", nil
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return nil, 0, "", err
	}

	claimToken := uuid.NewString()

	models := make([]mongodriver.WriteModel, 0, len(candidates))

	claimedIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		set := setForCandidate(candidate)
		if set["status"] == returnStatus {
			set["claim_token"] = claimToken

			claimedIDs = append(claimedIDs, candidate.ID)
		}

		models = append(models, mongodriver.NewUpdateOneModel().
			SetFilter(mergeFilters(bson.M{"id": candidate.ID}, repo.tenantMatchFilter(candidate.TenantID), bson.M{
				"status":     fromStatus,
				"attempts":   candidate.Attempts,
				"updated_at": candidate.UpdatedAt,
			})).
			SetUpdate(bson.M{"$set": set}))
	}

	result, err := collection.BulkWrite(ctx, models, mongooptions.BulkWrite().SetOrdered(false))
	if err != nil {
		return nil, 0, "", err
	}

	if result.ModifiedCount == 0 {
		return []document{}, 0, claimToken, nil
	}

	if len(claimedIDs) == 0 {
		return []document{}, result.ModifiedCount, claimToken, nil
	}

	claimedFilter := mergeFilters(
		bson.M{"claim_token": claimToken, "id": bson.M{"$in": claimedIDs}},
		repo.tenantMatchFilter(candidates[0].TenantID),
	)
	claimedFilter["status"] = returnStatus

	cursor, err := collection.Find(ctx, claimedFilter, mongooptions.Find().SetSort(sortSpec))
	if err != nil {
		return nil, int64(len(claimedIDs)), claimToken, err
	}

	claimed, err := repo.decodeAndCloseCursor(ctx, cursor, len(claimedIDs))

	return claimed, int64(len(claimedIDs)), claimToken, err
}

func (repo *Repository) findClaimCandidates(ctx context.Context, filter bson.M, sortSpec bson.D, limit int) ([]document, error) {
	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return nil, err
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return nil, err
	}

	filter = mergeFilters(filter, repo.tenantMatchFilter(tenantID))

	projection := bson.M{
		"id":         1,
		"status":     1,
		"attempts":   1,
		"updated_at": 1,
		"last_error": 1,
	}
	if repo.tenantField != "" {
		projection[repo.tenantField] = 1
	}

	cursor, err := collection.Find(ctx, filter, mongooptions.Find().SetSort(sortSpec).SetLimit(int64(limit)).SetProjection(projection))
	if err != nil {
		return nil, err
	}

	return repo.decodeAndCloseClaimCursor(ctx, cursor, limit)
}

func (repo *Repository) findCandidates(ctx context.Context, filter bson.M, sortSpec bson.D, limit int) ([]document, error) {
	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return nil, err
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return nil, err
	}

	filter = mergeFilters(filter, repo.tenantMatchFilter(tenantID))

	cursor, err := collection.Find(ctx, filter, mongooptions.Find().SetSort(sortSpec).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}

	return repo.decodeAndCloseCursor(ctx, cursor, limit)
}
