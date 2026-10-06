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
// replica, Migrator.Up and Migrator.Status check before opening their database,
// and NewFromPools refuses a posture it cannot verify.
//
// A refusal is a *WeakSSLModeError wrapping ErrWeakSSLMode. It names the
// connection, the setting and the value as written, never the DSN. Only the
// sslmode is judged: libpq treating require plus sslrootcert as verify-ca is
// not credited. CheckSSLMode and CheckSSLModeValue run the same check for a
// pool opened outside New.
//
// # Standalone migration binary
//
// Migrator is built to be the whole of a small migration binary, the kind a
// Kubernetes Job runs before a rollout. Ship the SQL inside the binary with
// MigrationConfig.MigrationsFS (an embed.FS; MigrationsPath then names the
// directory inside it), give Up a context with a deadline, and map the result
// to an exit code yourself: the library never exits.
//
//	//go:embed migrations
//	var migrations embed.FS
//
//	m, err := postgres.NewMigrator(postgres.MigrationConfig{
//		PrimaryDSN:       dsn,
//		DatabaseName:     "ledger",
//		MigrationsFS:     migrations,
//		MigrationsPath:   "migrations",
//		LockTimeout:      time.Minute,
//		StatementTimeout: 10 * time.Minute,
//		TLSPosture:       postgres.TLSPostureHardened,
//	})
//	...
//	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
//	defer cancel()
//	if err := m.Up(ctx); err != nil { ... exit non-zero ... }
//	status, err := m.Status(ctx) // Version, Dirty, Applied
//
// The context bounds the dial, the wait for the migration lock and the run.
// The lock wait is also capped by LockTimeout (zero: 15s) and fails with
// ErrMigrationLockTimeout while another migrator holds the lock; the caller's
// deadline instead yields context.DeadlineExceeded. golang-migrate cannot
// interrupt a statement in flight, so a context that ends mid-run stops the
// run between migrations: the database is left clean at the last finished
// version and Up's error names it and wraps the context's error.
// StatementTimeout bounds one statement; a statement it cuts fails its
// migration, which golang-migrate leaves dirty like any other failure.
//
// Up classifies a missing or empty source (ErrMigrationsNotFound, or nil under
// AllowMissingMigrations) before opening any database. Up and Status apply the
// TLS posture before dialing, exactly as New does.
//
// Neither ever returns or logs PrimaryDSN's password: the migrator redacts it
// by value, in every spelling a driver might print, and every driver or dial
// error comes back as a *SanitizedError, so a driver error that carries the
// connection config (pgx's *pgconn.ConnectError) is not reachable through
// errors.As. ErrMigrationDirty, ErrMigrationsNotFound,
// ErrMigrationVersionAhead, ErrMigrationLockTimeout and context errors keep
// their identity for errors.Is.
//
// # One version table per module
//
// Several modules of one service (or several scopes of one rail) can migrate
// the same database, each with its own version. Give each its own version
// table with MigrationConfig.MigrationsTable and, when it should live outside
// public, MigrationsSchema; empty keeps golang-migrate's
// public.schema_migrations. Without them, a module whose newest migration is
// version 1 reads a sibling's version 3 as ErrMigrationVersionAhead.
//
//	payments, _ := postgres.NewMigrator(postgres.MigrationConfig{
//		PrimaryDSN: dsn, DatabaseName: "rail",
//		MigrationsFS: paymentsSQL, MigrationsPath: "migrations",
//		MigrationsTable: "schema_migrations_payments",
//	})
//	receipts, _ := postgres.NewMigrator(postgres.MigrationConfig{
//		PrimaryDSN: dsn, DatabaseName: "rail",
//		MigrationsFS: receiptsSQL, MigrationsPath: "migrations",
//		MigrationsTable: "schema_migrations_receipts",
//	})
//
// golang-migrate keys its advisory lock on (database, schema, table), so each
// version table has its own lock: the modules never wait on each other and can
// migrate at the same time. Both names are lowercase identifiers
// (^[a-z_][a-z0-9_]{0,62}$; anything else is ErrInvalidConfig), because
// golang-migrate quotes them and a mixed-case name would turn case-sensitive.
// Up and Status create a missing MigrationsSchema (and leave an existing one
// alone). The schema moves only the version table: migration SQL still runs in
// the connection's search_path, so a migration that belongs in that schema
// names it.
package postgres
