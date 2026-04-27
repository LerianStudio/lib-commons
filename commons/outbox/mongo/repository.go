package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	libMongo "github.com/LerianStudio/lib-commons/v5/commons/mongo"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	mongooptions "go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	defaultCollectionName = "outbox_events"
	defaultTenantField    = "tenant_id"
	defaultScopeTenantID  = ""
	maxListScanMultiplier = 4
)

var (
	ErrConnectionRequired        = errors.New("mongo connection is required")
	ErrRepositoryNotInitialized  = errors.New("outbox repository not initialized")
	ErrStateTransitionConflict   = errors.New("outbox event state transition conflict")
	ErrLimitMustBePositive       = errors.New("limit must be greater than zero")
	ErrIDRequired                = errors.New("id is required")
	ErrAggregateIDRequired       = errors.New("aggregate id is required")
	ErrMaxAttemptsMustBePositive = errors.New("maxAttempts must be greater than zero")
	ErrEventTypeRequired         = errors.New("event type is required")
	ErrTransactionUnsupported    = errors.New("mongo outbox repository does not support sql transactions")
	ErrInvalidIdentifier         = errors.New("invalid mongo identifier")
	ErrCollectionRequired        = errors.New("collection name is required")
	identifierPattern            = regexpIdentifier()
)

type Option func(*Repository)

func WithLogger(logger libLog.Logger) Option {
	return func(repo *Repository) {
		if logger != nil {
			repo.logger = logger
		}
	}
}

func WithCollectionName(collectionName string) Option {
	return func(repo *Repository) {
		repo.collectionName = collectionName
	}
}

func WithTenantField(field string) Option {
	return func(repo *Repository) {
		repo.tenantField = field
	}
}

// WithAllowEmptyTenant permits repository operations without tenant context.
// Documents written without tenant context are stored under the default scope,
// represented by an empty tenant field value.
func WithAllowEmptyTenant() Option {
	return func(repo *Repository) {
		repo.requireTenant = false
	}
}

// WithRequireTenant enforces tenant context on every repository operation.
// This is the default behavior when the tenant field is enabled.
func WithRequireTenant() Option {
	return func(repo *Repository) {
		repo.requireTenant = true
	}
}

type Repository struct {
	client         *libMongo.Client
	collectionName string
	tenantField    string
	requireTenant  bool
	logger         libLog.Logger
	tracer         trace.Tracer
}

type document struct {
	ID          string
	EventType   string
	AggregateID string
	Payload     string
	Status      string
	Attempts    int
	PublishedAt *time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	TenantID    string
}

func NewRepository(client *libMongo.Client, opts ...Option) (*Repository, error) {
	if client == nil {
		return nil, ErrConnectionRequired
	}

	repo := &Repository{
		client:         client,
		collectionName: defaultCollectionName,
		tenantField:    defaultTenantField,
		requireTenant:  true,
		logger:         libLog.NewNop(),
		tracer:         noop.NewTracerProvider().Tracer("outbox.mongo"),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(repo)
		}
	}

	repo.collectionName = strings.TrimSpace(repo.collectionName)
	repo.tenantField = strings.TrimSpace(repo.tenantField)

	if repo.collectionName == "" {
		return nil, ErrCollectionRequired
	}

	if !identifierPattern.MatchString(repo.collectionName) {
		return nil, ErrInvalidIdentifier
	}

	if repo.tenantField != "" && !identifierPattern.MatchString(repo.tenantField) {
		return nil, ErrInvalidIdentifier
	}

	if repo.tenantField == "" {
		repo.requireTenant = false
	}

	if err := repo.ensureIndexes(context.Background()); err != nil {
		return nil, err
	}

	return repo, nil
}

func (repo *Repository) Create(ctx context.Context, event *outbox.OutboxEvent) (*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if err := validateCreateEvent(event); err != nil {
		return nil, err
	}

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.create_outbox_event")
	defer span.End()

	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return nil, err
	}

	values := normalizedCreateValues(event, time.Now().UTC())
	doc := documentFromCreateValues(values, tenantID)

	collection, err := repo.collection(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to resolve mongo collection", err)
		return nil, err
	}

	if _, err := collection.InsertOne(ctx, doc.toBSON(repo.tenantField)); err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to create outbox event", err)
		logSanitizedError(logger, ctx, "failed to create outbox event", err)

		return nil, fmt.Errorf("creating outbox event: %w", err)
	}

	return doc.toOutboxEvent()
}

func (repo *Repository) CreateWithTx(ctx context.Context, tx outbox.Tx, event *outbox.OutboxEvent) (*outbox.OutboxEvent, error) {
	if tx != nil {
		return nil, ErrTransactionUnsupported
	}

	return repo.Create(ctx, event)
}

func (repo *Repository) ListPending(ctx context.Context, limit int) ([]*outbox.OutboxEvent, error) {
	return repo.claimPending(ctx, limit, "", "mongo.list_outbox_pending")
}

func (repo *Repository) ListPendingByType(ctx context.Context, eventType string, limit int) ([]*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if limit <= 0 {
		return nil, ErrLimitMustBePositive
	}

	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return nil, ErrEventTypeRequired
	}

	return repo.claimPending(ctx, limit, eventType, "mongo.list_outbox_pending_by_type")
}

func (repo *Repository) ListTenants(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if repo.tenantField == "" {
		return []string{}, nil
	}

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.list_outbox_tenants")
	defer span.End()

	collection, err := repo.collection(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to resolve mongo collection", err)
		return nil, err
	}

	values, err := collection.Distinct(ctx, repo.tenantField, bson.M{
		"status":         bson.M{"$in": bson.A{outbox.OutboxStatusPending, outbox.OutboxStatusFailed, outbox.OutboxStatusProcessing}},
		repo.tenantField: bson.M{"$ne": defaultScopeTenantID},
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list tenants", err)
		logSanitizedError(logger, ctx, "failed to list tenants", err)

		return nil, fmt.Errorf("listing tenants: %w", err)
	}

	tenants := make([]string, 0, len(values))
	for _, value := range values {
		tenantID, ok := value.(string)
		if !ok {
			continue
		}

		tenantID = strings.TrimSpace(tenantID)
		if tenantID == "" {
			continue
		}

		tenants = append(tenants, tenantID)
	}

	sort.Strings(tenants)

	return tenants, nil
}

func (repo *Repository) GetByID(ctx context.Context, id uuid.UUID) (*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if id == uuid.Nil {
		return nil, ErrIDRequired
	}

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.get_outbox_by_id")
	defer span.End()

	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return nil, err
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to resolve mongo collection", err)
		return nil, err
	}

	var raw bson.M
	if err := collection.FindOne(ctx, repo.idTenantFilter(id, tenantID)).Decode(&raw); err != nil {
		if !errors.Is(err, mongodriver.ErrNoDocuments) {
			libOpentelemetry.HandleSpanError(span, "failed to get outbox event", err)
			logSanitizedError(logger, ctx, "failed to get outbox event", err)
		}

		return nil, fmt.Errorf("getting outbox event: %w", err)
	}

	doc, err := documentFromBSON(raw, repo.tenantField)
	if err != nil {
		return nil, fmt.Errorf("decode outbox event: %w", err)
	}

	return doc.toOutboxEvent()
}

func (repo *Repository) MarkPublished(ctx context.Context, id uuid.UUID, publishedAt time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return ErrRepositoryNotInitialized
	}

	if err := outbox.ValidateOutboxTransition(outbox.OutboxStatusProcessing, outbox.OutboxStatusPublished); err != nil {
		return fmt.Errorf("mark published transition: %w", err)
	}

	if id == uuid.Nil {
		return ErrIDRequired
	}

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.mark_outbox_published")
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
		mergeFilters(repo.idTenantFilter(id, tenantID), bson.M{"status": outbox.OutboxStatusProcessing}),
		bson.M{"$set": bson.M{
			"status":       outbox.OutboxStatusPublished,
			"published_at": publishedAt,
			"updated_at":   time.Now().UTC(),
		}},
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox published", err)
		logSanitizedError(logger, ctx, "failed to mark outbox published", err)

		return fmt.Errorf("marking published: %w", err)
	}

	if result.ModifiedCount == 0 {
		return ErrStateTransitionConflict
	}

	return nil
}

func (repo *Repository) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string, maxAttempts int) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return ErrRepositoryNotInitialized
	}

	if err := outbox.ValidateOutboxTransition(outbox.OutboxStatusProcessing, outbox.OutboxStatusFailed); err != nil {
		return fmt.Errorf("mark failed transition: %w", err)
	}

	if err := outbox.ValidateOutboxTransition(outbox.OutboxStatusProcessing, outbox.OutboxStatusInvalid); err != nil {
		return fmt.Errorf("mark failed->invalid transition: %w", err)
	}

	if id == uuid.Nil {
		return ErrIDRequired
	}

	if maxAttempts <= 0 {
		return ErrMaxAttemptsMustBePositive
	}

	errMsg = outbox.SanitizeErrorMessageForStorage(errMsg)

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.mark_outbox_failed")
	defer span.End()

	updatedDoc, err := repo.getByIDAndStatus(ctx, id, outbox.OutboxStatusProcessing)
	if err != nil {
		if errors.Is(err, mongodriver.ErrNoDocuments) {
			return ErrStateTransitionConflict
		}

		libOpentelemetry.HandleSpanError(span, "failed to load outbox event", err)
		logSanitizedError(logger, ctx, "failed to load outbox event", err)

		return fmt.Errorf("loading outbox event: %w", err)
	}

	nextAttempts := updatedDoc.Attempts + 1
	nextStatus := outbox.OutboxStatusFailed
	nextLastError := errMsg

	if nextAttempts >= maxAttempts {
		nextStatus = outbox.OutboxStatusInvalid
		nextLastError = "max dispatch attempts exceeded"
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to resolve mongo collection", err)
		return err
	}

	result, err := collection.UpdateOne(ctx,
		mergeFilters(repo.idTenantFilter(id, updatedDoc.TenantID), bson.M{
			"status":   outbox.OutboxStatusProcessing,
			"attempts": updatedDoc.Attempts,
		}),
		bson.M{"$set": bson.M{
			"status":     nextStatus,
			"attempts":   nextAttempts,
			"last_error": nextLastError,
			"updated_at": time.Now().UTC(),
		}},
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox failed", err)
		logSanitizedError(logger, ctx, "failed to mark outbox failed", err)

		return fmt.Errorf("marking failed: %w", err)
	}

	if result.ModifiedCount == 0 {
		return ErrStateTransitionConflict
	}

	return nil
}

func (repo *Repository) ListFailedForRetry(ctx context.Context, limit int, failedBefore time.Time, maxAttempts int) ([]*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if limit <= 0 {
		return nil, ErrLimitMustBePositive
	}

	if maxAttempts <= 0 {
		return nil, ErrMaxAttemptsMustBePositive
	}

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.list_failed_for_retry")
	defer span.End()

	docs, err := repo.findCandidates(ctx, bson.M{
		"status":     outbox.OutboxStatusFailed,
		"attempts":   bson.M{"$lt": maxAttempts},
		"updated_at": bson.M{"$lte": failedBefore},
	}, bson.D{{Key: "updated_at", Value: 1}, {Key: "id", Value: 1}}, limit)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list failed events for retry", err)
		logSanitizedError(logger, ctx, "failed to list failed events for retry", err)

		return nil, fmt.Errorf("listing failed events for retry: %w", err)
	}

	return docsToEvents(docs)
}

func (repo *Repository) ResetForRetry(ctx context.Context, limit int, failedBefore time.Time, maxAttempts int) ([]*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if limit <= 0 {
		return nil, ErrLimitMustBePositive
	}

	if maxAttempts <= 0 {
		return nil, ErrMaxAttemptsMustBePositive
	}

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.reset_for_retry")
	defer span.End()

	candidates, err := repo.findCandidates(ctx, bson.M{
		"status":     outbox.OutboxStatusFailed,
		"attempts":   bson.M{"$lt": maxAttempts},
		"updated_at": bson.M{"$lte": failedBefore},
	}, bson.D{{Key: "updated_at", Value: 1}, {Key: "id", Value: 1}}, limit)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to reset events for retry", err)
		logSanitizedError(logger, ctx, "failed to reset events for retry", err)

		return nil, fmt.Errorf("resetting events for retry: %w", err)
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	claimed := make([]document, 0, len(candidates))
	for _, candidate := range candidates {
		result, updateErr := collection.UpdateOne(ctx,
			mergeFilters(repo.idTenantFilter(parsedUUID(candidate.ID), candidate.TenantID), bson.M{
				"status":     outbox.OutboxStatusFailed,
				"attempts":   candidate.Attempts,
				"updated_at": candidate.UpdatedAt,
			}),
			bson.M{"$set": bson.M{"status": outbox.OutboxStatusProcessing, "updated_at": now}},
		)
		if updateErr != nil {
			libOpentelemetry.HandleSpanError(span, "failed to update retry candidate", updateErr)
			logSanitizedError(logger, ctx, "failed to update retry candidate", updateErr)

			return nil, fmt.Errorf("resetting events for retry: %w", updateErr)
		}

		if result.ModifiedCount == 0 {
			continue
		}

		candidate.Status = outbox.OutboxStatusProcessing
		candidate.UpdatedAt = now
		claimed = append(claimed, candidate)
	}

	return docsToEvents(claimed)
}

func (repo *Repository) ResetStuckProcessing(ctx context.Context, limit int, processingBefore time.Time, maxAttempts int) ([]*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if limit <= 0 {
		return nil, ErrLimitMustBePositive
	}

	if maxAttempts <= 0 {
		return nil, ErrMaxAttemptsMustBePositive
	}

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.reset_outbox_processing")
	defer span.End()

	candidates, err := repo.findCandidates(ctx, bson.M{
		"status":     outbox.OutboxStatusProcessing,
		"updated_at": bson.M{"$lte": processingBefore},
	}, bson.D{{Key: "updated_at", Value: 1}, {Key: "id", Value: 1}}, limit)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to reset stuck events", err)
		logSanitizedError(logger, ctx, "failed to reset stuck events", err)

		return nil, fmt.Errorf("reset stuck events: %w", err)
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	retryDocs := make([]document, 0, len(candidates))
	for _, candidate := range candidates {
		nextAttempts := candidate.Attempts + 1
		nextStatus := outbox.OutboxStatusProcessing
		nextLastError := candidate.LastError

		if nextAttempts >= maxAttempts {
			nextStatus = outbox.OutboxStatusInvalid
			nextLastError = "max dispatch attempts exceeded"
		}

		result, updateErr := collection.UpdateOne(ctx,
			mergeFilters(repo.idTenantFilter(parsedUUID(candidate.ID), candidate.TenantID), bson.M{
				"status":     outbox.OutboxStatusProcessing,
				"attempts":   candidate.Attempts,
				"updated_at": candidate.UpdatedAt,
			}),
			bson.M{"$set": bson.M{
				"status":     nextStatus,
				"attempts":   nextAttempts,
				"last_error": nextLastError,
				"updated_at": now,
			}},
		)
		if updateErr != nil {
			libOpentelemetry.HandleSpanError(span, "failed to update stuck candidate", updateErr)
			logSanitizedError(logger, ctx, "failed to update stuck candidate", updateErr)

			return nil, fmt.Errorf("reset stuck events: %w", updateErr)
		}

		if result.ModifiedCount == 0 {
			continue
		}

		if nextStatus == outbox.OutboxStatusInvalid {
			continue
		}

		candidate.Attempts = nextAttempts
		candidate.Status = nextStatus
		candidate.UpdatedAt = now
		retryDocs = append(retryDocs, candidate)
	}

	return docsToEvents(retryDocs)
}

func (repo *Repository) MarkInvalid(ctx context.Context, id uuid.UUID, errMsg string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return ErrRepositoryNotInitialized
	}

	if err := outbox.ValidateOutboxTransition(outbox.OutboxStatusProcessing, outbox.OutboxStatusInvalid); err != nil {
		return fmt.Errorf("mark invalid transition: %w", err)
	}

	if id == uuid.Nil {
		return ErrIDRequired
	}

	errMsg = outbox.SanitizeErrorMessageForStorage(errMsg)

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.mark_outbox_invalid")
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
		mergeFilters(repo.idTenantFilter(id, tenantID), bson.M{"status": outbox.OutboxStatusProcessing}),
		bson.M{"$set": bson.M{"status": outbox.OutboxStatusInvalid, "last_error": errMsg, "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox invalid", err)
		logSanitizedError(logger, ctx, "failed to mark outbox invalid", err)

		return fmt.Errorf("marking invalid: %w", err)
	}

	if result.ModifiedCount == 0 {
		return ErrStateTransitionConflict
	}

	return nil
}

func (repo *Repository) RequiresTenant() bool {
	if repo == nil {
		return true
	}

	return repo.requireTenant
}

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

	logger, tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, spanName)
	defer span.End()

	filter := bson.M{"status": outbox.OutboxStatusPending}
	if eventType != "" {
		filter["event_type"] = eventType
	}

	candidates, err := repo.findCandidates(ctx, filter, bson.D{{Key: "created_at", Value: 1}, {Key: "id", Value: 1}}, limit)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list outbox events", err)
		logSanitizedError(logger, ctx, "failed to list outbox events", err)

		return nil, fmt.Errorf("listing pending events: %w", err)
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	claimed := make([]document, 0, len(candidates))
	for _, candidate := range candidates {
		updateFilter := mergeFilters(repo.idTenantFilter(parsedUUID(candidate.ID), candidate.TenantID), bson.M{
			"status":     outbox.OutboxStatusPending,
			"attempts":   candidate.Attempts,
			"updated_at": candidate.UpdatedAt,
		})

		result, updateErr := collection.UpdateOne(ctx, updateFilter, bson.M{"$set": bson.M{"status": outbox.OutboxStatusProcessing, "updated_at": now}})
		if updateErr != nil {
			libOpentelemetry.HandleSpanError(span, "failed to claim outbox event", updateErr)
			logSanitizedError(logger, ctx, "failed to claim outbox event", updateErr)

			return nil, fmt.Errorf("listing pending events: %w", updateErr)
		}

		if result.ModifiedCount == 0 {
			continue
		}

		candidate.Status = outbox.OutboxStatusProcessing
		candidate.UpdatedAt = now
		claimed = append(claimed, candidate)
	}

	return docsToEvents(claimed)
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

	findLimit := int64(limit)
	if findLimit < int64(maxListScanMultiplier) {
		findLimit *= maxListScanMultiplier
	}

	cursor, err := collection.Find(ctx, filter, mongooptions.Find().SetSort(sortSpec).SetLimit(findLimit))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	docs := make([]document, 0, limit)

	for cursor.Next(ctx) {
		var raw bson.M
		if err := cursor.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode outbox event: %w", err)
		}

		doc, err := documentFromBSON(raw, repo.tenantField)
		if err != nil {
			return nil, fmt.Errorf("decode outbox event: %w", err)
		}

		docs = append(docs, doc)
		if len(docs) >= limit {
			break
		}
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	return docs, nil
}

func (repo *Repository) getByIDAndStatus(ctx context.Context, id uuid.UUID, status string) (document, error) {
	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return document{}, err
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return document{}, err
	}

	var raw bson.M

	filter := mergeFilters(repo.idTenantFilter(id, tenantID), bson.M{"status": status})
	if err := collection.FindOne(ctx, filter).Decode(&raw); err != nil {
		return document{}, err
	}

	return documentFromBSON(raw, repo.tenantField)
}

func (repo *Repository) initialized() bool {
	return repo != nil && repo.client != nil && !isNilPointer(repo.client)
}

func (repo *Repository) collection(ctx context.Context) (*mongodriver.Collection, error) {
	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	database, err := repo.client.Database(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve mongo database: %w", err)
	}

	return database.Collection(repo.collectionName), nil
}

func (repo *Repository) ensureIndexes(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	indexes := buildIndexes(repo.collectionName, repo.tenantField)
	if len(indexes) == 0 {
		return nil
	}

	if err := repo.client.EnsureIndexes(ctx, repo.collectionName, indexes...); err != nil {
		return fmt.Errorf("ensure outbox indexes: %w", err)
	}

	return nil
}

func (repo *Repository) tenantIDFromContext(ctx context.Context) (string, error) {
	tenantID, ok := outbox.TenantIDFromContext(ctx)
	if repo.requireTenant && (!ok || tenantID == "") {
		return "", outbox.ErrTenantIDRequired
	}

	if !ok {
		return defaultScopeTenantID, nil
	}

	return tenantID, nil
}

func (repo *Repository) tenantMatchFilter(tenantID string) bson.M {
	if repo.tenantField == "" {
		return bson.M{}
	}

	return bson.M{repo.tenantField: tenantID}
}

func (repo *Repository) idTenantFilter(id uuid.UUID, tenantID string) bson.M {
	filter := bson.M{"id": id.String()}
	maps.Copy(filter, repo.tenantMatchFilter(tenantID))

	return filter
}

func (repo *Repository) tracking(ctx context.Context) (libLog.Logger, trace.Tracer) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	if logger == nil {
		logger = repo.logger
	}

	if logger == nil {
		logger = libLog.NewNop()
	}

	if tracer == nil {
		tracer = repo.tracer
	}

	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("outbox.mongo")
	}

	return logger, tracer
}

func buildIndexes(collectionName, tenantField string) []mongodriver.IndexModel {
	_ = collectionName

	keysWithTenant := func(keys bson.D) bson.D {
		if tenantField == "" {
			return keys
		}

		prefixed := make(bson.D, 0, len(keys)+1)
		prefixed = append(prefixed, bson.E{Key: tenantField, Value: 1})
		prefixed = append(prefixed, keys...)

		return prefixed
	}

	uniqueIDKeys := bson.D{{Key: "id", Value: 1}}
	if tenantField != "" {
		uniqueIDKeys = keysWithTenant(bson.D{{Key: "id", Value: 1}})
	}

	indexes := []mongodriver.IndexModel{
		{
			Keys:    uniqueIDKeys,
			Options: mongooptions.Index().SetUnique(true).SetName("outbox_id_scope_unique"),
		},
		{
			Keys:    keysWithTenant(bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: 1}, {Key: "id", Value: 1}}),
			Options: mongooptions.Index().SetName("outbox_pending_claim"),
		},
		{
			Keys:    keysWithTenant(bson.D{{Key: "event_type", Value: 1}, {Key: "status", Value: 1}, {Key: "created_at", Value: 1}, {Key: "id", Value: 1}}),
			Options: mongooptions.Index().SetName("outbox_pending_by_type_claim"),
		},
		{
			Keys:    keysWithTenant(bson.D{{Key: "status", Value: 1}, {Key: "updated_at", Value: 1}, {Key: "id", Value: 1}}),
			Options: mongooptions.Index().SetName("outbox_state_updated_scan"),
		},
	}

	if tenantField != "" {
		indexes = append(indexes, mongodriver.IndexModel{
			Keys:    bson.D{{Key: tenantField, Value: 1}, {Key: "status", Value: 1}},
			Options: mongooptions.Index().SetName("outbox_tenant_status"),
		})
	}

	return indexes
}

func (doc document) toBSON(tenantField string) bson.M {
	raw := bson.M{
		"id":           doc.ID,
		"event_type":   doc.EventType,
		"aggregate_id": doc.AggregateID,
		"payload":      doc.Payload,
		"status":       doc.Status,
		"attempts":     doc.Attempts,
		"created_at":   doc.CreatedAt,
		"updated_at":   doc.UpdatedAt,
	}

	if doc.PublishedAt != nil {
		raw["published_at"] = *doc.PublishedAt
	}

	if doc.LastError != "" {
		raw["last_error"] = doc.LastError
	}

	if tenantField != "" {
		raw[tenantField] = doc.TenantID
	}

	return raw
}

func documentFromBSON(raw bson.M, tenantField string) (document, error) {
	id, err := stringField(raw, "id")
	if err != nil {
		return document{}, err
	}

	eventType, err := stringField(raw, "event_type")
	if err != nil {
		return document{}, err
	}

	aggregateID, err := stringField(raw, "aggregate_id")
	if err != nil {
		return document{}, err
	}

	payload, err := stringField(raw, "payload")
	if err != nil {
		return document{}, err
	}

	status, err := stringField(raw, "status")
	if err != nil {
		return document{}, err
	}

	attempts, err := intField(raw, "attempts")
	if err != nil {
		return document{}, err
	}

	createdAt, err := timeField(raw, "created_at")
	if err != nil {
		return document{}, err
	}

	updatedAt, err := timeField(raw, "updated_at")
	if err != nil {
		return document{}, err
	}

	lastError, err := optionalStringField(raw, "last_error")
	if err != nil {
		return document{}, err
	}

	publishedAt, err := optionalTimeField(raw, "published_at")
	if err != nil {
		return document{}, err
	}

	tenantID := defaultScopeTenantID
	if tenantField != "" {
		tenantID, err = optionalStringField(raw, tenantField)
		if err != nil {
			return document{}, err
		}
	}

	return document{
		ID:          id,
		EventType:   eventType,
		AggregateID: aggregateID,
		Payload:     payload,
		Status:      status,
		Attempts:    attempts,
		PublishedAt: publishedAt,
		LastError:   strings.TrimSpace(lastError),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		TenantID:    strings.TrimSpace(tenantID),
	}, nil
}

func docsToEvents(docs []document) ([]*outbox.OutboxEvent, error) {
	result := make([]*outbox.OutboxEvent, 0, len(docs))
	for _, doc := range docs {
		event, err := doc.toOutboxEvent()
		if err != nil {
			return nil, err
		}

		result = append(result, event)
	}

	return result, nil
}

func (doc document) toOutboxEvent() (*outbox.OutboxEvent, error) {
	id, err := uuid.Parse(doc.ID)
	if err != nil {
		return nil, fmt.Errorf("parse outbox event id: %w", err)
	}

	aggregateID, err := uuid.Parse(doc.AggregateID)
	if err != nil {
		return nil, fmt.Errorf("parse outbox aggregate id: %w", err)
	}

	return &outbox.OutboxEvent{
		ID:          id,
		EventType:   doc.EventType,
		AggregateID: aggregateID,
		Payload:     []byte(doc.Payload),
		Status:      doc.Status,
		Attempts:    doc.Attempts,
		PublishedAt: doc.PublishedAt,
		LastError:   doc.LastError,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}, nil
}

func stringField(raw bson.M, key string) (string, error) {
	value, ok := raw[key]
	if !ok {
		return "", fmt.Errorf("missing field %q", key)
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("field %q must be string", key)
	}

	return str, nil
}

func optionalStringField(raw bson.M, key string) (string, error) {
	value, ok := raw[key]
	if !ok || value == nil {
		return "", nil
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("field %q must be string", key)
	}

	return str, nil
}

func intField(raw bson.M, key string) (int, error) {
	value, ok := raw[key]
	if !ok {
		return 0, fmt.Errorf("missing field %q", key)
	}

	switch typed := value.(type) {
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float64:
		return int(typed), nil
	default:
		return 0, fmt.Errorf("field %q must be integer", key)
	}
}

func timeField(raw bson.M, key string) (time.Time, error) {
	value, ok := raw[key]
	if !ok {
		return time.Time{}, fmt.Errorf("missing field %q", key)
	}

	switch typed := value.(type) {
	case time.Time:
		return typed, nil
	case primitive.DateTime:
		return typed.Time(), nil
	default:
		return time.Time{}, fmt.Errorf("field %q must be time", key)
	}
}

func optionalTimeField(raw bson.M, key string) (*time.Time, error) {
	value, ok := raw[key]
	if !ok || value == nil {
		return nil, nil //nolint:nilnil // nil pointer represents an absent optional timestamp.
	}

	switch typed := value.(type) {
	case time.Time:
		return &typed, nil
	case primitive.DateTime:
		timeValue := typed.Time()
		return &timeValue, nil
	default:
		return nil, fmt.Errorf("field %q must be time", key)
	}
}

type createValues struct {
	id          uuid.UUID
	eventType   string
	aggregateID uuid.UUID
	payload     []byte
	status      string
	attempts    int
	publishedAt *time.Time
	lastError   string
	createdAt   time.Time
	updatedAt   time.Time
}

func documentFromCreateValues(values createValues, tenantID string) document {
	return document{
		ID:          values.id.String(),
		EventType:   values.eventType,
		AggregateID: values.aggregateID.String(),
		Payload:     string(values.payload),
		Status:      values.status,
		Attempts:    values.attempts,
		PublishedAt: values.publishedAt,
		LastError:   values.lastError,
		CreatedAt:   values.createdAt,
		UpdatedAt:   values.updatedAt,
		TenantID:    tenantID,
	}
}

func normalizedCreateValues(event *outbox.OutboxEvent, now time.Time) createValues {
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	updatedAt := event.UpdatedAt
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		updatedAt = createdAt
	}

	return createValues{
		id:          event.ID,
		eventType:   strings.TrimSpace(event.EventType),
		aggregateID: event.AggregateID,
		payload:     event.Payload,
		status:      outbox.OutboxStatusPending,
		attempts:    0,
		publishedAt: nil,
		lastError:   "",
		createdAt:   createdAt,
		updatedAt:   updatedAt,
	}
}

func validateCreateEvent(event *outbox.OutboxEvent) error {
	if event == nil {
		return outbox.ErrOutboxEventRequired
	}

	if event.ID == uuid.Nil {
		return ErrIDRequired
	}

	if strings.TrimSpace(event.EventType) == "" {
		return ErrEventTypeRequired
	}

	if event.AggregateID == uuid.Nil {
		return ErrAggregateIDRequired
	}

	if len(event.Payload) == 0 {
		return outbox.ErrOutboxEventPayloadRequired
	}

	if len(event.Payload) > outbox.DefaultMaxPayloadBytes {
		return outbox.ErrOutboxEventPayloadTooLarge
	}

	if !json.Valid(event.Payload) {
		return outbox.ErrOutboxEventPayloadNotJSON
	}

	return nil
}

func mergeFilters(base bson.M, extras ...bson.M) bson.M {
	merged := make(bson.M, len(base)+len(extras)*2)
	maps.Copy(merged, base)

	for _, extra := range extras {
		maps.Copy(merged, extra)
	}

	return merged
}

func parsedUUID(raw string) uuid.UUID {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}

	return parsed
}

func logSanitizedError(logger libLog.Logger, ctx context.Context, message string, err error) {
	if logger == nil || err == nil {
		return
	}

	logger.Log(ctx, libLog.LevelError, message, libLog.String("error", outbox.SanitizeErrorMessageForStorage(err.Error())))
}

func regexpIdentifier() *regexp.Regexp {
	return regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
}

func isNilPointer(value any) bool {
	if value == nil {
		return true
	}

	rv := reflect.ValueOf(value)

	return rv.Kind() == reflect.Pointer && rv.IsNil()
}
