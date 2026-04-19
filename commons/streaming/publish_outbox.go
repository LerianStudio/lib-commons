package streaming

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

// txContextKey is the context key that holds an ambient *sql.Tx for the
// outbox fallback to join via CreateWithTx.
//
// A package-local unexported type avoids collisions with any other
// package's context values (the standard idiom for context keys).
type txContextKey struct{}

// ContextWithTx returns a copy of ctx carrying the supplied *sql.Tx under
// streaming's unexported context key. When the circuit breaker is OPEN
// and WithOutboxRepository is wired, Emit's outbox fallback detects the
// ambient tx and writes the outbox row via CreateWithTx — joining the
// event publication to the caller's unit of work.
//
// Typical use:
//
//	tx, err := db.BeginTx(ctx, nil)
//	// ... business writes under tx ...
//	ctx = streaming.ContextWithTx(ctx, tx)
//	if err := emitter.Emit(ctx, event); err != nil { ... }
//	if err := tx.Commit(); err != nil { ... }
//
// Passing a nil *sql.Tx is a no-op — the outbox path falls back to
// Create. When the ambient tx is present but the circuit is CLOSED, the
// event publishes directly to the broker (no outbox row); the caller's
// tx proceeds independently. Callers who need strict at-least-once
// semantics should always pair Emit with tx-wrapped business writes AND
// wire an outbox Dispatcher.
//
// ContextWithTx is safe for concurrent use; context.WithValue produces a
// new ctx and does not mutate the parent.
func ContextWithTx(ctx context.Context, tx *sql.Tx) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	if tx == nil {
		return ctx
	}

	return context.WithValue(ctx, txContextKey{}, tx)
}

// publishToOutbox serializes an already-pre-flighted Event to an
// outbox.OutboxEvent and writes it via the configured OutboxRepository.
//
// When an ambient *sql.Tx is present on the context (see txFromContext),
// CreateWithTx is used so the write joins the caller's unit of work.
// Otherwise Create is called and the outbox repo opens its own transaction.
//
// Contract:
//   - On success, the caller path (Emit → cb.Execute closure) MUST return
//     (nil, nil) so the circuit breaker remains untouched — the outbox
//     absorbs the hit and the already-open breaker gets no additional
//     failure attributed to it.
//   - On OutboxRepository failure, the original error is returned so the
//     caller sees the failure. Silent drops are never acceptable — the
//     event becomes unrecoverable.
//   - Status/Attempts are NOT pre-set on the OutboxEvent. The outbox repo
//     normalizes them to PENDING/0 on insert (see
//     commons/outbox/postgres/repository.go:normalizedCreateValues). Pre-
//     setting would either get overwritten or conflict with that logic.
//
// Nil-receiver safe. Returns ErrNilProducer / ErrOutboxNotConfigured so
// callers get deterministic sentinels instead of panics.
func (p *Producer) publishToOutbox(ctx context.Context, event Event) error {
	if p == nil {
		return ErrNilProducer
	}

	if p.outbox == nil {
		return ErrOutboxNotConfigured
	}

	// JSON-marshal the full Event including CloudEvents context attributes
	// and the raw Payload. The handler on the read side (handleOutboxRow)
	// unmarshals back into an Event struct and hands it to publishDirect,
	// so round-trip fidelity matters — anything set by ApplyDefaults is
	// preserved here so replays produce identical wire-format messages.
	//
	// musttag nolint: Event ships in T1 without explicit `json:` tags; the
	// default Go-capitalized field names are what outbox consumers (same
	// package's handleOutboxRow) unmarshal against. Adding tags would be
	// a T1 scope change; the round-trip is exercised in tests.
	payload, err := json.Marshal(event) //nolint:musttag // see above; T1 scope
	if err != nil {
		return fmt.Errorf("streaming: marshal event for outbox: %w", err)
	}

	row := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   event.Topic(),
		AggregateID: deriveAggregateID(event),
		Payload:     payload,
		// Status / Attempts deliberately left zero-value — the outbox
		// repository overwrites them to PENDING/0 in normalizedCreateValues.
	}

	// Ambient transaction path. See txFromContext for why v1 doesn't yet
	// thread anything onto this key.
	if tx, ok := txFromContext(ctx); ok {
		if _, err := p.outbox.CreateWithTx(ctx, tx, row); err != nil {
			return fmt.Errorf("streaming: outbox create (tx): %w", err)
		}

		return nil
	}

	if _, err := p.outbox.Create(ctx, row); err != nil {
		return fmt.Errorf("streaming: outbox create: %w", err)
	}

	return nil
}

// deriveAggregateID produces a deterministic UUID from the event's
// partition key. Same tenant+aggregate → same AggregateID, which keeps the
// outbox row stream aligned with the Kafka partition stream and lets
// operators correlate the two by hash.
//
// For non-system events this hashes only the PartitionKey (= TenantID),
// giving tenant-level correlation. AggregateID is NOT per-event identity
// — use event.Subject for that. If a service wants per-aggregate grouping
// stronger than tenant, it should override the partition key via
// WithPartitionKey or, preferably, use a distinct Subject per aggregate.
//
// SystemEvent=true events use a random UUID because their "partition key"
// ("system:<eventtype>") would otherwise collapse every system event into
// the same aggregate — not what we want for audit/correlation.
//
// Uses uuid.NewSHA1 (v5) against the DNS namespace as a stable, well-known
// deterministic-UUID recipe. The exact namespace choice is not
// cryptographically significant; we need determinism, not uniqueness
// across namespaces.
func deriveAggregateID(event Event) uuid.UUID {
	if event.SystemEvent {
		return uuid.New()
	}

	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte(event.PartitionKey()))
}

// txFromContext returns the ambient *sql.Tx if one is stored on ctx under
// streaming's tx context key, previously installed by ContextWithTx.
//
// When no tx is on the context (the common case), returns (nil, false)
// and publishToOutbox falls through to the non-transactional Create
// branch. The outbox Dispatcher then drains the row with its own
// transaction management.
func txFromContext(ctx context.Context) (*sql.Tx, bool) {
	if ctx == nil {
		return nil, false
	}

	tx, ok := ctx.Value(txContextKey{}).(*sql.Tx)
	if !ok || tx == nil {
		return nil, false
	}

	return tx, true
}
