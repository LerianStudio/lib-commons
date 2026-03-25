// Package watcher provides a standalone settings revalidation component that
// periodically fetches connection settings from the Tenant Manager and applies
// them to active PostgreSQL and MongoDB connections. It runs independently of
// the RabbitMQ consumer, so services without RabbitMQ still get settings
// revalidation.
package watcher

import (
	"context"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
)

// defaultInterval is the default revalidation interval.
const defaultInterval = 30 * time.Second

// SettingsWatcher periodically revalidates connection settings for all
// connected tenants. It runs independently of the RabbitMQ consumer,
// so services without RabbitMQ still get settings revalidation.
type SettingsWatcher struct {
	client   *client.Client
	service  string
	interval time.Duration
	postgres *tmpostgres.Manager
	mongo    *tmmongo.Manager
	logger   *logcompat.Logger
	cancel   context.CancelFunc
	done     chan struct{}
}

// Option configures a SettingsWatcher.
type Option func(*SettingsWatcher)

// WithPostgresManager sets the PostgreSQL manager for settings revalidation.
func WithPostgresManager(p *tmpostgres.Manager) Option {
	return func(w *SettingsWatcher) { w.postgres = p }
}

// WithMongoManager sets the MongoDB manager for settings revalidation.
func WithMongoManager(m *tmmongo.Manager) Option {
	return func(w *SettingsWatcher) { w.mongo = m }
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
func (w *SettingsWatcher) Start(ctx context.Context) {
	if w.postgres == nil && w.mongo == nil {
		return // no-op: nothing to revalidate
	}

	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})

	go w.loop(ctx)
}

// Stop cancels the background goroutine and waits for it to finish.
func (w *SettingsWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}

	if w.done != nil {
		<-w.done
	}
}

// loop is the background goroutine that periodically revalidates settings.
func (w *SettingsWatcher) loop(ctx context.Context) {
	defer close(w.done)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.logger.InfofCtx(ctx, "settings watcher started (interval=%s)", w.interval)

	for {
		select {
		case <-ticker.C:
			w.revalidate(ctx)
		case <-ctx.Done():
			w.logger.InfoCtx(ctx, "settings watcher stopped: context cancelled")
			return
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

		if w.postgres != nil {
			w.postgres.ApplyConnectionSettings(tenantID, config)
		}

		if w.mongo != nil {
			w.mongo.ApplyConnectionSettings(tenantID, config)
		}

		revalidated++
	}

	if revalidated > 0 {
		logger.InfofCtx(ctx, "settings watcher: revalidated connection settings for %d/%d connected tenants", revalidated, len(tenantIDs))
	}
}

// collectTenantIDs returns the union of tenant IDs from all configured managers.
func (w *SettingsWatcher) collectTenantIDs() []string {
	seen := make(map[string]struct{})

	if w.postgres != nil {
		for _, id := range w.postgres.ConnectedTenantIDs() {
			seen[id] = struct{}{}
		}
	}

	if w.mongo != nil {
		for _, id := range w.mongo.ConnectedTenantIDs() {
			seen[id] = struct{}{}
		}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}

	return ids
}

// handleSuspendedTenant closes connections in the managers for a tenant
// that has been suspended or purged. The consumer's sync loop will handle
// removing the tenant from its own state independently.
func (w *SettingsWatcher) handleSuspendedTenant(ctx context.Context, tenantID string, logger *logcompat.Logger) {
	logger.WarnfCtx(ctx, "settings watcher: tenant %s suspended/purged, closing connections", tenantID)

	if w.postgres != nil {
		if err := w.postgres.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "settings watcher: failed to close PostgreSQL connection for suspended tenant %s: %v", tenantID, err)
		}
	}

	if w.mongo != nil {
		if err := w.mongo.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "settings watcher: failed to close MongoDB connection for suspended tenant %s: %v", tenantID, err)
		}
	}
}
