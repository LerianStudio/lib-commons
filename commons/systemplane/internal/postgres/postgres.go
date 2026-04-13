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

	"github.com/LerianStudio/lib-commons/v4/commons/backoff"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/runtime"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

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

// ensureSchema creates the table and trigger in a single transaction.
func (s *Store) ensureSchema(ctx context.Context) error {
	tx, err := s.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	createTable := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		namespace   TEXT NOT NULL,
		key         TEXT NOT NULL,
		value       JSONB NOT NULL,
		updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_by  TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (namespace, key)
	)`, s.cfg.Table)

	if _, err := tx.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// The trigger function references the channel name literally. We have
	// already validated it against safeIdentifierRe, so direct interpolation
	// is safe here.
	createFunc := fmt.Sprintf(`CREATE OR REPLACE FUNCTION systemplane_notify() RETURNS TRIGGER AS $$
BEGIN
	PERFORM pg_notify('%s', json_build_object('namespace', NEW.namespace, 'key', NEW.key)::text);
	RETURN NEW;
END;
$$ LANGUAGE plpgsql`, s.cfg.Channel)

	if _, err := tx.ExecContext(ctx, createFunc); err != nil {
		return fmt.Errorf("create function: %w", err)
	}

	dropTrigger := fmt.Sprintf(`DROP TRIGGER IF EXISTS systemplane_notify_trigger ON %s`, s.cfg.Table)
	if _, err := tx.ExecContext(ctx, dropTrigger); err != nil {
		return fmt.Errorf("drop trigger: %w", err)
	}

	createTrigger := fmt.Sprintf(`CREATE TRIGGER systemplane_notify_trigger
AFTER INSERT OR UPDATE ON %s
FOR EACH ROW EXECUTE FUNCTION systemplane_notify()`, s.cfg.Table)

	if _, err := tx.ExecContext(ctx, createTrigger); err != nil {
		return fmt.Errorf("create trigger: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// List returns all entries from the Postgres table.
func (s *Store) List(ctx context.Context) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span := s.startSpan(ctx, "systemplane.postgres.list")
	defer span.End()

	query := fmt.Sprintf(
		`SELECT namespace, key, value, updated_at, updated_by FROM %s ORDER BY namespace, key`,
		s.cfg.Table,
	)

	rows, err := s.cfg.DB.QueryContext(ctx, query)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")

		return nil, fmt.Errorf("systemplane/postgres: list: %w", err)
	}
	defer rows.Close()

	var entries []store.Entry

	for rows.Next() {
		var e store.Entry

		if err := rows.Scan(&e.Namespace, &e.Key, &e.Value, &e.UpdatedAt, &e.UpdatedBy); err != nil {
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

// Get returns a single entry by namespace and key.
func (s *Store) Get(ctx context.Context, namespace, key string) (store.Entry, bool, error) {
	if s == nil || s.isClosed() {
		return store.Entry{}, false, store.ErrClosed
	}

	ctx, span := s.startSpan(ctx, "systemplane.postgres.get",
		attribute.String("namespace", namespace),
		attribute.String("key", key),
	)
	defer span.End()

	query := fmt.Sprintf(
		`SELECT namespace, key, value, updated_at, updated_by FROM %s WHERE namespace = $1 AND key = $2`,
		s.cfg.Table,
	)

	var e store.Entry

	err := s.cfg.DB.QueryRowContext(ctx, query, namespace, key).
		Scan(&e.Namespace, &e.Key, &e.Value, &e.UpdatedAt, &e.UpdatedBy)
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

// Set persists an entry using an INSERT ... ON CONFLICT UPDATE (upsert).
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

	ctx, span := s.startSpan(ctx, "systemplane.postgres.set",
		attribute.String("namespace", e.Namespace),
		attribute.String("key", e.Key),
	)
	defer span.End()

	query := fmt.Sprintf(`INSERT INTO %s (namespace, key, value, updated_at, updated_by)
VALUES ($1, $2, $3, now(), $4)
ON CONFLICT (namespace, key) DO UPDATE
SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		s.cfg.Table,
	)

	if _, err := s.cfg.DB.ExecContext(ctx, query, e.Namespace, e.Key, e.Value, e.UpdatedBy); err != nil {
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

		delay := backoff.ExponentialWithJitter(backoffBase, attempt)
		if delay > backoffCap {
			delay = backoffCap
		}

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

	listenStmt := fmt.Sprintf("LISTEN %s", quoteIdentifier(s.cfg.Channel))

	if _, err := conn.Exec(ctx, listenStmt); err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	s.logInfo(ctx, "LISTEN connection established",
		log.String("channel", s.cfg.Channel),
	)

	// Reset attempt counter on successful connection (the caller manages this,
	// but we signal success by returning a non-connection error or nil).

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
				log.String("payload", notification.Payload),
				log.Err(err),
			)

			continue
		}

		s.invokeHandler(ctx, handler, evt)
	}
}

// invokeHandler calls the user's handler with panic recovery.
func (s *Store) invokeHandler(ctx context.Context, handler func(store.Event), evt store.Event) {
	defer func() {
		if s.cfg.Logger != nil {
			runtime.RecoverAndLogWithContext(ctx, s.cfg.Logger, "systemplane", "postgres.handler")
		}
	}()

	handler(evt)
}

// notifyPayload is the JSON shape emitted by the pg_notify trigger.
type notifyPayload struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

// parseNotifyPayload converts a NOTIFY JSON payload into a store.Event.
func parseNotifyPayload(data string) (store.Event, error) {
	var p notifyPayload

	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return store.Event{}, fmt.Errorf("unmarshal: %w", err)
	}

	return store.Event{
		Namespace: p.Namespace,
		Key:       p.Key,
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
// a noop span.
func (s *Store) startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if s.cfg.Telemetry == nil {
		return ctx, trace.SpanFromContext(ctx)
	}

	tracer, err := s.cfg.Telemetry.Tracer(tracerName)
	if err != nil || tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}

	ctx, span := tracer.Start(ctx, name,
		trace.WithAttributes(attrs...),
	)

	return ctx, span
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
