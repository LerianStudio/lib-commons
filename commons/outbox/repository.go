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

// PublishedPurger is an additive capability for the dispatcher's retention
// sweep. It is kept separate from OutboxRepository so existing implementations
// are not forced to grow a method; NewDispatcher refuses to enable retention
// on a repository that lacks it.
type PublishedPurger interface {
	// DeletePublishedBefore deletes at most limit PUBLISHED events created
	// before the cutoff, oldest first, skipping every event whose type is in
	// keepEventTypes, and returns how many were deleted. The age bound uses
	// created_at: a PUBLISHED event's published_at is never earlier than its
	// created_at, so the bound is conservative and served by the
	// (status, created_at) index. PENDING, PROCESSING, FAILED and INVALID
	// events are never deleted. A limit <= 0 deletes nothing and returns 0.
	DeletePublishedBefore(ctx context.Context, before time.Time, keepEventTypes []string, limit int) (int64, error)
}

// PublishedTenantLister lets retention reach tenants that dispatch discovery
// skips for lack of work: the dispatcher calls it once per
// RetentionSweepInterval and sweeps every tenant it returns.
type PublishedTenantLister interface {
	// ListTenantsWithPublishedBefore returns the tenants holding a PUBLISHED
	// event created before the cutoff whose type is not in keepEventTypes.
	ListTenantsWithPublishedBefore(ctx context.Context, before time.Time, keepEventTypes []string) ([]string, error)
}

// InvalidPurger is the capability WithRetentionInvalid needs: the INVALID
// counterpart of PublishedPurger and PublishedTenantLister, with the same
// keep-type, limit and tenant semantics.
type InvalidPurger interface {
	// DeleteInvalidBefore deletes at most limit INVALID events that became
	// INVALID before the cutoff, oldest first, and returns how many it deleted.
	DeleteInvalidBefore(ctx context.Context, before time.Time, keepEventTypes []string, limit int) (int64, error)
	// ListTenantsWithInvalidBefore returns the tenants DeleteInvalidBefore would
	// delete from; a repository whose discovery lists every tenant returns none.
	ListTenantsWithInvalidBefore(ctx context.Context, before time.Time, keepEventTypes []string) ([]string, error)
}

// IdempotentWriter is a narrow, opt-in contract for content-addressed idempotent
// outbox writes. It is deliberately kept separate from OutboxRepository so that
// only callers that need replay-safe upserts depend on it, and existing
// OutboxRepository implementations are not forced to grow a new method.
type IdempotentWriter interface {
	CreateIdempotentWithTx(ctx context.Context, tx Tx, event *OutboxEvent) (*OutboxEvent, error)
}

// MultiTypePendingRepository is an additive capability for atomically claiming
// pending events from several event types. Implementations preserve the caller's
// event-type priority and FIFO order within each type.
type MultiTypePendingRepository interface {
	ListPendingByTypes(ctx context.Context, eventTypes []string, limit int) ([]*OutboxEvent, error)
}

// TransactionalBatchWriter is a narrow, opt-in contract for storing multiple
// outbox events atomically in one set-wise statement inside a caller transaction.
// Implementations preserve input order in the returned slice. An empty input is
// a no-op that returns a non-nil empty slice.
type TransactionalBatchWriter interface {
	CreateManyWithTx(ctx context.Context, tx Tx, events []*OutboxEvent) ([]*OutboxEvent, error)
}
