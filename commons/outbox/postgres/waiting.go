package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/google/uuid"
)

var _ outbox.InvalidRequeuer = (*Repository)(nil)

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

	ctx, span := tracerFromContext(ctx).Start(ctx, "postgres.requeue_outbox_invalid")
	defer span.End()

	_, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (struct{}, error) {
		tenantID, err := repo.tenantIDFromContext(ctx)
		if err != nil {
			return struct{}{}, err
		}

		filter, filterArgs, err := repo.tenantFilterClause(5, tenantID)
		if err != nil {
			return struct{}{}, err
		}

		query := "UPDATE " + quoteIdentifierPath(repo.tableName) + // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers
			" SET status = $1::outbox_event_status, attempts = 0, last_error = NULL, updated_at = $2" +
			" WHERE id = $3 AND status = $4::outbox_event_status" + filter

		args := append([]any{outbox.OutboxStatusPending, time.Now().UTC(), id, outbox.OutboxStatusInvalid}, filterArgs...)

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
			return struct{}{}, outbox.ErrOutboxEventNotInvalid
		}

		return struct{}{}, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to requeue invalid outbox event", err)

		return fmt.Errorf("requeueing invalid event: %w", err)
	}

	return nil
}
