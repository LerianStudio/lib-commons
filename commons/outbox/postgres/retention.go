package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
)

var (
	_ outbox.PublishedPurger       = (*Repository)(nil)
	_ outbox.PublishedTenantLister = (*Repository)(nil)
	_ outbox.InvalidPurger         = (*Repository)(nil)
)

// agedRows names the rows one retention deletes: a terminal status and the
// column its age is read from.
type agedRows struct {
	status string
	since  string
}

var (
	agedPublished = agedRows{status: outbox.OutboxStatusPublished, since: "created_at"}
	// INVALID is terminal and both transitions into it stamp updated_at, so
	// updated_at is the time the row became INVALID; (status, updated_at) is indexed.
	agedInvalid = agedRows{status: outbox.OutboxStatusInvalid, since: "updated_at"}
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
	return repo.deleteAgedBefore(ctx, agedPublished, before, keepEventTypes, limit)
}

// DeleteInvalidBefore deletes at most limit INVALID events that became INVALID
// (updated_at) before the cutoff, oldest first, skipping event types listed in
// keepEventTypes, and returns how many rows were deleted.
func (repo *Repository) DeleteInvalidBefore(
	ctx context.Context,
	before time.Time,
	keepEventTypes []string,
	limit int,
) (int64, error) {
	return repo.deleteAgedBefore(ctx, agedInvalid, before, keepEventTypes, limit)
}

func (repo *Repository) deleteAgedBefore(
	ctx context.Context,
	rows agedRows,
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

	label := strings.ToLower(rows.status)

	ctx, span := tracer.Start(ctx, "postgres.delete_"+label+"_outbox_events")
	defer span.End()

	if missing, err := repo.tenantOutboxTableMissing(ctx); err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to check outbox table presence", err)

		return 0, fmt.Errorf("deleting %s events: %w", label, err)
	} else if missing {
		return 0, nil
	}

	deleted, err := withTenantTxOrExisting(repo, ctx, nil, func(tx *sql.Tx) (int64, error) {
		tenantID, tenantErr := repo.tenantIDFromContext(ctx)
		if tenantErr != nil {
			return 0, tenantErr
		}

		table := quoteIdentifierPath(repo.tableName)
		where, args := rows.clause(before, keepEventTypes)
		selection := "SELECT id FROM " + table + where

		filter, filterArgs, filterErr := repo.tenantFilterClause(len(args)+1, tenantID)
		if filterErr != nil {
			return 0, filterErr
		}

		args = append(args, filterArgs...)
		selection += filter + fmt.Sprintf(" ORDER BY %s ASC, id ASC LIMIT $%d", rows.since, len(args)+1)
		args = append(args, limit)

		// The outer tenant filter reuses the inner placeholder: in column-per-tenant
		// mode the key is (tenant_id, id), so an id alone does not name one row.
		query := "DELETE FROM " + table + " WHERE id IN (" + selection + ")" + filter // #nosec G202 -- table name validated at construction; quoteIdentifierPath escapes identifiers

		// withTenantTxOrExisting begins the transaction on a context bounded by
		// the transaction timeout, and database/sql rolls the transaction back
		// when that context expires, but only once in-flight statements return:
		// a DELETE waiting for a transaction that still holds a lock on a
		// selected row is not interrupted by it. The statement therefore takes
		// the same bound itself when the caller set no deadline.
		execCtx := ctx

		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc

			execCtx, cancel = context.WithTimeout(ctx, repo.transactionTimeout)
			defer cancel()
		}

		result, execErr := tx.ExecContext(execCtx, query, args...)
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
		libOpentelemetry.HandleSpanError(span, "failed to delete "+label+" outbox events", err)

		return 0, fmt.Errorf("deleting %s events: %w", label, err)
	}

	return deleted, nil
}

// ListTenantsWithPublishedBefore lists the tenants DeletePublishedBefore would
// delete from, for column-per-tenant repositories, whose dispatch discovery
// skips idle tenants. Other modes discover every tenant and return none.
func (repo *Repository) ListTenantsWithPublishedBefore(
	ctx context.Context,
	before time.Time,
	keepEventTypes []string,
) ([]string, error) {
	return repo.listTenantsAgedBefore(ctx, agedPublished, before, keepEventTypes)
}

// ListTenantsWithInvalidBefore lists the tenants DeleteInvalidBefore would
// delete from, for column-per-tenant repositories. Other modes return none.
func (repo *Repository) ListTenantsWithInvalidBefore(
	ctx context.Context,
	before time.Time,
	keepEventTypes []string,
) ([]string, error) {
	return repo.listTenantsAgedBefore(ctx, agedInvalid, before, keepEventTypes)
}

func (repo *Repository) listTenantsAgedBefore(
	ctx context.Context,
	rows agedRows,
	before time.Time,
	keepEventTypes []string,
) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !repo.initialized() {
		return nil, ErrRepositoryNotInitialized
	}

	if repo.tenantColumn == "" {
		return nil, nil
	}

	tracer := tracerFromContext(ctx)

	label := strings.ToLower(rows.status)

	ctx, span := tracer.Start(ctx, "postgres.list_outbox_tenants_with_"+label)
	defer span.End()

	tenants, err := repo.queryTenantsAgedBefore(ctx, rows, before, keepEventTypes)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list tenants with "+label+" events", err)

		return nil, fmt.Errorf("listing tenants with %s events: %w", label, err)
	}

	return tenants, nil
}

func (repo *Repository) queryTenantsAgedBefore(
	ctx context.Context,
	rows agedRows,
	before time.Time,
	keepEventTypes []string,
) ([]string, error) {
	db, err := repo.primaryDB(ctx)
	if err != nil {
		return nil, err
	}

	queryCtx, cancel := context.WithTimeout(ctx, repo.transactionTimeout)
	defer cancel()

	column := quoteIdentifier(repo.tenantColumn)
	where, args := rows.clause(before, keepEventTypes)

	result, err := db.QueryContext(queryCtx, "SELECT DISTINCT "+column+" FROM "+quoteIdentifierPath(repo.tableName)+where, args...) // #nosec G202 -- table/column names validated at construction; quote functions escape identifiers
	if err != nil {
		return nil, fmt.Errorf("querying tenant ids: %w", err)
	}

	return scanTenantIDs(result)
}

// clause selects the rows a retention may delete: of its status, aged past the
// cutoff, of a type not kept.
func (rows agedRows) clause(before time.Time, keepEventTypes []string) (string, []any) {
	where := " WHERE status = $1::outbox_event_status AND " + rows.since + " < $2"
	args := []any{rows.status, before}

	if keep := normalizeEventTypes(keepEventTypes); len(keep) > 0 {
		args = append(args, keep)
		where += fmt.Sprintf(" AND NOT (event_type = ANY($%d::text[]))", len(args))
	}

	return where, args
}
