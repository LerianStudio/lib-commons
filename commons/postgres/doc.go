// Package postgres provides shared PostgreSQL connection helpers.
//
// It focuses on predictable connection lifecycle and configuration defaults that
// are safe for service startup and shutdown flows.
//
// # Bounded snapshot reads
//
// RunReadOnly is the helper for a read that must see one consistent picture and
// must not be able to run away: it opens a REPEATABLE READ, READ ONLY
// transaction, caps every statement in it with SET LOCAL statement_timeout, and
// always rolls back. A dashboard query, a report, a reconciliation sweep — the
// workloads where a missing bound turns one slow plan into a pool with no
// connections left for anything else.
//
// The statement cap is MANDATORY: a zero StatementTimeout is refused with
// ErrReadOnlyStatementTimeoutRequired rather than opening an uncapped
// transaction. TransactionTimeout is optional and bounds the whole read.
//
// Three outcomes are distinguishable, and they mean different things to
// whoever is on call: ErrReadOnlyStatementTimeout is PostgreSQL cancelling one
// statement under the cap (the plan is too slow, and wants an index),
// ErrReadOnlyTxDeadline is the transaction's own budget expiring, and
// context.Canceled is the caller walking away — a read nobody is waiting for,
// which should be dropped rather than retried. All three come back with the
// driver's error still in the chain for errors.As.
//
// Use the (*Client).RunReadOnly method rather than the package-level function
// wherever a Client exists: it sends the read to the replica pool, which the
// package-level one cannot do on its own. Read its own doc first — a replica
// does not read your own writes.
//
// # TLS posture
//
// Config.TLSPosture (and MigrationConfig.TLSPosture) is the consumer's TLS
// stance, decided by the consumer: the library never reads ENV_NAME. The zero
// value, TLSPostureDefault, keeps the long-standing rule: sslmode must be
// require or stronger unless ALLOW_INSECURE_TLS=true. TLSPostureHardened
// refuses, before any dial, every sslmode weaker than Config.MinSSLMode
// (require, verify-ca or verify-full; empty means verify-full), an absent
// sslmode and an unparseable DSN; ALLOW_INSECURE_TLS never lifts that floor.
// TLSPostureSaaS requires verify-full. New checks the primary and any distinct
// replica, Migrator.Up checks before opening its database, and NewFromPools
// refuses a posture it cannot verify.
//
// A refusal is a *WeakSSLModeError wrapping ErrWeakSSLMode. It names the
// connection, the setting and the value as written, never the DSN. Only the
// sslmode is judged: libpq treating require plus sslrootcert as verify-ca is
// not credited. CheckSSLMode and CheckSSLModeValue run the same check for a
// pool opened outside New.
package postgres
