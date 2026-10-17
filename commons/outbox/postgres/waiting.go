package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/google/uuid"
)

var (
	_ outbox.Requeuer            = (*Repository)(nil)
	_ outbox.OldestWaitingReader = (*Repository)(nil)
)

// Requeue implements outbox.Requeuer.
func (repo *Repository) Requeue(ctx context.Context, id uuid.UUID) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return ErrRepositoryNotInitialized
	}

	for _, from := range []string{outbox.OutboxStatusInvalid, outbox.OutboxStatusFailed} {
		if err := outbox.ValidateOutboxTransition(from, outbox.OutboxStatusPending); err != nil {
			return fmt.Errorf("requeue transition: %w", err)
		}
	}

	if id == uuid.Nil {
		return ErrIDRequired
	}

	ctx, span := tracerFromContext(ctx).Start(ctx, "postgres.requeue_outbox_event")
	defer span.End()

	_, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (struct{}, error) {
		tenantID, err := repo.tenantIDFromContext(ctx)
		if err != nil {
			return struct{}{}, err
		}

		filter, filterArgs, err := repo.tenantFilterClause(6, tenantID)
		if err != nil {
			return struct{}{}, err
		}

		query := "UPDATE " + quoteIdentifierPath(repo.tableName) + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
			" SET status = $1::outbox_event_status, attempts = 0, last_error = NULL, updated_at = $2" +
			" WHERE id = $3 AND status IN ($4::outbox_event_status, $5::outbox_event_status)" + filter

		args := append([]any{outbox.OutboxStatusPending, time.Now().UTC(), id, outbox.OutboxStatusInvalid, outbox.OutboxStatusFailed}, filterArgs...)

		scope, scopeArgs := claimScopeClause(ctx, len(args)+1)

		result, err := tx.ExecContext(ctx, query+scope, append(args, scopeArgs...)...)
		if err != nil {
			return struct{}{}, fmt.Errorf("executing update: %w", err)
		}

		rows, err := rowsAffected(result)
		if err != nil {
			return struct{}{}, err
		}

		if rows == 0 {
			return struct{}{}, outbox.ErrOutboxEventNotRequeueable
		}

		return struct{}{}, nil
	})
	if err != nil {
		if !errors.Is(err, outbox.ErrOutboxEventNotRequeueable) {
			libOpentelemetry.HandleSpanError(span, "failed to requeue outbox event", err)
		}

		return fmt.Errorf("requeueing event: %w", err)
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

	ctx, span := tracerFromContext(ctx).Start(ctx, "postgres.oldest_waiting_outbox_event")
	defer span.End()

	if missing, err := repo.tenantOutboxTableMissing(ctx); err != nil {
		return time.Time{}, false, fmt.Errorf("reading oldest waiting event: %w", err)
	} else if missing {
		return time.Time{}, false, nil
	}

	oldest, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (sql.NullTime, error) {
		tenantID, err := repo.tenantIDFromContext(ctx)
		if err != nil {
			return sql.NullTime{}, err
		}

		query, args, err := repo.oldestWaitingQuery(ctx, tenantID)
		if err != nil {
			return sql.NullTime{}, err
		}

		var oldest sql.NullTime

		err = tx.QueryRowContext(ctx, query, args...).Scan(&oldest)

		return oldest, err
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to read oldest waiting outbox event", err)

		return time.Time{}, false, fmt.Errorf("reading oldest waiting event: %w", err)
	}

	return oldest.Time, oldest.Valid, nil
}

// oldestWaitingQuery is one LEAST of index-only min(created_at) probes: one per waiting
// status or, under a claim scope, one per (event type, status), so the plan never walks
// another service's backlog in a shared table, however few types it holds.
func (repo *Repository) oldestWaitingQuery(ctx context.Context, tenantID string) (string, []any, error) {
	statuses := []any{outbox.OutboxStatusPending, outbox.OutboxStatusProcessing, outbox.OutboxStatusFailed}
	eventTypes, scoped := outbox.ClaimEventTypesFromContext(ctx)

	var conditions []string

	for status := 1; status <= len(statuses); status++ {
		if !scoped {
			conditions = append(conditions, fmt.Sprintf("status = $%d", status))

			continue
		}

		for i := range eventTypes {
			conditions = append(conditions, fmt.Sprintf("event_type = $%d AND status = $%d", len(statuses)+1+i, status))
		}
	}

	if len(conditions) == 0 { // an empty claim scope waits on nothing
		return "SELECT NULL::timestamptz", nil, nil
	}

	args := statuses
	for _, eventType := range eventTypes {
		args = append(args, eventType)
	}

	filter, filterArgs, err := repo.tenantFilterClause(len(args)+1, tenantID)
	if err != nil {
		return "", nil, err
	}

	table := quoteIdentifierPath(repo.tableName)
	probes := make([]string, len(conditions))

	for i, condition := range conditions {
		probes[i] = "(SELECT min(created_at) FROM " + table + " WHERE " + condition + filter + ")" // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
	}

	return "SELECT LEAST(" + strings.Join(probes, ", ") + ")", append(args, filterArgs...), nil
}
