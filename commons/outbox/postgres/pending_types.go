package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
)

var _ outbox.MultiTypePendingRepository = (*Repository)(nil)

// ListPendingByTypes atomically claims pending events matching eventTypes.
// Event types are prioritized in caller order, with FIFO created_at/id order
// inside each type. Selection uses one FOR UPDATE SKIP LOCKED transaction and
// the same transaction marks every returned row PROCESSING.
func (repo *Repository) ListPendingByTypes(
	ctx context.Context,
	eventTypes []string,
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

	types := normalizeEventTypes(eventTypes)
	if len(types) == 0 {
		return nil, ErrEventTypeRequired
	}

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.list_outbox_pending_by_types")
	defer span.End()

	if missing, err := repo.tenantOutboxTableMissing(ctx); err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to check outbox table presence", err)

		return nil, fmt.Errorf("listing pending events by types: %w", err)
	} else if missing {
		return nil, nil
	}

	result, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) ([]*outbox.OutboxEvent, error) {
		events, listErr := repo.listPendingByTypesRows(ctx, tx, types, limit)
		if listErr != nil || len(events) == 0 {
			return events, listErr
		}

		ids := collectEventIDs(events)
		if len(ids) == 0 {
			return events, nil
		}

		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return nil, tenantErr
		}

		now := time.Now().UTC()
		if markErr := repo.markEventsProcessing(
			ctx,
			tx,
			now,
			ids,
			tenantID,
			outbox.OutboxStatusPending,
		); markErr != nil {
			return nil, markErr
		}

		applyProcessingState(events, now)

		return events, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list outbox events by types", err)

		return nil, fmt.Errorf("listing pending events by types: %w", err)
	}

	return result, nil
}

func (repo *Repository) listPendingByTypesRows(
	ctx context.Context,
	tx *sql.Tx,
	eventTypes []string,
	limit int,
) ([]*outbox.OutboxEvent, error) {
	table := quoteIdentifierPath(repo.tableName)

	var query strings.Builder
	query.WriteString("SELECT ")
	query.WriteString(outboxColumns)
	query.WriteString(" FROM ")
	query.WriteString(table)
	query.WriteString(" WHERE status = $1 AND event_type IN (")

	args := make([]any, 0, len(eventTypes)+3)
	args = append(args, outbox.OutboxStatusPending)

	for index, eventType := range eventTypes {
		if index > 0 {
			query.WriteString(", ")
		}

		query.WriteByte('$')
		query.WriteString(strconv.Itoa(len(args) + 1))
		args = append(args, eventType)
	}

	query.WriteByte(')')

	tenantID, err := repo.tenantIDFromContext(ctx)
	if err != nil {
		return nil, err
	}

	filter, filterArgs, err := repo.tenantFilterClause(len(args)+1, tenantID)
	if err != nil {
		return nil, err
	}

	query.WriteString(filter)

	args = append(args, filterArgs...)

	query.WriteString(" ORDER BY CASE event_type")

	for index := range eventTypes {
		query.WriteString(" WHEN $")
		query.WriteString(strconv.Itoa(index + 2))
		query.WriteString(" THEN ")
		query.WriteString(strconv.Itoa(index))
	}

	query.WriteString(" ELSE ")
	query.WriteString(strconv.Itoa(len(eventTypes)))
	query.WriteString(" END, created_at ASC, id ASC LIMIT $")
	query.WriteString(strconv.Itoa(len(args) + 1))
	query.WriteString(" FOR UPDATE SKIP LOCKED")

	args = append(args, limit)

	return queryOutboxEvents(ctx, tx, query.String(), args, limit, "querying pending events by types")
}

func normalizeEventTypes(eventTypes []string) []string {
	types := make([]string, 0, len(eventTypes))
	seen := make(map[string]struct{}, len(eventTypes))

	for _, eventType := range eventTypes {
		eventType = strings.TrimSpace(eventType)
		if eventType == "" {
			continue
		}

		if _, exists := seen[eventType]; exists {
			continue
		}

		seen[eventType] = struct{}{}
		types = append(types, eventType)
	}

	return types
}
