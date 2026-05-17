package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/google/uuid"
)

const maxSQLIdentifierLength = 63

var (
	ErrConnectionRequired        = errors.New("postgres connection is required")
	ErrTransactionRequired       = errors.New("postgres transaction is required")
	ErrStateTransitionConflict   = errors.New("outbox event state transition conflict")
	ErrRepositoryNotInitialized  = errors.New("outbox repository not initialized")
	ErrLimitMustBePositive       = errors.New("limit must be greater than zero")
	ErrIDRequired                = errors.New("id is required")
	ErrAggregateIDRequired       = errors.New("aggregate id is required")
	ErrMaxAttemptsMustBePositive = errors.New("maxAttempts must be greater than zero")
	ErrEventTypeRequired         = errors.New("event type is required")
	ErrTenantResolverRequired    = errors.New("tenant resolver is required")
	ErrTenantDiscovererRequired  = errors.New("tenant discoverer is required")
	ErrNoPrimaryDB               = errors.New("no primary database configured for tenant transaction")
	ErrInvalidIdentifier         = errors.New("invalid sql identifier")
	defaultTransactionTimeout    = 30 * time.Second
	outboxColumns                = "id, event_type, aggregate_id, payload, status, attempts, published_at, last_error, created_at, updated_at"
)

type tenantColumnProvider interface {
	TenantColumn() string
}

type tenantRequirementProvider interface {
	RequiresTenant() bool
}

type Option func(*Repository)

func WithLogger(logger libLog.Logger) Option {
	return func(repo *Repository) {
		if nilcheck.Interface(logger) {
			return
		}

		repo.logger = logger
	}
}

func WithTableName(tableName string) Option {
	return func(repo *Repository) {
		repo.tableName = tableName
	}
}

func WithTenantColumn(tenantColumn string) Option {
	return func(repo *Repository) {
		repo.tenantColumn = tenantColumn
	}
}

func WithTransactionTimeout(timeout time.Duration) Option {
	return func(repo *Repository) {
		if timeout > 0 {
			repo.transactionTimeout = timeout
		}
	}
}

// Repository persists outbox events in PostgreSQL.
type Repository struct {
	client             *libPostgres.Client
	tenantResolver     outbox.TenantResolver
	tenantDiscoverer   outbox.TenantDiscoverer
	primaryDBLookup    func(context.Context) (*sql.DB, error)
	requireTenant      bool
	logger             libLog.Logger
	tableName          string
	tenantColumn       string
	transactionTimeout time.Duration
}

// NewRepository creates a PostgreSQL outbox repository.
func NewRepository(
	client *libPostgres.Client,
	tenantResolver outbox.TenantResolver,
	tenantDiscoverer outbox.TenantDiscoverer,
	opts ...Option,
) (*Repository, error) {
	if client == nil {
		return nil, ErrConnectionRequired
	}

	if nilcheck.Interface(tenantResolver) {
		return nil, ErrTenantResolverRequired
	}

	if nilcheck.Interface(tenantDiscoverer) {
		return nil, ErrTenantDiscovererRequired
	}

	repo := &Repository{
		client:             client,
		tenantResolver:     tenantResolver,
		tenantDiscoverer:   tenantDiscoverer,
		logger:             libLog.NewNop(),
		tableName:          defaultOutboxTableName,
		transactionTimeout: defaultTransactionTimeout,
	}

	if provider, ok := tenantResolver.(tenantColumnProvider); ok {
		repo.tenantColumn = provider.TenantColumn()
	}

	if provider, ok := tenantResolver.(tenantRequirementProvider); ok {
		repo.requireTenant = provider.RequiresTenant()
	}

	for _, opt := range opts {
		if opt != nil {
			opt(repo)
		}
	}

	if nilcheck.Interface(repo.logger) {
		repo.logger = libLog.NewNop()
	}

	repo.tableName = strings.TrimSpace(repo.tableName)
	if repo.tableName == "" {
		repo.tableName = defaultOutboxTableName
	}

	repo.tenantColumn = strings.TrimSpace(repo.tenantColumn)

	if err := validateIdentifierPath(repo.tableName); err != nil {
		return nil, fmt.Errorf("table name: %w", err)
	}

	if repo.tenantColumn != "" {
		if err := validateIdentifier(repo.tenantColumn); err != nil {
			return nil, fmt.Errorf("tenant column: %w", err)
		}
	}

	return repo, nil
}

// GetByID retrieves an outbox event by id.
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

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.get_outbox_by_id")
	defer span.End()

	result, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (*outbox.OutboxEvent, error) {
		table := quoteIdentifierPath(repo.tableName)
		query := "SELECT " + outboxColumns + " FROM " + table + " WHERE id = $1" // #nosec G202 -- table name validated at construction via validateIdentifierPath; quoteIdentifierPath escapes identifiers

		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return nil, tenantErr
		}

		filter, filterArgs, filterErr := repo.tenantFilterClause(2, tenantID)
		if filterErr != nil {
			return nil, filterErr
		}

		args := make([]any, 0, 1+len(filterArgs))
		args = append(args, id)

		query += filter

		args = append(args, filterArgs...)

		row := tx.QueryRowContext(ctx, query, args...)

		return scanOutboxEvent(row)
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			libOpentelemetry.HandleSpanError(span, "failed to get outbox event", err)
		}

		return nil, fmt.Errorf("getting outbox event: %w", err)
	}

	return result, nil
}

// Create stores a new outbox event using a new transaction.
func (repo *Repository) Create(ctx context.Context, event *outbox.OutboxEvent) (*outbox.OutboxEvent, error) {
	return repo.create(ctx, nil, event)
}

// CreateWithTx stores a new outbox event using an existing transaction.
func (repo *Repository) CreateWithTx(
	ctx context.Context,
	tx outbox.Tx,
	event *outbox.OutboxEvent,
) (*outbox.OutboxEvent, error) {
	return repo.create(ctx, tx, event)
}

func (repo *Repository) create(
	ctx context.Context,
	tx *sql.Tx,
	event *outbox.OutboxEvent,
) (*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if err := validateCreateEvent(event); err != nil {
		return nil, err
	}

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.create_outbox_event")
	defer span.End()

	result, err := withTenantTxOrExisting(repo, ctx, tx, func(execTx *sql.Tx) (*outbox.OutboxEvent, error) {
		values, valuesErr := normalizedCreateValues(event, time.Now().UTC())
		if valuesErr != nil {
			return nil, valuesErr
		}

		table := quoteIdentifierPath(repo.tableName)
		query := "INSERT INTO " + table + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
			" (id, event_type, aggregate_id, payload, status, attempts, published_at, last_error, created_at, updated_at"

		args := []any{
			values.id,
			values.eventType,
			values.aggregateID,
			values.payload,
			values.status,
			values.attempts,
			values.publishedAt,
			values.lastError,
			values.createdAt,
			values.updatedAt,
		}

		if repo.tenantColumn != "" {
			tenantID, tenantErr := repo.tenantIDFromContext(ctx)
			if tenantErr != nil {
				return nil, tenantErr
			}

			query += ", " + quoteIdentifier(repo.tenantColumn)

			args = append(args, tenantID)
		}

		var placeholders strings.Builder

		for i := range args {
			if i > 0 {
				placeholders.WriteString(", ")
			}

			fmt.Fprintf(&placeholders, "$%d", i+1)
		}

		query += ") VALUES (" + placeholders.String() + ") RETURNING " + outboxColumns

		row := execTx.QueryRowContext(ctx, query, args...)

		return scanOutboxEvent(row)
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to create outbox event", err)

		return nil, fmt.Errorf("creating outbox event: %w", err)
	}

	return result, nil
}

// ListPending retrieves pending outbox events up to the given limit.
func (repo *Repository) ListPending(ctx context.Context, limit int) ([]*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if limit <= 0 {
		return nil, ErrLimitMustBePositive
	}

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.list_outbox_pending")
	defer span.End()

	result, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) ([]*outbox.OutboxEvent, error) {
		events, err := repo.listPendingRows(ctx, tx, limit)
		if err != nil {
			return nil, err
		}

		if len(events) == 0 {
			return events, nil
		}

		ids := collectEventIDs(events)
		if len(ids) == 0 {
			return events, nil
		}

		now := time.Now().UTC()

		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return nil, tenantErr
		}

		if err := repo.markEventsProcessing(ctx, tx, now, ids, tenantID, outbox.OutboxStatusPending); err != nil {
			return nil, err
		}

		applyProcessingState(events, now)

		return events, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list outbox events", err)

		return nil, fmt.Errorf("listing pending events: %w", err)
	}

	return result, nil
}

// ListPendingByType retrieves pending outbox events filtered by event type.
func (repo *Repository) ListPendingByType(
	ctx context.Context,
	eventType string,
	limit int,
) ([]*outbox.OutboxEvent, error) {
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

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.list_outbox_pending_by_type")
	defer span.End()

	result, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) ([]*outbox.OutboxEvent, error) {
		events, err := repo.listPendingByTypeRows(ctx, tx, eventType, limit)
		if err != nil {
			return nil, err
		}

		if len(events) == 0 {
			return events, nil
		}

		ids := collectEventIDs(events)
		if len(ids) == 0 {
			return events, nil
		}

		now := time.Now().UTC()

		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return nil, tenantErr
		}

		if err := repo.markEventsProcessing(ctx, tx, now, ids, tenantID, outbox.OutboxStatusPending); err != nil {
			return nil, err
		}

		applyProcessingState(events, now)

		return events, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list outbox events by type", err)

		return nil, fmt.Errorf("listing pending events by type: %w", err)
	}

	return result, nil
}

// ListTenants returns tenant IDs discovered by the configured discoverer.
func (repo *Repository) ListTenants(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.list_outbox_tenants")
	defer span.End()

	tenants, err := repo.tenantDiscoverer.DiscoverTenants(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list tenant schemas", err)

		return nil, fmt.Errorf("list tenant schemas: %w", err)
	}

	return tenants, nil
}

func (repo *Repository) listPendingRows(ctx context.Context, tx *sql.Tx, limit int) ([]*outbox.OutboxEvent, error) {
	table := quoteIdentifierPath(repo.tableName)
	query := "SELECT " + outboxColumns + " FROM " + table + " WHERE status = $1"

	tenantID, tenantErr := repo.tenantIDFromContext(ctx)
	if tenantErr != nil {
		return nil, tenantErr
	}

	filter, filterArgs, filterErr := repo.tenantFilterClause(2, tenantID)
	if filterErr != nil {
		return nil, filterErr
	}

	args := make([]any, 0, 1+len(filterArgs)+1)
	args = append(args, outbox.OutboxStatusPending)

	query += filter

	args = append(args, filterArgs...)
	query += fmt.Sprintf(" ORDER BY created_at ASC LIMIT $%d FOR UPDATE SKIP LOCKED", len(args)+1)
	args = append(args, limit)

	return queryOutboxEvents(ctx, tx, query, args, limit, "querying pending events")
}

func (repo *Repository) listPendingByTypeRows(
	ctx context.Context,
	tx *sql.Tx,
	eventType string,
	limit int,
) ([]*outbox.OutboxEvent, error) {
	table := quoteIdentifierPath(repo.tableName)
	query := "SELECT " + outboxColumns + " FROM " + table + " WHERE status = $1 AND event_type = $2"

	tenantID, tenantErr := repo.tenantIDFromContext(ctx)
	if tenantErr != nil {
		return nil, tenantErr
	}

	filter, filterArgs, filterErr := repo.tenantFilterClause(3, tenantID)
	if filterErr != nil {
		return nil, filterErr
	}

	args := make([]any, 0, 2+len(filterArgs)+1)
	args = append(args, outbox.OutboxStatusPending, eventType)

	query += filter

	args = append(args, filterArgs...)

	query += fmt.Sprintf(" ORDER BY created_at ASC LIMIT $%d FOR UPDATE SKIP LOCKED", len(args)+1)
	args = append(args, limit)

	return queryOutboxEvents(ctx, tx, query, args, limit, "querying pending events by type")
}

func (repo *Repository) listFailedForRetryRows(
	ctx context.Context,
	tx *sql.Tx,
	limit int,
	failedBefore time.Time,
	maxAttempts int,
	forUpdate bool,
) ([]*outbox.OutboxEvent, error) {
	table := quoteIdentifierPath(repo.tableName)
	query := "SELECT " + outboxColumns + " FROM " + table +
		" WHERE status = $1 AND attempts < $2 AND updated_at <= $3"

	tenantID, tenantErr := repo.tenantIDFromContext(ctx)
	if tenantErr != nil {
		return nil, tenantErr
	}

	filter, filterArgs, filterErr := repo.tenantFilterClause(4, tenantID)
	if filterErr != nil {
		return nil, filterErr
	}

	args := make([]any, 0, 3+len(filterArgs)+1)
	args = append(args, outbox.OutboxStatusFailed, maxAttempts, failedBefore)

	query += filter

	args = append(args, filterArgs...)
	query += fmt.Sprintf(" ORDER BY updated_at ASC LIMIT $%d", len(args)+1)
	args = append(args, limit)

	if forUpdate {
		query += " FOR UPDATE SKIP LOCKED"
	}

	return queryOutboxEvents(ctx, tx, query, args, limit, "querying failed events for retry")
}

func (repo *Repository) listStuckProcessingRows(
	ctx context.Context,
	tx *sql.Tx,
	limit int,
	processingBefore time.Time,
) ([]*outbox.OutboxEvent, error) {
	table := quoteIdentifierPath(repo.tableName)
	query := "SELECT " + outboxColumns + " FROM " + table +
		" WHERE status = $1 AND updated_at <= $2"

	tenantID, tenantErr := repo.tenantIDFromContext(ctx)
	if tenantErr != nil {
		return nil, tenantErr
	}

	filter, filterArgs, filterErr := repo.tenantFilterClause(3, tenantID)
	if filterErr != nil {
		return nil, filterErr
	}

	args := make([]any, 0, 2+len(filterArgs)+1)
	args = append(args, outbox.OutboxStatusProcessing, processingBefore)

	query += filter

	args = append(args, filterArgs...)
	query += fmt.Sprintf(" ORDER BY updated_at ASC LIMIT $%d FOR UPDATE SKIP LOCKED", len(args)+1)
	args = append(args, limit)

	return queryOutboxEvents(ctx, tx, query, args, limit, "querying stuck events")
}

func (repo *Repository) markEventsProcessing(
	ctx context.Context,
	tx *sql.Tx,
	now time.Time,
	ids []uuid.UUID,
	tenantID string,
	fromStatus string,
) error {
	return repo.markEventsWithStatus(
		ctx,
		tx,
		now,
		outbox.OutboxStatusProcessing,
		ids,
		tenantID,
		fromStatus,
	)
}

func (repo *Repository) markEventsWithStatus(
	ctx context.Context,
	tx *sql.Tx,
	now time.Time,
	status string,
	ids []uuid.UUID,
	tenantID string,
	fromStatus string,
) error {
	if err := outbox.ValidateOutboxTransition(fromStatus, status); err != nil {
		return fmt.Errorf("status transition: %w", err)
	}

	table := quoteIdentifierPath(repo.tableName)
	query := "UPDATE " + table + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
		" SET status = $1::outbox_event_status, updated_at = $2 WHERE id = ANY($3::uuid[]) AND status = $4::outbox_event_status"

	filter, filterArgs, filterErr := repo.tenantFilterClause(5, tenantID)
	if filterErr != nil {
		return filterErr
	}

	args := make([]any, 0, 4+len(filterArgs))
	args = append(args, status, now, ids, fromStatus)

	query += filter

	args = append(args, filterArgs...)

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating status to %s: %w", status, err)
	}

	if err := ensureRowsAffectedExact(result, int64(len(ids))); err != nil {
		return fmt.Errorf("updating status to %s: %w", status, err)
	}

	return nil
}

func (repo *Repository) markStuckEventsReprocessing(
	ctx context.Context,
	tx *sql.Tx,
	now time.Time,
	ids []uuid.UUID,
	tenantID string,
) error {
	if err := outbox.ValidateOutboxTransition(outbox.OutboxStatusProcessing, outbox.OutboxStatusProcessing); err != nil {
		return fmt.Errorf("stuck reprocessing transition: %w", err)
	}

	// Intentionally keep PROCESSING -> PROCESSING while incrementing attempts.
	// If we flipped to PENDING before returning rows to the caller, another
	// dispatcher could acquire and publish the same event immediately after this
	// transaction commits. Keeping PROCESSING narrows duplicate publication windows
	// to later stuck-recovery cycles.
	table := quoteIdentifierPath(repo.tableName)
	query := "UPDATE " + table + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
		" SET status = $1::outbox_event_status, attempts = attempts + 1, updated_at = $2 " +
		"WHERE id = ANY($3::uuid[]) AND status = $4::outbox_event_status"

	filter, filterArgs, filterErr := repo.tenantFilterClause(5, tenantID)
	if filterErr != nil {
		return filterErr
	}

	args := make([]any, 0, 4+len(filterArgs))
	args = append(args, outbox.OutboxStatusProcessing, now, ids, outbox.OutboxStatusProcessing)

	query += filter

	args = append(args, filterArgs...)

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating stuck events to processing: %w", err)
	}

	if err := ensureRowsAffectedExact(result, int64(len(ids))); err != nil {
		return fmt.Errorf("updating stuck events to processing: %w", err)
	}

	return nil
}

func (repo *Repository) markStuckEventsInvalid(
	ctx context.Context,
	tx *sql.Tx,
	now time.Time,
	ids []uuid.UUID,
	tenantID string,
) error {
	if err := outbox.ValidateOutboxTransition(outbox.OutboxStatusProcessing, outbox.OutboxStatusInvalid); err != nil {
		return fmt.Errorf("stuck invalid transition: %w", err)
	}

	table := quoteIdentifierPath(repo.tableName)
	query := "UPDATE " + table + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
		" SET status = $1::outbox_event_status, attempts = attempts + 1, " +
		"last_error = $2, updated_at = $3 WHERE id = ANY($4::uuid[]) AND status = $5::outbox_event_status"

	filter, filterArgs, filterErr := repo.tenantFilterClause(6, tenantID)
	if filterErr != nil {
		return filterErr
	}

	args := make([]any, 0, 5+len(filterArgs))
	args = append(args, outbox.OutboxStatusInvalid, "max dispatch attempts exceeded", now, ids, outbox.OutboxStatusProcessing)

	query += filter

	args = append(args, filterArgs...)

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating stuck events to invalid: %w", err)
	}

	if err := ensureRowsAffectedExact(result, int64(len(ids))); err != nil {
		return fmt.Errorf("updating stuck events to invalid: %w", err)
	}

	return nil
}

func splitStuckEvents(events []*outbox.OutboxEvent, maxAttempts int) ([]*outbox.OutboxEvent, []uuid.UUID) {
	retryEvents := make([]*outbox.OutboxEvent, 0, len(events))
	exhaustedIDs := make([]uuid.UUID, 0)

	for _, event := range events {
		if event == nil || event.ID == uuid.Nil {
			continue
		}

		if event.Attempts+1 >= maxAttempts {
			exhaustedIDs = append(exhaustedIDs, event.ID)

			continue
		}

		retryEvents = append(retryEvents, event)
	}

	return retryEvents, exhaustedIDs
}

func applyStuckReprocessingState(events []*outbox.OutboxEvent, now time.Time) {
	for _, event := range events {
		if event == nil {
			continue
		}

		event.Attempts++
		event.Status = outbox.OutboxStatusProcessing
		event.UpdatedAt = now
	}
}

func collectEventIDs(events []*outbox.OutboxEvent) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(events))

	for _, event := range events {
		if event == nil || event.ID == uuid.Nil {
			continue
		}

		ids = append(ids, event.ID)
	}

	return ids
}

func applyProcessingState(events []*outbox.OutboxEvent, now time.Time) {
	for _, event := range events {
		if event == nil {
			continue
		}

		event.Status = outbox.OutboxStatusProcessing
		event.UpdatedAt = now
	}
}

func scanOutboxEvent(scanner interface{ Scan(dest ...any) error }) (*outbox.OutboxEvent, error) {
	var event outbox.OutboxEvent

	var lastError sql.NullString

	if err := scanner.Scan(
		&event.ID,
		&event.EventType,
		&event.AggregateID,
		&event.Payload,
		&event.Status,
		&event.Attempts,
		&event.PublishedAt,
		&lastError,
		&event.CreatedAt,
		&event.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("scanning outbox event: %w", err)
	}

	if lastError.Valid {
		event.LastError = lastError.String
	}

	return &event, nil
}

func withTenantTxOrExisting[T any](
	repo *Repository,
	ctx context.Context,
	tx *sql.Tx,
	fn func(*sql.Tx) (T, error),
) (T, error) {
	var zero T

	if ctx == nil {
		ctx = context.Background()
	}

	if tx != nil {
		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return zero, tenantErr
		}

		if err := repo.tenantResolver.ApplyTenant(ctx, tx, tenantID); err != nil {
			return zero, fmt.Errorf("failed to apply tenant: %w", err)
		}

		return fn(tx)
	}

	primaryDB, err := repo.primaryDB(ctx)
	if err != nil {
		return zero, err
	}

	txCtx := ctx

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc

		txCtx, cancel = context.WithTimeout(ctx, repo.transactionTimeout)
		defer cancel()
	}

	newTx, err := primaryDB.BeginTx(txCtx, nil)
	if err != nil {
		return zero, fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		_ = newTx.Rollback()
	}()

	tenantID, tenantErr := repo.tenantIDFromContext(txCtx)
	if tenantErr != nil {
		return zero, tenantErr
	}

	if err := repo.tenantResolver.ApplyTenant(txCtx, newTx, tenantID); err != nil {
		return zero, fmt.Errorf("failed to apply tenant: %w", err)
	}

	result, err := fn(newTx)
	if err != nil {
		return zero, err
	}

	if err := newTx.Commit(); err != nil {
		return zero, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return result, nil
}

func (repo *Repository) initialized() bool {
	return repo != nil && repo.client != nil && !nilcheck.Interface(repo.tenantResolver) && !nilcheck.Interface(repo.tenantDiscoverer)
}

// RequiresTenant reports whether repository operations require a tenant ID.
func (repo *Repository) RequiresTenant() bool {
	if repo == nil {
		return true
	}

	return repo.requireTenant || repo.tenantColumn != ""
}

func (repo *Repository) primaryDB(ctx context.Context) (*sql.DB, error) {
	if repo == nil {
		return nil, ErrConnectionRequired
	}

	if repo.primaryDBLookup != nil {
		return repo.primaryDBLookup(ctx)
	}

	return resolvePrimaryDB(ctx, repo.client)
}

func (repo *Repository) tenantIDFromContext(ctx context.Context) (string, error) {
	tenantID, ok := outbox.TenantIDFromContext(ctx)
	if (repo.tenantColumn != "" || repo.requireTenant) && (!ok || tenantID == "") {
		return "", outbox.ErrTenantIDRequired
	}

	if !ok {
		return "", nil
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

func (repo *Repository) tenantFilterClause(index int, tenantID string) (string, []any, error) {
	if repo.tenantColumn == "" {
		return "", nil, nil
	}

	if tenantID == "" {
		return "", nil, outbox.ErrTenantIDRequired
	}

	filter := fmt.Sprintf(" AND %s = $%d", quoteIdentifier(repo.tenantColumn), index)

	return filter, []any{tenantID}, nil
}
