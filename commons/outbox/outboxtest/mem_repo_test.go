//go:build unit

package outboxtest

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
)

// memOutboxRepo is a minimal in-memory implementation of outbox.OutboxRepository
// for use in contract tests. It enforces tenant isolation using tenant IDs
// extracted from context via outbox.TenantIDFromContext.
type memOutboxRepo struct {
	mu        sync.Mutex
	events    map[uuid.UUID]*outbox.OutboxEvent
	tenantIDs map[uuid.UUID]string // maps event ID to tenant ID for isolation
}

func newMemOutboxRepo() *memOutboxRepo {
	return &memOutboxRepo{
		events:    make(map[uuid.UUID]*outbox.OutboxEvent),
		tenantIDs: make(map[uuid.UUID]string),
	}
}

// extractTenantID gets the tenant ID from context (may be empty string).
func extractTenantID(ctx context.Context) string {
	tenantID, _ := outbox.TenantIDFromContext(ctx)
	return tenantID
}

func (m *memOutboxRepo) Create(ctx context.Context, event *outbox.OutboxEvent) (*outbox.OutboxEvent, error) {
	if event == nil {
		return nil, errors.New("event is nil")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	tenantID := extractTenantID(ctx)

	created := &outbox.OutboxEvent{
		ID:          event.ID,
		EventType:   strings.TrimSpace(event.EventType),
		AggregateID: event.AggregateID,
		Payload:     event.Payload,
		Status:      outbox.OutboxStatusPending,
		Attempts:    0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	m.events[created.ID] = created
	m.tenantIDs[created.ID] = tenantID

	return created, nil
}

func (m *memOutboxRepo) CreateWithTx(ctx context.Context, _ *sql.Tx, event *outbox.OutboxEvent) (*outbox.OutboxEvent, error) {
	return m.Create(ctx, event)
}

func (m *memOutboxRepo) GetByID(ctx context.Context, id uuid.UUID) (*outbox.OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tenantID := extractTenantID(ctx)
	e, ok := m.events[id]

	if !ok {
		return nil, sql.ErrNoRows
	}

	// Enforce tenant isolation: reject cross-tenant reads
	if tenantID != "" && m.tenantIDs[id] != tenantID {
		return nil, sql.ErrNoRows
	}

	cp := *e

	return &cp, nil
}

func (m *memOutboxRepo) ListPending(ctx context.Context, limit int) ([]*outbox.OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tenantID := extractTenantID(ctx)
	var result []*outbox.OutboxEvent

	for _, e := range m.events {
		if e.Status == outbox.OutboxStatusPending && (tenantID == "" || m.tenantIDs[e.ID] == tenantID) {
			cp := *e
			cp.Status = outbox.OutboxStatusProcessing
			m.events[cp.ID] = &cp
			result = append(result, &cp)

			if len(result) >= limit {
				break
			}
		}
	}

	return result, nil
}

func (m *memOutboxRepo) ListPendingByType(ctx context.Context, eventType string, limit int) ([]*outbox.OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tenantID := extractTenantID(ctx)
	var result []*outbox.OutboxEvent

	for _, e := range m.events {
		if e.Status == outbox.OutboxStatusPending && e.EventType == eventType && (tenantID == "" || m.tenantIDs[e.ID] == tenantID) {
			cp := *e
			cp.Status = outbox.OutboxStatusProcessing
			m.events[cp.ID] = &cp
			result = append(result, &cp)

			if len(result) >= limit {
				break
			}
		}
	}

	return result, nil
}

func (m *memOutboxRepo) ListTenants(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[string]struct{})
	var tenants []string

	for id, tenantID := range m.tenantIDs {
		if tenantID != "" {
			if _, ok := m.events[id]; ok {
				if _, already := seen[tenantID]; !already {
					seen[tenantID] = struct{}{}
					tenants = append(tenants, tenantID)
				}
			}
		}
	}

	return tenants, nil
}

func (m *memOutboxRepo) MarkPublished(ctx context.Context, id uuid.UUID, publishedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tenantID := extractTenantID(ctx)
	e, ok := m.events[id]

	if !ok || (tenantID != "" && m.tenantIDs[id] != tenantID) {
		return sql.ErrNoRows
	}

	if e.Status != outbox.OutboxStatusProcessing {
		return errors.New("state transition conflict")
	}
	e.Status = outbox.OutboxStatusPublished
	e.PublishedAt = &publishedAt
	e.UpdatedAt = time.Now().UTC()

	return nil
}

func (m *memOutboxRepo) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string, maxAttempts int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tenantID := extractTenantID(ctx)
	e, ok := m.events[id]

	if !ok || (tenantID != "" && m.tenantIDs[id] != tenantID) {
		return sql.ErrNoRows
	}

	if e.Status != outbox.OutboxStatusProcessing {
		return errors.New("state transition conflict")
	}

	e.Attempts++
	e.UpdatedAt = time.Now().UTC()

	sanitized := outbox.SanitizeErrorMessageForStorage(errMsg)

	if e.Attempts >= maxAttempts {
		e.Status = outbox.OutboxStatusInvalid
		e.LastError = "max dispatch attempts exceeded"
	} else {
		e.Status = outbox.OutboxStatusFailed
		e.LastError = sanitized
	}

	return nil
}

func (m *memOutboxRepo) ListFailedForRetry(_ context.Context, limit int, _ time.Time, maxAttempts int) ([]*outbox.OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []*outbox.OutboxEvent

	for _, e := range m.events {
		// Skip events at or above maxAttempts (they should be invalid, not retried)
		if e.Status == outbox.OutboxStatusFailed && (maxAttempts <= 0 || e.Attempts < maxAttempts) {
			cp := *e
			result = append(result, &cp)

			if len(result) >= limit {
				break
			}
		}
	}

	return result, nil
}

func (m *memOutboxRepo) ResetForRetry(_ context.Context, limit int, _ time.Time, maxAttempts int) ([]*outbox.OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []*outbox.OutboxEvent

	for _, e := range m.events {
		// Skip events at or above maxAttempts
		if e.Status == outbox.OutboxStatusFailed && (maxAttempts <= 0 || e.Attempts < maxAttempts) {
			e.Status = outbox.OutboxStatusProcessing
			e.UpdatedAt = time.Now().UTC()
			cp := *e
			result = append(result, &cp)

			if len(result) >= limit {
				break
			}
		}
	}

	return result, nil
}

func (m *memOutboxRepo) ResetStuckProcessing(_ context.Context, limit int, _ time.Time, maxAttempts int) ([]*outbox.OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []*outbox.OutboxEvent

	for _, e := range m.events {
		if e.Status == outbox.OutboxStatusProcessing {
			if e.Attempts+1 >= maxAttempts {
				// Exceeded max attempts - invalidate
				e.Attempts++
				e.Status = outbox.OutboxStatusInvalid
				e.LastError = "max dispatch attempts exceeded"
				e.UpdatedAt = time.Now().UTC()
			} else {
				// Reset to processing (increment attempts for the stuck attempt)
				e.Attempts++
				e.Status = outbox.OutboxStatusProcessing
				e.UpdatedAt = time.Now().UTC()
				cp := *e
				result = append(result, &cp)
			}

			if len(result) >= limit {
				break
			}
		}
	}

	return result, nil
}

func (m *memOutboxRepo) MarkInvalid(ctx context.Context, id uuid.UUID, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tenantID := extractTenantID(ctx)
	e, ok := m.events[id]

	if !ok || (tenantID != "" && m.tenantIDs[id] != tenantID) {
		return sql.ErrNoRows
	}

	if e.Status != outbox.OutboxStatusProcessing {
		return errors.New("state transition conflict")
	}

	e.Status = outbox.OutboxStatusInvalid
	e.LastError = errMsg
	e.UpdatedAt = time.Now().UTC()

	return nil
}

// memRepoFactory creates a fresh in-memory repository for each test.
func memRepoFactory(t *testing.T) outbox.OutboxRepository {
	t.Helper()

	return newMemOutboxRepo()
}

// TestRun_WithMemRepo exercises the outboxtest contract suite using an in-memory repository.
// Some tests are skipped due to complex semantics that need real DB behavior.
func TestRun_WithMemRepo(t *testing.T) {
	Run(t, memRepoFactory,
	)
}
