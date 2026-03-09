package outbox

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// Tx is the transactional handle used by CreateWithTx.
//
// It intentionally aliases *sql.Tx to keep the repository contract compatible
// with existing database/sql transaction orchestration and tenant resolvers.
// This avoids hidden adapter layers in write paths where tenant scoping runs
// inside the caller's transaction.
type Tx = *sql.Tx

// OutboxRepository defines persistence operations for outbox events.
type OutboxRepository interface {
	Create(ctx context.Context, event *OutboxEvent) (*OutboxEvent, error)
	CreateWithTx(ctx context.Context, tx Tx, event *OutboxEvent) (*OutboxEvent, error)
	ListPending(ctx context.Context, limit int) ([]*OutboxEvent, error)
	ListPendingByType(ctx context.Context, eventType string, limit int) ([]*OutboxEvent, error)
	ListTenants(ctx context.Context) ([]string, error)
	GetByID(ctx context.Context, id uuid.UUID) (*OutboxEvent, error)
	MarkPublished(ctx context.Context, id uuid.UUID, publishedAt time.Time) error
	MarkFailed(ctx context.Context, id uuid.UUID, errMsg string, maxAttempts int) error
	ListFailedForRetry(ctx context.Context, limit int, failedBefore time.Time, maxAttempts int) ([]*OutboxEvent, error)
	ResetForRetry(ctx context.Context, limit int, failedBefore time.Time, maxAttempts int) ([]*OutboxEvent, error)
	ResetStuckProcessing(ctx context.Context, limit int, processingBefore time.Time, maxAttempts int) ([]*OutboxEvent, error)
	MarkInvalid(ctx context.Context, id uuid.UUID, errMsg string) error
}
