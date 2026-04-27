package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
)

// MarkPublished marks an outbox event as published.
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

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.mark_outbox_published")
	defer span.End()

	_, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (struct{}, error) {
		table := quoteIdentifierPath(repo.tableName)
		query := "UPDATE " + table + " SET status = $1::outbox_event_status, published_at = $2, updated_at = $3 " + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
			"WHERE id = $4 AND status = $5::outbox_event_status"

		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return struct{}{}, tenantErr
		}

		filter, filterArgs, filterErr := repo.tenantFilterClause(6, tenantID)
		if filterErr != nil {
			return struct{}{}, filterErr
		}

		args := make([]any, 0, 5+len(filterArgs))
		args = append(args, outbox.OutboxStatusPublished, publishedAt, time.Now().UTC(), id, outbox.OutboxStatusProcessing)

		query += filter

		args = append(args, filterArgs...)

		result, execErr := tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			return struct{}{}, fmt.Errorf("executing update: %w", execErr)
		}

		if err := ensureRowsAffected(result); err != nil {
			return struct{}{}, err
		}

		return struct{}{}, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox published", err)

		return fmt.Errorf("marking published: %w", err)
	}

	return nil
}

// MarkFailed marks an outbox event as failed and may transition to invalid.
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

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.mark_outbox_failed")
	defer span.End()

	_, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (struct{}, error) {
		table := quoteIdentifierPath(repo.tableName)
		query := "UPDATE " + table + " SET " + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
			"status = CASE WHEN attempts + 1 >= $1 THEN $2 ELSE $3 END::outbox_event_status, " +
			"attempts = attempts + 1, " +
			"last_error = CASE WHEN attempts + 1 >= $1 THEN $4 ELSE $5 END, " +
			"updated_at = $6 WHERE id = $7 AND status = $8::outbox_event_status"

		args := []any{
			maxAttempts,
			outbox.OutboxStatusInvalid,
			outbox.OutboxStatusFailed,
			"max dispatch attempts exceeded",
			errMsg,
			time.Now().UTC(),
			id,
			outbox.OutboxStatusProcessing,
		}

		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return struct{}{}, tenantErr
		}

		filter, filterArgs, filterErr := repo.tenantFilterClause(9, tenantID)
		if filterErr != nil {
			return struct{}{}, filterErr
		}

		query += filter

		args = append(args, filterArgs...)

		result, execErr := tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			return struct{}{}, fmt.Errorf("executing update: %w", execErr)
		}

		if err := ensureRowsAffected(result); err != nil {
			return struct{}{}, err
		}

		return struct{}{}, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox failed", err)

		return fmt.Errorf("marking failed: %w", err)
	}

	return nil
}

// ListFailedForRetry lists failed events eligible for retry.
func (repo *Repository) ListFailedForRetry(
	ctx context.Context,
	limit int,
	failedBefore time.Time,
	maxAttempts int,
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

	if maxAttempts <= 0 {
		return nil, ErrMaxAttemptsMustBePositive
	}

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.list_failed_for_retry")
	defer span.End()

	result, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) ([]*outbox.OutboxEvent, error) {
		return repo.listFailedForRetryRows(ctx, tx, limit, failedBefore, maxAttempts, false)
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list failed events for retry", err)

		return nil, fmt.Errorf("listing failed events for retry: %w", err)
	}

	return result, nil
}

// ResetForRetry atomically selects and resets failed events to processing.
func (repo *Repository) ResetForRetry(
	ctx context.Context,
	limit int,
	failedBefore time.Time,
	maxAttempts int,
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

	if maxAttempts <= 0 {
		return nil, ErrMaxAttemptsMustBePositive
	}

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.reset_for_retry")
	defer span.End()

	result, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) ([]*outbox.OutboxEvent, error) {
		events, err := repo.listFailedForRetryRows(ctx, tx, limit, failedBefore, maxAttempts, true)
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

		if err := repo.markEventsProcessing(ctx, tx, now, ids, tenantID, outbox.OutboxStatusFailed); err != nil {
			return nil, err
		}

		applyProcessingState(events, now)

		return events, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to reset events for retry", err)

		return nil, fmt.Errorf("resetting events for retry: %w", err)
	}

	return result, nil
}

// ResetStuckProcessing reclaims long-running processing events.
func (repo *Repository) ResetStuckProcessing(
	ctx context.Context,
	limit int,
	processingBefore time.Time,
	maxAttempts int,
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

	if maxAttempts <= 0 {
		return nil, ErrMaxAttemptsMustBePositive
	}

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.reset_outbox_processing")
	defer span.End()

	result, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) ([]*outbox.OutboxEvent, error) {
		events, err := repo.listStuckProcessingRows(ctx, tx, limit, processingBefore)
		if err != nil {
			return nil, err
		}

		if len(events) == 0 {
			return events, nil
		}

		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return nil, tenantErr
		}

		retryEvents, exhaustedIDs := splitStuckEvents(events, maxAttempts)
		now := time.Now().UTC()

		retryIDs := collectEventIDs(retryEvents)
		if len(retryIDs) > 0 {
			if err := repo.markStuckEventsReprocessing(ctx, tx, now, retryIDs, tenantID); err != nil {
				return nil, err
			}

			applyStuckReprocessingState(retryEvents, now)
		}

		if len(exhaustedIDs) > 0 {
			if err := repo.markStuckEventsInvalid(ctx, tx, now, exhaustedIDs, tenantID); err != nil {
				return nil, err
			}
		}

		return retryEvents, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to reset stuck events", err)

		return nil, fmt.Errorf("reset stuck events: %w", err)
	}

	return result, nil
}

// MarkInvalid marks an outbox event as invalid.
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

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.mark_outbox_invalid")
	defer span.End()

	_, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (struct{}, error) {
		table := quoteIdentifierPath(repo.tableName)
		query := "UPDATE " + table + " SET status = $1::outbox_event_status, last_error = $2, updated_at = $3 " + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
			"WHERE id = $4 AND status = $5::outbox_event_status"

		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return struct{}{}, tenantErr
		}

		filter, filterArgs, filterErr := repo.tenantFilterClause(6, tenantID)
		if filterErr != nil {
			return struct{}{}, filterErr
		}

		args := make([]any, 0, 5+len(filterArgs))
		args = append(args, outbox.OutboxStatusInvalid, errMsg, time.Now().UTC(), id, outbox.OutboxStatusProcessing)

		query += filter

		args = append(args, filterArgs...)

		result, execErr := tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			return struct{}{}, fmt.Errorf("executing update: %w", execErr)
		}

		if err := ensureRowsAffected(result); err != nil {
			return struct{}{}, err
		}

		return struct{}{}, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to mark outbox invalid", err)

		return fmt.Errorf("marking invalid: %w", err)
	}

	return nil
}
