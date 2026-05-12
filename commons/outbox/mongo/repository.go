package mongo

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	libMongo "github.com/LerianStudio/lib-commons/v5/commons/mongo"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	defaultCollectionName = "outbox_events"
	defaultTenantField    = "tenant_id"
	defaultScopeTenantID  = ""
	maxListScanMultiplier = 8
	defaultIndexTimeout   = 10 * time.Second
	cursorCloseTimeout    = 2 * time.Second
	bsonFieldID           = "id"
	bsonFieldEventType    = "event_type"
	bsonFieldAggregateID  = "aggregate_id"
	bsonFieldPayload      = "payload"
	bsonFieldStatus       = "status"
	bsonFieldAttempts     = "attempts"
	bsonFieldPublishedAt  = "published_at"
	bsonFieldLastError    = "last_error"
	bsonFieldCreatedAt    = "created_at"
	bsonFieldUpdatedAt    = "updated_at"
	bsonFieldClaimToken   = "claim_token"
	bsonOperatorIn        = "$in"
	bsonOperatorLT        = "$lt"
	bsonOperatorLTE       = "$lte"
	bsonOperatorNE        = "$ne"
	bsonOperatorSet       = "$set"
	bsonOperatorUnset     = "$unset"
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
	ErrTenantDatabaseRequired    = errors.New("tenant mongo database is required")
	errUseDefaultDatabase        = errors.New("use default mongo database")
	identifierPattern            = regexpIdentifier()
)

// Option customizes a MongoDB outbox repository.
type Option func(*Repository)

// TenantDatabaseResolver resolves tenant-scoped MongoDB databases for dispatcher
// contexts that only carry a tenant ID. It lets generic outbox dispatchers drain
// rows written through tenant-manager/core.ContextWithMB without requiring the
// caller to pre-install a database handle on every dispatch context.
type TenantDatabaseResolver interface {
	ListTenants(ctx context.Context, module string) ([]string, error)
	DatabaseForTenant(ctx context.Context, tenantID string, module string) (*mongodriver.Database, error)
}

// WithLogger configures the logger used for sanitized repository error logs.
func WithLogger(logger libLog.Logger) Option {
	return func(repo *Repository) {
		if nilcheck.Interface(logger) {
			return
		}

		repo.logger = logger
	}
}

// WithCollectionName configures the MongoDB collection used to store outbox events.
func WithCollectionName(collectionName string) Option {
	return func(repo *Repository) {
		repo.collectionName = collectionName
	}
}

// WithTenantField configures the BSON field used to store the tenant identifier.
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

// WithModule configures the tenant-manager module used to resolve a
// module-scoped MongoDB database from context. When set, repository operations
// fail closed if the module-specific database is absent from context.
func WithModule(module string) Option {
	return func(repo *Repository) {
		repo.tenantModule = module
	}
}

// WithTenantDatabaseResolver configures tenant database discovery for generic
// dispatcher loops. When set, ListTenants uses the resolver and collection
// resolution can derive the tenant Mongo database from context tenant ID.
func WithTenantDatabaseResolver(resolver TenantDatabaseResolver) Option {
	return func(repo *Repository) {
		if nilcheck.Interface(resolver) {
			return
		}

		repo.tenantDatabaseResolver = resolver
	}
}

// Repository persists outbox events in MongoDB.
//
// By default, operations require a tenant ID in context and store it in the
// configured tenant field. When a tenant-scoped MongoDB database is present in
// context through tenant-manager/core.ContextWithMB, that database is used;
// otherwise the constructor client database is used for row-scoped deployments.
type Repository struct {
	client                 *libMongo.Client
	collectionName         string
	tenantField            string
	requireTenant          bool
	logger                 libLog.Logger
	tracer                 trace.Tracer
	tenantModule           string
	tenantDatabaseResolver TenantDatabaseResolver
	indexMu                sync.Mutex
	indexedTenantDatabases sync.Map
}

type document struct {
	ID          string     `bson:"id"`
	EventType   string     `bson:"event_type"`
	AggregateID string     `bson:"aggregate_id"`
	Payload     string     `bson:"payload"`
	Status      string     `bson:"status"`
	Attempts    int        `bson:"attempts"`
	PublishedAt *time.Time `bson:"published_at,omitempty"`
	LastError   string     `bson:"last_error,omitempty"`
	CreatedAt   time.Time  `bson:"created_at"`
	UpdatedAt   time.Time  `bson:"updated_at"`
	TenantID    string     `bson:"tenant_id,omitempty"`
}

// NewRepository creates a MongoDB outbox repository.
//
// Indexes are ensured during construction with a bounded default timeout. Use
// NewRepositoryWithContext when caller-controlled cancellation is required.
func NewRepository(client *libMongo.Client, opts ...Option) (*Repository, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultIndexTimeout)
	defer cancel()

	return NewRepositoryWithContext(ctx, client, opts...)
}

// NewRepositoryWithContext creates a MongoDB outbox repository using ctx for
// constructor-time index creation.
func NewRepositoryWithContext(ctx context.Context, client *libMongo.Client, opts ...Option) (*Repository, error) {
	if ctx == nil {
		ctx = context.Background()
	}

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

	if nilcheck.Interface(repo.logger) {
		repo.logger = libLog.NewNop()
	}

	repo.collectionName = strings.TrimSpace(repo.collectionName)
	repo.tenantField = strings.TrimSpace(repo.tenantField)
	repo.tenantModule = strings.TrimSpace(repo.tenantModule)

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

	if err := repo.ensureIndexes(ctx); err != nil {
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

	tracer := repo.tracking(ctx)

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

	tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.list_outbox_tenants")
	defer span.End()

	if !nilcheck.Interface(repo.tenantDatabaseResolver) {
		tenants, err := repo.tenantDatabaseResolver.ListTenants(ctx, repo.tenantModule)
		if err != nil {
			libOpentelemetry.HandleSpanError(span, "failed to list tenant databases", err)

			return nil, fmt.Errorf("listing tenant databases: %w", err)
		}

		return normalizeTenantIDs(tenants)
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to resolve mongo collection", err)
		return nil, err
	}

	values, err := collection.Distinct(ctx, repo.tenantField, bson.M{
		bsonFieldStatus:  bson.M{bsonOperatorIn: bson.A{outbox.OutboxStatusPending, outbox.OutboxStatusFailed, outbox.OutboxStatusProcessing}},
		repo.tenantField: bson.M{bsonOperatorNE: defaultScopeTenantID},
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list tenants", err)

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

		if !tmcore.IsValidTenantID(tenantID) {
			return nil, fmt.Errorf("%w: %q", outbox.ErrInvalidTenantID, tenantID)
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

	tracer := repo.tracking(ctx)

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

	doc, err := repo.decodeSingle(ctx, collection, repo.idTenantFilter(id, tenantID))
	if err != nil {
		if !errors.Is(err, mongodriver.ErrNoDocuments) {
			libOpentelemetry.HandleSpanError(span, "failed to get outbox event", err)
		}

		return nil, fmt.Errorf("getting outbox event: %w", err)
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

	tracer := repo.tracking(ctx)

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
		mergeFilters(repo.idTenantFilter(id, tenantID), bson.M{bsonFieldStatus: outbox.OutboxStatusProcessing}),
		bson.M{bsonOperatorSet: bson.M{
			bsonFieldStatus:      outbox.OutboxStatusPublished,
			bsonFieldPublishedAt: publishedAt,
			bsonFieldUpdatedAt:   time.Now().UTC(),
		}, bsonOperatorUnset: bson.M{bsonFieldClaimToken: ""}},
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox published", err)

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

	tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.mark_outbox_failed")
	defer span.End()

	updatedDoc, err := repo.getByIDAndStatus(ctx, id, outbox.OutboxStatusProcessing)
	if err != nil {
		if errors.Is(err, mongodriver.ErrNoDocuments) {
			return ErrStateTransitionConflict
		}

		libOpentelemetry.HandleSpanError(span, "failed to load outbox event", err)

		return fmt.Errorf("loading outbox event: %w", err)
	}

	nextAttempts := updatedDoc.Attempts + 1
	nextStatus := outbox.OutboxStatusFailed
	nextLastError := errMsg

	if nextAttempts >= maxAttempts {
		nextStatus = outbox.OutboxStatusInvalid
		nextLastError = outbox.ErrMessageMaxDispatchExceeded
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to resolve mongo collection", err)
		return err
	}

	result, err := collection.UpdateOne(ctx,
		mergeFilters(repo.idTenantFilter(id, updatedDoc.TenantID), bson.M{
			bsonFieldStatus:   outbox.OutboxStatusProcessing,
			bsonFieldAttempts: updatedDoc.Attempts,
		}),
		bson.M{bsonOperatorSet: bson.M{
			bsonFieldStatus:    nextStatus,
			bsonFieldAttempts:  nextAttempts,
			bsonFieldLastError: nextLastError,
			bsonFieldUpdatedAt: time.Now().UTC(),
		}, bsonOperatorUnset: bson.M{bsonFieldClaimToken: ""}},
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox failed", err)

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

	tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.list_failed_for_retry")
	defer span.End()

	docs, err := repo.findCandidates(ctx, bson.M{
		bsonFieldStatus:    outbox.OutboxStatusFailed,
		bsonFieldAttempts:  bson.M{bsonOperatorLT: maxAttempts},
		bsonFieldUpdatedAt: bson.M{bsonOperatorLTE: failedBefore},
	}, bson.D{{Key: bsonFieldUpdatedAt, Value: 1}, {Key: bsonFieldID, Value: 1}}, limit)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list failed events for retry", err)

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

	tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.reset_for_retry")
	defer span.End()

	claimed, err := repo.claimMatching(ctx, bson.M{
		bsonFieldStatus:    outbox.OutboxStatusFailed,
		bsonFieldAttempts:  bson.M{bsonOperatorLT: maxAttempts},
		bsonFieldUpdatedAt: bson.M{bsonOperatorLTE: failedBefore},
	}, bson.D{{Key: bsonFieldUpdatedAt, Value: 1}, {Key: bsonFieldID, Value: 1}}, limit, outbox.OutboxStatusFailed, outbox.OutboxStatusProcessing, func(_ document) bson.M {
		return bson.M{bsonFieldStatus: outbox.OutboxStatusProcessing, bsonFieldUpdatedAt: time.Now().UTC()}
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to reset events for retry", err)

		return nil, fmt.Errorf("resetting events for retry: %w", err)
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

	tracer := repo.tracking(ctx)

	ctx, span := tracer.Start(ctx, "mongo.reset_outbox_processing")
	defer span.End()

	claimed, err := repo.claimMatching(ctx, bson.M{
		bsonFieldStatus:    outbox.OutboxStatusProcessing,
		bsonFieldUpdatedAt: bson.M{bsonOperatorLTE: processingBefore},
	}, bson.D{{Key: bsonFieldUpdatedAt, Value: 1}, {Key: bsonFieldID, Value: 1}}, limit, outbox.OutboxStatusProcessing, outbox.OutboxStatusProcessing, func(candidate document) bson.M {
		nextAttempts := candidate.Attempts + 1
		nextStatus := outbox.OutboxStatusProcessing
		nextLastError := candidate.LastError

		if nextAttempts >= maxAttempts {
			nextStatus = outbox.OutboxStatusInvalid
			nextLastError = outbox.ErrMessageMaxDispatchExceeded
		}

		return bson.M{bsonFieldStatus: nextStatus, bsonFieldAttempts: nextAttempts, bsonFieldLastError: nextLastError, bsonFieldUpdatedAt: time.Now().UTC()}
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to reset stuck events", err)

		return nil, fmt.Errorf("reset stuck events: %w", err)
	}

	return docsToEvents(claimed)
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

	tracer := repo.tracking(ctx)

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
		mergeFilters(repo.idTenantFilter(id, tenantID), bson.M{bsonFieldStatus: outbox.OutboxStatusProcessing}),
		bson.M{bsonOperatorSet: bson.M{bsonFieldStatus: outbox.OutboxStatusInvalid, bsonFieldLastError: errMsg, bsonFieldUpdatedAt: time.Now().UTC()}, bsonOperatorUnset: bson.M{bsonFieldClaimToken: ""}},
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox invalid", err)

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

func (repo *Repository) getByIDAndStatus(ctx context.Context, id uuid.UUID, status string) (document, error) {
	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return document{}, err
	}

	collection, err := repo.collection(ctx)
	if err != nil {
		return document{}, err
	}

	filter := mergeFilters(repo.idTenantFilter(id, tenantID), bson.M{bsonFieldStatus: status})

	return repo.decodeSingle(ctx, collection, filter)
}

func (repo *Repository) initialized() bool {
	return repo != nil && !nilcheck.Interface(repo.client)
}

func (repo *Repository) collection(ctx context.Context) (*mongodriver.Collection, error) {
	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	database, err := repo.collectionDatabase(ctx)
	if err != nil {
		if !errors.Is(err, errUseDefaultDatabase) {
			return nil, err
		}

		database = nil
	}

	if database == nil {
		database, err = repo.client.Database(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve mongo database: %w", err)
		}
	}

	return database.Collection(repo.collectionName), nil
}

func (repo *Repository) collectionDatabase(ctx context.Context) (*mongodriver.Database, error) {
	if repo.tenantModule != "" {
		database := tmcore.GetMBContext(ctx, repo.tenantModule)
		if database == nil && !nilcheck.Interface(repo.tenantDatabaseResolver) {
			resolved, err := repo.resolveTenantDatabase(ctx)
			if err != nil {
				return nil, err
			}

			database = resolved
		}

		if database == nil {
			return nil, ErrTenantDatabaseRequired
		}

		if err := repo.ensureDatabaseIndexes(ctx, database); err != nil {
			return nil, err
		}

		return database, nil
	}

	if database := tmcore.GetMBContext(ctx); database != nil {
		if err := repo.ensureDatabaseIndexes(ctx, database); err != nil {
			return nil, err
		}

		return database, nil
	}

	return repo.resolvedTenantDatabase(ctx)
}

func (repo *Repository) resolvedTenantDatabase(ctx context.Context) (*mongodriver.Database, error) {
	if nilcheck.Interface(repo.tenantDatabaseResolver) {
		return nil, errUseDefaultDatabase
	}

	database, err := repo.resolveTenantDatabase(ctx)
	if err != nil {
		if errors.Is(err, outbox.ErrTenantIDRequired) {
			return nil, errUseDefaultDatabase
		}

		return nil, err
	}

	if database == nil {
		return nil, errUseDefaultDatabase
	}

	if err := repo.ensureDatabaseIndexes(ctx, database); err != nil {
		return nil, err
	}

	return database, nil
}

func (repo *Repository) ensureIndexes(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	indexes := buildIndexes(repo.tenantField)
	if len(indexes) == 0 {
		return nil
	}

	if err := repo.client.EnsureIndexes(ctx, repo.collectionName, indexes...); err != nil {
		return fmt.Errorf("ensure outbox indexes: %w", err)
	}

	return nil
}

func (repo *Repository) ensureDatabaseIndexes(ctx context.Context, database *mongodriver.Database) error {
	if database == nil {
		return ErrRepositoryNotInitialized
	}

	indexes := buildIndexes(repo.tenantField)
	if len(indexes) == 0 {
		return nil
	}

	key := fmt.Sprintf("%p/%s/%s", database.Client(), database.Name(), repo.collectionName)
	if _, loaded := repo.indexedTenantDatabases.Load(key); loaded {
		return nil
	}

	repo.indexMu.Lock()
	defer repo.indexMu.Unlock()

	if _, loaded := repo.indexedTenantDatabases.Load(key); loaded {
		return nil
	}

	if _, err := database.Collection(repo.collectionName).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure outbox indexes for tenant database: %w", err)
	}

	repo.indexedTenantDatabases.Store(key, struct{}{})

	return nil
}

func (repo *Repository) resolveTenantDatabase(ctx context.Context) (*mongodriver.Database, error) {
	if nilcheck.Interface(repo.tenantDatabaseResolver) {
		return nil, ErrTenantDatabaseRequired
	}

	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return nil, err
	}

	if tenantID == defaultScopeTenantID {
		return nil, outbox.ErrTenantIDRequired
	}

	database, err := repo.tenantDatabaseResolver.DatabaseForTenant(ctx, tenantID, repo.tenantModule)
	if err != nil {
		return nil, fmt.Errorf("resolving tenant mongo database: %w", err)
	}

	if database == nil {
		return nil, ErrTenantDatabaseRequired
	}

	return database, nil
}

func (repo *Repository) tenantIDFromContext(ctx context.Context) (string, error) {
	tenantID, ok := outbox.TenantIDFromContext(ctx)
	if repo.requireTenant && (!ok || tenantID == "") {
		return "", outbox.ErrTenantIDRequired
	}

	if !ok {
		return defaultScopeTenantID, nil
	}

	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return "", outbox.ErrTenantIDRequired
	}

	if !tmcore.IsValidTenantID(tenantID) {
		return "", fmt.Errorf("%w: %q", outbox.ErrInvalidTenantID, tenantID)
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
	filter := bson.M{bsonFieldID: id.String()}
	maps.Copy(filter, repo.tenantMatchFilter(tenantID))

	return filter
}

func (repo *Repository) tracking(ctx context.Context) trace.Tracer {
	logger, tracer, meter, trackingErr := libCommons.NewTrackingFromContext(ctx)
	_ = logger
	_ = meter
	_ = trackingErr

	if tracer == nil {
		tracer = repo.tracer
	}

	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("outbox.mongo")
	}

	return tracer
}
