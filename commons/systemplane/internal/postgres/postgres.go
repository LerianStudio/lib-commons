// Package postgres implements the internal store.Store interface over Postgres
// with LISTEN/NOTIFY for change-feed delivery. It uses pgx/v5 for the dedicated
// LISTEN connection and database/sql for reads and writes.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	libOTEL "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Compile-time interface satisfaction check.
var _ store.Store = (*Store)(nil)

// safeIdentifierRe validates that a SQL identifier contains only safe characters.
// This prevents SQL injection in DDL statements where parameterized queries
// are not supported.
var safeIdentifierRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// tracerName is the OpenTelemetry instrumentation scope name.
const tracerName = "systemplane.postgres"

// defaultChannel is the LISTEN/NOTIFY channel used when Config.Channel is
// empty. Hardcoded because changing this is a wire-format break: every
// consumer of a given database must agree on the channel name.
const defaultChannel = "systemplane_changes"

// Config holds the parameters needed to construct a Postgres-backed Store.
type Config struct {
	// DB is the database/sql handle for reads and writes.
	DB *sql.DB

	// ListenDSN is the connection string used by pgx.Connect to establish a
	// dedicated LISTEN connection. Typically the same DSN used to open DB,
	// but must be provided explicitly because database/sql does not expose
	// its underlying DSN.
	ListenDSN string

	// Channel is the Postgres LISTEN/NOTIFY channel name.
	// Default: "systemplane_changes".
	Channel string

	// ChannelExplicit signals that the caller deliberately selected Channel
	// (typically via WithListenChannel). When false, New emits a warning if
	// Channel resolves to the default "systemplane_changes" — multiple
	// services sharing a database on that channel receive each other's
	// NOTIFY traffic, which is rarely what operators want.
	ChannelExplicit bool

	// Table is the Postgres table name.
	// Default: "systemplane_entries".
	Table string

	// Logger is the structured logger.
	Logger log.Logger

	// Telemetry is the OpenTelemetry provider for spans and metrics.
	Telemetry *libOTEL.Telemetry

	// TenantSchemaEnabled opts the backend into phase-2 schema. When false
	// (the default), ensureSchema keeps the legacy (namespace, key) primary
	// key intact so pre-tenant binaries can continue to use ON CONFLICT
	// (namespace, key). Tenant writes return ErrTenantSchemaNotEnabled.
	// When true, the legacy PK is dropped and a composite unique on
	// (namespace, key, tenant_id) is created.
	TenantSchemaEnabled bool
}

// Store implements [store.Store] over Postgres with LISTEN/NOTIFY.
type Store struct {
	cfg    Config
	cancel context.CancelFunc
	ctx    context.Context

	mu     sync.Mutex
	closed bool
}

// New creates a Postgres-backed Store. It validates the configuration,
// then creates the backing table and NOTIFY trigger idempotently.
func New(cfg Config) (*Store, error) {
	if cfg.DB == nil {
		return nil, store.ErrNilBackend
	}

	if cfg.ListenDSN == "" {
		return nil, errors.New("systemplane/postgres: ListenDSN is required")
	}

	usingDefaultChannel := cfg.Channel == ""
	if usingDefaultChannel {
		cfg.Channel = defaultChannel
	}

	if cfg.Table == "" {
		cfg.Table = "systemplane_entries"
	}

	if !safeIdentifierRe.MatchString(cfg.Channel) {
		return nil, fmt.Errorf("systemplane/postgres: unsafe channel name %q", cfg.Channel)
	}

	if !safeIdentifierRe.MatchString(cfg.Table) {
		return nil, fmt.Errorf("systemplane/postgres: unsafe table name %q", cfg.Table)
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &Store{
		cfg:    cfg,
		cancel: cancel,
		ctx:    ctx,
	}

	// Warn once when we're falling back to the default channel without an
	// explicit opt-in. Every service sharing this database on the default
	// channel will see each other's NOTIFY traffic; operators should isolate
	// via a per-service channel name. Only warn when the caller did NOT
	// explicitly select the channel (even if their explicit selection
	// happens to match the default — their intent was deliberate).
	if usingDefaultChannel && !cfg.ChannelExplicit && cfg.Logger != nil {
		s.logWarn(ctx, "Postgres LISTEN channel using default 'systemplane_changes'; multiple services sharing this database will receive each other's events. Consider setting WithListenChannel('<service_name>_systemplane_changes') to isolate changefeeds.",
			log.String("channel", cfg.Channel),
		)
	}

	if err := s.ensureSchema(context.Background()); err != nil {
		cancel()
		return nil, fmt.Errorf("systemplane/postgres: schema init: %w", err)
	}

	return s, nil
}

// List returns only the global (tenant_id='_global') entries from the Postgres
// table. Tenant-scoped overrides are deliberately excluded — callers wanting
// every row (including overrides) should use ListTenantValues. This filter
// preserves backward compatibility: pre-tenant consumers called List() to
// hydrate their global cache, and they must continue to see only globals
// even after the schema gains tenant rows (TRD §9 backward-compat matrix,
// TRD §4.5 hydration sequence).
func (s *Store) List(ctx context.Context) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span, finish := s.startSpan(ctx, "systemplane.postgres.list")
	defer finish()

	query := fmt.Sprintf( // #nosec G201 -- table name validated as Postgres identifier in New()
		`SELECT namespace, key, tenant_id, value, updated_at, updated_by FROM %s WHERE tenant_id = $1 ORDER BY namespace, key`,
		s.cfg.Table,
	)

	rows, err := s.cfg.DB.QueryContext(ctx, query, store.SentinelGlobal)
	if err != nil {
		libOTEL.HandleSpanError(span, "list query failed", err)

		return nil, fmt.Errorf("systemplane/postgres: list: %w", err)
	}
	defer rows.Close()

	var entries []store.Entry

	for rows.Next() {
		var e store.Entry

		if err := rows.Scan(&e.Namespace, &e.Key, &e.TenantID, &e.Value, &e.UpdatedAt, &e.UpdatedBy); err != nil {
			libOTEL.HandleSpanError(span, "list scan failed", err)

			return nil, fmt.Errorf("systemplane/postgres: list scan: %w", err)
		}

		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		libOTEL.HandleSpanError(span, "list rows iteration failed", err)

		return nil, fmt.Errorf("systemplane/postgres: list rows: %w", err)
	}

	// Return empty slice, not nil.
	if entries == nil {
		entries = []store.Entry{}
	}

	return entries, nil
}

// Get returns the global (tenant_id='_global') entry for the given
// (namespace, key). Tenant-scoped overrides are deliberately invisible to
// the legacy Get path — consumers that want a tenant override must call
// GetTenantValue explicitly. This preserves PRD AC1: Get(ns, key) must
// ignore tenant overrides even when they exist.
func (s *Store) Get(ctx context.Context, namespace, key string) (store.Entry, bool, error) {
	if s == nil || s.isClosed() {
		return store.Entry{}, false, store.ErrClosed
	}

	ctx, span, finish := s.startSpan(ctx, "systemplane.postgres.get",
		attribute.String("namespace", namespace),
		attribute.String("key", key),
	)
	defer finish()

	query := fmt.Sprintf( // #nosec G201 -- table name validated as Postgres identifier in New()
		`SELECT namespace, key, tenant_id, value, updated_at, updated_by FROM %s WHERE namespace = $1 AND key = $2 AND tenant_id = $3`,
		s.cfg.Table,
	)

	var e store.Entry

	err := s.cfg.DB.QueryRowContext(ctx, query, namespace, key, store.SentinelGlobal).
		Scan(&e.Namespace, &e.Key, &e.TenantID, &e.Value, &e.UpdatedAt, &e.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Entry{}, false, nil
	}

	if err != nil {
		libOTEL.HandleSpanError(span, "get query failed", err)

		return store.Entry{}, false, fmt.Errorf("systemplane/postgres: get: %w", err)
	}

	return e, true, nil
}

// Set persists a global entry using an INSERT ... ON CONFLICT UPDATE
// (upsert). The tenant_id column is populated explicitly with the '_global'
// sentinel — the column's DEFAULT would do the same, but writing it
// explicitly keeps the intent visible and future-proofs against a column
// rewrite that might remove the default.
//
// The ON CONFLICT target adapts to the configured schema phase:
//
//   - Phase 1 (TenantSchemaEnabled=false, default): targets the legacy
//     (namespace, key) primary key, matching the arbiter shape used by
//     pre-tenant lib-commons binaries (v5.0.x). This is the rolling-deploy
//     safe configuration.
//   - Phase 2 (TenantSchemaEnabled=true): targets the composite
//     (namespace, key, tenant_id) unique index.
//
// In both phases tenant_id is pinned to '_global' for writes issued through
// this method, so the collision domain is effectively (namespace, key).
// The backing trigger issues NOTIFY on the configured channel.
func (s *Store) Set(ctx context.Context, e store.Entry) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	if e.Namespace == "" {
		return errors.New("systemplane/postgres: namespace must not be empty")
	}

	if e.Key == "" {
		return errors.New("systemplane/postgres: key must not be empty")
	}

	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = time.Now().UTC()
	}

	ctx, span, finish := s.startSpan(ctx, "systemplane.postgres.set",
		attribute.String("namespace", e.Namespace),
		attribute.String("key", e.Key),
	)
	defer finish()

	conflictTarget := "(namespace, key)"
	if s.cfg.TenantSchemaEnabled {
		conflictTarget = "(namespace, key, tenant_id)"
	}

	query := fmt.Sprintf(`INSERT INTO %s (namespace, key, tenant_id, value, updated_at, updated_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT %s DO UPDATE
SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
		s.cfg.Table, conflictTarget, // #nosec G201 -- table name validated as Postgres identifier in New(); conflictTarget is a compile-time literal
	)

	if _, err := s.cfg.DB.ExecContext(ctx, query, e.Namespace, e.Key, store.SentinelGlobal, e.Value, e.UpdatedAt, e.UpdatedBy); err != nil {
		libOTEL.HandleSpanError(span, "set upsert failed", err)

		return fmt.Errorf("systemplane/postgres: set: %w", err)
	}

	return nil
}

// Close releases any resources held by the Store. Idempotent.
// Does NOT close s.cfg.DB; that is the caller's responsibility.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	s.cancel()

	return nil
}

// isClosed checks whether the store has been closed.
func (s *Store) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

// quoteIdentifier wraps a validated identifier in double quotes for use in SQL.
// The identifier MUST have already been validated against safeIdentifierRe.
func quoteIdentifier(name string) string {
	return `"` + name + `"`
}

// startSpan creates a child span if telemetry is configured, otherwise returns
// a noop span. Callers MUST defer finish() to end the span.
func (s *Store) startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span, func()) {
	noop := func() {}

	if s.cfg.Telemetry == nil {
		return ctx, trace.SpanFromContext(ctx), noop
	}

	tracer, err := s.cfg.Telemetry.Tracer(tracerName)
	if err != nil || tracer == nil {
		return ctx, trace.SpanFromContext(ctx), noop
	}

	ctx, span := tracer.Start(ctx, name,
		trace.WithAttributes(attrs...),
	)

	return ctx, span, func() { span.End() }
}

// logInfo emits an info-level log if a logger is configured.
func (s *Store) logInfo(ctx context.Context, msg string, fields ...log.Field) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Log(ctx, log.LevelInfo, msg, fields...)
	}
}

// logWarn emits a warn-level log if a logger is configured.
func (s *Store) logWarn(ctx context.Context, msg string, fields ...log.Field) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Log(ctx, log.LevelWarn, msg, fields...)
	}
}

// logDebug emits a debug-level log if a logger is configured. Used for
// low-signal reconnect chatter where the first incident is already logged
// at warn level.
func (s *Store) logDebug(ctx context.Context, msg string, fields ...log.Field) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Log(ctx, log.LevelDebug, msg, fields...)
	}
}

// LISTEN/NOTIFY subscription methods live in postgres_listen.go.
// Tenant-scoped Store methods live in postgres_tenant.go.
