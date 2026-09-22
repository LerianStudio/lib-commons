package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
)

// DeletePublishedBefore deletes at most limit PUBLISHED events created before
// the cutoff, oldest first, skipping event types listed in keepEventTypes, and
// returns how many rows were deleted.
//
// The age bound reads created_at rather than published_at: the
// (status, created_at) index serves it, and a PUBLISHED row's published_at is
// never earlier than its created_at, so the bound only ever keeps a row longer.
// PENDING, PROCESSING, FAILED and INVALID rows are never selected. A limit <= 0
// deletes nothing, so the statement is always bounded.
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

	tracer := tracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.delete_published_outbox_events")
	defer span.End()

	if missing, err := repo.tenantOutboxTableMissing(ctx); err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to check outbox table presence", err)

		return 0, fmt.Errorf("deleting published events: %w", err)
	} else if missing {
		return 0, nil
	}

	deleted, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (int64, error) {
		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return 0, tenantErr
		}

		table := quoteIdentifierPath(repo.tableName)
		selection := "SELECT id FROM " + table + " WHERE status = $1::outbox_event_status AND created_at < $2"
		args := []any{outbox.OutboxStatusPublished, before}

		if keep := normalizeEventTypes(keepEventTypes); len(keep) > 0 {
			args = append(args, keep)
			selection += fmt.Sprintf(" AND NOT (event_type = ANY($%d::text[]))", len(args))
		}

		filter, filterArgs, filterErr := repo.tenantFilterClause(len(args)+1, tenantID)
		if filterErr != nil {
			return 0, filterErr
		}

		args = append(args, filterArgs...)
		selection += filter + fmt.Sprintf(" ORDER BY created_at ASC LIMIT $%d", len(args)+1)
		args = append(args, limit)

		// The outer tenant filter reuses the inner placeholder: in column-per-tenant
		// mode the key is (tenant_id, id), so an id alone does not name one row.
		query := "DELETE FROM " + table + " WHERE id IN (" + selection + ")" + filter // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers

		result, execErr := tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			return 0, fmt.Errorf("executing delete: %w", execErr)
		}

		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return 0, fmt.Errorf("reading deleted rows: %w", rowsErr)
		}

		return affected, nil
	})
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to delete published outbox events", err)

		return 0, fmt.Errorf("deleting published events: %w", err)
	}

	return deleted, nil
}
