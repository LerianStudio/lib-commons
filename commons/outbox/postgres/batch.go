package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/google/uuid"
)

const outboxCreateColumnCount = 10

var (
	errBatchInsertNilEvent    = errors.New("batch insert returned a nil event")
	errBatchInsertDuplicateID = errors.New("batch insert returned duplicate event id")
)

var _ outbox.TransactionalBatchWriter = (*Repository)(nil)

// CreateManyWithTx stores outbox events in one set-wise INSERT using an
// existing transaction. A nil transaction uses the repository's normal
// transaction orchestration. Returned events follow the input order.
func (repo *Repository) CreateManyWithTx(
	ctx context.Context,
	tx outbox.Tx,
	events []*outbox.OutboxEvent,
) ([]*outbox.OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if len(events) == 0 {
		return []*outbox.OutboxEvent{}, nil
	}

	for _, event := range events {
		if err := validateCreateEvent(event); err != nil {
			return nil, err
		}
	}

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.create_many_outbox_events")
	defer span.End()

	created, err := withTenantTxOrExisting(repo, ctx, tx, func(execTx *sql.Tx) ([]*outbox.OutboxEvent, error) {
		query, args, queryErr := repo.createManyQuery(ctx, events, time.Now().UTC())
		if queryErr != nil {
			return nil, queryErr
		}

		inserted, queryErr := queryOutboxEvents(
			ctx,
			execTx,
			query,
			args,
			len(events),
			"inserting outbox event batch",
		)
		if queryErr != nil {
			return nil, queryErr
		}

		return orderCreatedEvents(events, inserted)
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to create outbox event batch", err)

		return nil, fmt.Errorf("creating outbox event batch: %w", err)
	}

	return created, nil
}

func (repo *Repository) createManyQuery(
	ctx context.Context,
	events []*outbox.OutboxEvent,
	now time.Time,
) (string, []any, error) {
	tenantID := ""

	if repo.tenantColumn != "" {
		var err error

		tenantID, err = repo.tenantIDFromContext(ctx)
		if err != nil {
			return "", nil, err
		}
	}

	columnCount := outboxCreateColumnCount
	if repo.tenantColumn != "" {
		columnCount++
	}

	args := make([]any, 0, len(events)*columnCount)

	var placeholders strings.Builder

	for eventIndex, event := range events {
		values, err := normalizedCreateValues(event, now)
		if err != nil {
			return "", nil, err
		}

		if eventIndex > 0 {
			placeholders.WriteString(", ")
		}

		start := len(args) + 1

		args = appendCreateArgs(args, values)
		if repo.tenantColumn != "" {
			args = append(args, tenantID)
		}

		writePlaceholderTuple(&placeholders, start, columnCount)
	}

	table := quoteIdentifierPath(repo.tableName)

	query := "INSERT INTO " + table + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
		" (id, event_type, aggregate_id, payload, status, attempts, published_at, last_error, created_at, updated_at"
	if repo.tenantColumn != "" {
		query += ", " + quoteIdentifier(repo.tenantColumn)
	}

	query += ") VALUES " + placeholders.String() + " RETURNING " + outboxColumns

	return query, args, nil
}

func appendCreateArgs(args []any, values createValues) []any {
	return append(
		args,
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
	)
}

func writePlaceholderTuple(builder *strings.Builder, start, count int) {
	builder.WriteString("(")

	for offset := range count {
		if offset > 0 {
			builder.WriteString(", ")
		}

		builder.WriteString("$")
		builder.WriteString(strconv.Itoa(start + offset))
	}

	builder.WriteString(")")
}

func orderCreatedEvents(
	input []*outbox.OutboxEvent,
	inserted []*outbox.OutboxEvent,
) ([]*outbox.OutboxEvent, error) {
	if len(inserted) != len(input) {
		return nil, fmt.Errorf("batch insert returned %d rows for %d events", len(inserted), len(input))
	}

	byID := make(map[uuid.UUID]*outbox.OutboxEvent, len(inserted))
	for _, event := range inserted {
		if event == nil {
			return nil, errBatchInsertNilEvent
		}

		if _, exists := byID[event.ID]; exists {
			return nil, errBatchInsertDuplicateID
		}

		byID[event.ID] = event
	}

	ordered := make([]*outbox.OutboxEvent, len(input))
	for index, event := range input {
		created, ok := byID[event.ID]
		if !ok {
			return nil, fmt.Errorf("batch insert did not return input event at index %d", index)
		}

		ordered[index] = created
	}

	return ordered, nil
}
