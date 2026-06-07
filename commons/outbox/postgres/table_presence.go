package postgres

import (
	"context"
	"sync"
	"time"
)

// defaultTablePresenceTTL bounds how long a positive/negative outbox-table
// presence result is trusted before re-probing. In pool-per-tenant mode the
// schema discoverer's information_schema check is unavailable, so this cheap
// per-tenant guard prevents hammering a tenant DB with 42P01 ("relation does
// not exist") errors every dispatch cycle when the outbox table is missing.
const defaultTablePresenceTTL = 60 * time.Second

// tablePresenceProbe reports whether the outbox table exists for a tenant.
type tablePresenceProbe func(ctx context.Context, tenantID string) (bool, error)

// tablePresenceEntry caches a presence result with an expiry.
type tablePresenceEntry struct {
	present bool
	expires time.Time
}

// tablePresenceGuard caches per-tenant outbox-table presence with a TTL.
// Errors are never cached (they invalidate any pending decision), so a
// transient failure re-probes on the next call rather than being treated as a
// durable "missing table".
type tablePresenceGuard struct {
	probe    tablePresenceProbe
	ttl      time.Duration
	mu       sync.Mutex
	byTenant map[string]tablePresenceEntry
}

func newTablePresenceGuard(probe tablePresenceProbe, ttl time.Duration) *tablePresenceGuard {
	if probe == nil {
		// No probe means no guard: callers nil-check repo.tablePresence and fall
		// back to ungated dispatch, which is safer than panicking in present().
		return nil
	}

	if ttl <= 0 {
		ttl = defaultTablePresenceTTL
	}

	return &tablePresenceGuard{
		probe:    probe,
		ttl:      ttl,
		byTenant: make(map[string]tablePresenceEntry),
	}
}

// present reports whether the outbox table exists for tenantID, serving cached
// results within the TTL and re-probing afterward. Probe errors are surfaced
// and not cached.
func (g *tablePresenceGuard) present(ctx context.Context, tenantID string) (bool, error) {
	now := time.Now()

	g.mu.Lock()
	if entry, ok := g.byTenant[tenantID]; ok && now.Before(entry.expires) {
		g.mu.Unlock()
		return entry.present, nil
	}
	g.mu.Unlock()

	present, err := g.probe(ctx, tenantID)
	if err != nil {
		g.mu.Lock()
		delete(g.byTenant, tenantID)
		g.mu.Unlock()

		return false, err
	}

	g.mu.Lock()
	g.byTenant[tenantID] = tablePresenceEntry{present: present, expires: now.Add(g.ttl)}
	g.mu.Unlock()

	return present, nil
}
