// Package postgres — LISTEN/NOTIFY subscription loop.
//
// This file holds the changefeed-delivery side of the backend: Subscribe
// (public entry point), listenLoop (single-connection inner loop),
// invokeHandler (panic-safe user callback), notifyPayload (JSON shape the
// trigger emits), parseNotifyPayload, and the reconnection backoff
// constants. Splitting this out of postgres.go keeps that file focused on
// schema, read/write methods, and Store lifecycle — and prevents postgres.go
// from drifting past the 500-LOC guardrail.
//
// The LISTEN path relies on a dedicated pgx connection (database/sql's
// connection pool can reuse a connection across requests, which breaks
// LISTEN's session-scoped semantics), so it lives separately from the
// Query/Exec callers that run through cfg.DB.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LerianStudio/lib-commons/v5/commons/backoff"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// backoffBase is the starting delay for LISTEN reconnection backoff.
const backoffBase = 500 * time.Millisecond

// backoffCap is the maximum delay between LISTEN reconnection attempts.
const backoffCap = 30 * time.Second

// closeTimeout is the deadline for best-effort cleanup on connection close.
const closeTimeout = 5 * time.Second

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
// decode as an empty string, which parseNotifyPayload coerces to
// store.SentinelGlobal so the Client's refresh router takes the legacy
// global-refresh path — that graceful degradation is intentional so a
// mid-rollout mix of upgraded and legacy rows cannot wedge a subscriber.
type notifyPayload struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	TenantID  string `json:"tenant_id"`
}

// parseNotifyPayload converts a NOTIFY JSON payload into a store.Event.
// A missing tenant_id field is not an error — it decodes as "" and is coerced
// to the '_global' sentinel so the Client's refresh router takes the legacy
// global path (the only safe interpretation of a pre-tenant payload). Without
// the coercion, the router would treat "" as a tenant event, fail the
// registry lookup, and emit a warn for every legacy payload during a
// rolling upgrade. Real post-Task-3 payloads always carry an explicit
// tenant_id, so this branch only activates during mixed-binary windows.
func parseNotifyPayload(data string) (store.Event, error) {
	var p notifyPayload

	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return store.Event{}, fmt.Errorf("unmarshal: %w", err)
	}

	tenantID := p.TenantID
	if tenantID == "" {
		tenantID = store.SentinelGlobal
	}

	return store.Event{
		Namespace: p.Namespace,
		Key:       p.Key,
		TenantID:  tenantID,
	}, nil
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
