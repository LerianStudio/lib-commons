// Package postgres implements the internal store.Store interface over Postgres
// with LISTEN/NOTIFY for change-feed delivery. It uses pgx/v5 for the dedicated
// LISTEN connection and database/sql for reads and writes.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LerianStudio/lib-commons/v5/commons/backoff"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Compile-time interface satisfaction check.
var _ store.Store = (*Store)(nil)

// safeIdentifierRe validates that a SQL identifier contains only safe characters.
// This prevents SQL injection in DDL statements where parameterized queries
// are not supported.
var safeIdentifierRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// backoffBase is the starting delay for LISTEN reconnection backoff.
const backoffBase = 500 * time.Millisecond

// backoffCap is the maximum delay between LISTEN reconnection attempts.
const backoffCap = 30 * time.Second

// closeTimeout is the deadline for best-effort cleanup on connection close.
const closeTimeout = 5 * time.Second

// tracerName is the OpenTelemetry instrumentation scope name.
const tracerName = "systemplane.postgres"

// sentinelGlobal is the tenant_id value used for shared (non-tenant-scoped)
// rows. It is chosen so that no valid tenant ID can collide: per
// commons/tenant-manager/core/validation.go the tenant ID regex is
// ^[a-zA-Z0-9][a-zA-Z0-9_-]*$, so an identifier starting with "_" is illegal
// for a real tenant and safe as a sentinel. Mirrors TRD §3.1.
const sentinelGlobal = "_global"

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

	// Table is the Postgres table name.
	// Default: "systemplane_entries".
	Table string

	// Logger is the structured logger.
	Logger log.Logger

	// Telemetry is the OpenTelemetry provider for spans and metrics.
	Telemetry *opentelemetry.Telemetry
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

	if cfg.Channel == "" {
		cfg.Channel = "systemplane_changes"
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

	rows, err := s.cfg.DB.QueryContext(ctx, query, sentinelGlobal)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")

		return nil, fmt.Errorf("systemplane/postgres: list: %w", err)
	}
	defer rows.Close()

	var entries []store.Entry

	for rows.Next() {
		var e store.Entry

		if err := rows.Scan(&e.Namespace, &e.Key, &e.TenantID, &e.Value, &e.UpdatedAt, &e.UpdatedBy); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "scan failed")

			return nil, fmt.Errorf("systemplane/postgres: list scan: %w", err)
		}

		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "rows iteration failed")

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

	err := s.cfg.DB.QueryRowContext(ctx, query, namespace, key, sentinelGlobal).
		Scan(&e.Namespace, &e.Key, &e.TenantID, &e.Value, &e.UpdatedAt, &e.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Entry{}, false, nil
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")

		return store.Entry{}, false, fmt.Errorf("systemplane/postgres: get: %w", err)
	}

	return e, true, nil
}

// Set persists a global entry using an INSERT ... ON CONFLICT UPDATE
// (upsert). The tenant_id column is populated explicitly with the '_global'
// sentinel — the column's DEFAULT would do the same, but writing it
// explicitly keeps the intent visible and future-proofs against a column
// rewrite that might remove the default. The ON CONFLICT target is the
// composite (namespace, key, tenant_id) unique index; with tenant_id pinned
// to the sentinel the effective collision domain is the same as the
// pre-tenant (namespace, key) PK. The backing trigger issues NOTIFY on the
// configured channel.
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

	query := fmt.Sprintf(`INSERT INTO %s (namespace, key, tenant_id, value, updated_at, updated_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (namespace, key, tenant_id) DO UPDATE
SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
		s.cfg.Table, // #nosec G201 -- table name validated as Postgres identifier in New()
	)

	if _, err := s.cfg.DB.ExecContext(ctx, query, e.Namespace, e.Key, sentinelGlobal, e.Value, e.UpdatedAt, e.UpdatedBy); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "upsert failed")

		return fmt.Errorf("systemplane/postgres: set: %w", err)
	}

	return nil
}

// Subscribe blocks until ctx is cancelled, listening on the Postgres
// LISTEN/NOTIFY channel and invoking handler for each change event.
// Uses pgx.Connect with the ListenDSN for a dedicated connection.
// Safe to invoke multiple times concurrently; each call is its own subscription.
func (s *Store) Subscribe(ctx context.Context, handler func(store.Event)) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	// Merge the store-level cancellation with the caller's context so that
	// Close() also terminates running subscriptions.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	var attempt int

	for {
		err := s.listenLoop(ctx, handler)
		if err == nil {
			return nil
		}

		// Context cancelled (either caller or store close) -- clean exit.
		if ctx.Err() != nil {
			return nil
		}

		s.logWarn(ctx, "LISTEN connection lost, reconnecting",
			log.Int("attempt", attempt),
			log.Err(err),
		)

		delay := min(backoff.ExponentialWithJitter(backoffBase, attempt), backoffCap)

		attempt++

		if err := backoff.WaitContext(ctx, delay); err != nil {
			return nil
		}
	}
}

// listenLoop establishes a single pgx connection, issues LISTEN, and processes
// notifications until the context is cancelled or the connection fails.
func (s *Store) listenLoop(ctx context.Context, handler func(store.Event)) error {
	conn, err := pgx.Connect(ctx, s.cfg.ListenDSN)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cleanCancel()

		_ = conn.Close(cleanCtx)
	}()

	listenStmt := "LISTEN " + quoteIdentifier(s.cfg.Channel)

	if _, err := conn.Exec(ctx, listenStmt); err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	s.logInfo(ctx, "LISTEN connection established",
		log.String("channel", s.cfg.Channel),
	)

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return fmt.Errorf("wait: %w", err)
		}

		evt, err := parseNotifyPayload(notification.Payload)
		if err != nil {
			s.logWarn(ctx, "failed to decode NOTIFY payload",
				log.String("payload", truncateString(notification.Payload, 200)),
				log.Err(err),
			)

			continue
		}

		s.invokeHandler(ctx, handler, evt)
	}
}

// invokeHandler calls the user's handler with panic recovery.
func (s *Store) invokeHandler(ctx context.Context, handler func(store.Event), evt store.Event) {
	defer runtime.RecoverAndLogWithContext(ctx, s.cfg.Logger, "systemplane", "postgres.handler")

	handler(evt)
}

// notifyPayload is the JSON shape emitted by the pg_notify trigger.
// The TenantID field is populated by post-Task-3 installations; older
// payloads (produced before the tenant column was added) lack the key and
// decode as an empty string, which the Client layer treats as "unknown
// tenant" and routes through the global path — that graceful degradation
// is intentional so a mid-rollout mix of upgraded and legacy rows cannot
// wedge a subscriber.
type notifyPayload struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	TenantID  string `json:"tenant_id"`
}

// parseNotifyPayload converts a NOTIFY JSON payload into a store.Event.
// A missing tenant_id field is not an error — it decodes as "" and the
// caller is responsible for treating the absence as "pre-tenant payload".
// Real post-Task-3 payloads always carry the sentinel '_global' for global
// writes, so an empty TenantID on a fresh install should not occur.
func parseNotifyPayload(data string) (store.Event, error) {
	var p notifyPayload

	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return store.Event{}, fmt.Errorf("unmarshal: %w", err)
	}

	return store.Event{
		Namespace: p.Namespace,
		Key:       p.Key,
		TenantID:  p.TenantID,
	}, nil
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

// truncateString returns s unchanged if len(s) <= maxLen, otherwise returns
// the first maxLen bytes followed by "...". Used for defense-in-depth when
// logging untrusted payloads.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}

	return s[:maxLen] + "..."
}

// Tenant-scoped Store methods live in postgres_tenant.go.
