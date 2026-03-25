// Package watcher provides a standalone settings revalidation component that
// periodically fetches connection settings from the Tenant Manager and applies
// them to active PostgreSQL connections. It runs independently of the RabbitMQ
// consumer, so services without RabbitMQ still get settings revalidation.
//
// MongoDB is intentionally excluded: the MongoDB Go driver does not support
// changing maxPoolSize after client creation, so there are no pool settings to
// revalidate at runtime.
package watcher

import (
	"context"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
)

// defaultInterval is the default revalidation interval.
const defaultInterval = 30 * time.Second

// SettingsWatcher periodically revalidates PostgreSQL connection pool settings
// for all connected tenants. It runs independently of the RabbitMQ consumer,
// so services without RabbitMQ still get settings revalidation.
//
// Multiple PostgreSQL managers can be registered (e.g., one per module such as
// onboarding and transaction). Each manager is revalidated independently.
type SettingsWatcher struct {
	client   *client.Client
	service  string
	interval time.Duration
	managers []*tmpostgres.Manager
	logger   *logcompat.Logger
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

// Option configures a SettingsWatcher.
type Option func(*SettingsWatcher)

// WithPostgresManager appends a PostgreSQL manager for settings revalidation.
// Multiple managers can be registered (e.g., one for onboarding and one for
// transaction). Each will be revalidated independently.
func WithPostgresManager(p *tmpostgres.Manager) Option {
	return func(w *SettingsWatcher) {
		if p != nil {
			w.managers = append(w.managers, p)
		}
	}
}

// WithInterval sets the revalidation interval (minimum 1 second).
func WithInterval(d time.Duration) Option {
	return func(w *SettingsWatcher) {
		if d >= time.Second {
			w.interval = d
		}
	}
}

// WithLogger sets the logger for the watcher.
func WithLogger(l libLog.Logger) Option {
	return func(w *SettingsWatcher) {
		w.logger = logcompat.New(l)
	}
}

// NewSettingsWatcher creates a new SettingsWatcher. The watcher does not start
// automatically; call Start to begin the revalidation loop.
func NewSettingsWatcher(c *client.Client, service string, opts ...Option) *SettingsWatcher {
	w := &SettingsWatcher{
		client:   c,
		service:  service,
		interval: defaultInterval,
		logger:   logcompat.New(nil),
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Start launches the background revalidation goroutine. It returns immediately.
// The goroutine runs until Stop is called or the parent context is cancelled.
// Calling Start on an already-started watcher is a no-op.
func (w *SettingsWatcher) Start(ctx context.Context) {
	if len(w.managers) == 0 {
		return // no-op: nothing to revalidate
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cancel != nil {
		return // already started
	}

	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})

	go w.loop(ctx)
}

// Stop cancels the background goroutine and waits for it to finish.
// After Stop returns the watcher can be re-started with Start.
func (w *SettingsWatcher) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	done := w.done
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if done != nil {
		<-done
	}

	w.mu.Lock()
	w.cancel = nil
	w.done = nil
	w.mu.Unlock()
}

// loop is the background goroutine that periodically revalidates settings.
func (w *SettingsWatcher) loop(ctx context.Context) {
	defer close(w.done)

	w.logger.InfofCtx(ctx, "settings watcher started (interval=%s)", w.interval)

	// Immediate revalidation on startup so settings changed while the
	// service was down are applied without waiting for the first tick.
	w.revalidate(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.InfoCtx(ctx, "settings watcher stopped: context cancelled")
			return
		case <-ticker.C:
			w.revalidate(ctx)
		}
	}
}

// revalidate fetches current settings from the Tenant Manager for each
// connected tenant and applies any changed connection pool settings.
func (w *SettingsWatcher) revalidate(ctx context.Context) {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if w.logger != nil {
		logger = w.logger
	}

	ctx, span := tracer.Start(ctx, "watcher.settings_watcher.revalidate")
	defer span.End()

	tenantIDs := w.collectTenantIDs()
	if len(tenantIDs) == 0 {
		return
	}

	var revalidated int

	for _, tenantID := range tenantIDs {
		config, err := w.client.GetTenantConfig(ctx, tenantID, w.service)
		if err != nil {
			if core.IsTenantSuspendedError(err) || core.IsTenantPurgedError(err) {
				w.handleSuspendedTenant(ctx, tenantID, logger)
				continue
			}

			logger.WarnfCtx(ctx, "settings watcher: failed to fetch config for tenant %s: %v", tenantID, err)

			continue
		}

		for _, mgr := range w.managers {
			mgr.ApplyConnectionSettings(tenantID, config)
		}

		revalidated++
	}

	if revalidated > 0 {
		logger.InfofCtx(ctx, "settings watcher: revalidated connection settings for %d/%d connected tenants", revalidated, len(tenantIDs))
	}
}

// collectTenantIDs returns the deduplicated union of tenant IDs from all
// registered PostgreSQL managers.
func (w *SettingsWatcher) collectTenantIDs() []string {
	if len(w.managers) == 0 {
		return nil
	}

	seen := make(map[string]struct{})

	for _, mgr := range w.managers {
		for _, id := range mgr.ConnectedTenantIDs() {
			seen[id] = struct{}{}
		}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}

	return ids
}

// handleSuspendedTenant closes the PostgreSQL connections for a tenant that has
// been suspended or purged across all registered managers. The consumer's sync
// loop will handle removing the tenant from its own state independently.
func (w *SettingsWatcher) handleSuspendedTenant(ctx context.Context, tenantID string, logger *logcompat.Logger) {
	logger.WarnfCtx(ctx, "settings watcher: tenant %s suspended/purged, closing connections", tenantID)

	for _, mgr := range w.managers {
		if err := mgr.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "settings watcher: failed to close PostgreSQL connection for suspended tenant %s: %v", tenantID, err)
		}
	}
}
