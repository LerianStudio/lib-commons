package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Read-only transaction sentinels.
//
// The two timeout sentinels exist because a read that ran out of time did so in
// one of two places, and the operator response differs. A STATEMENT timeout means
// one query's plan is too slow — the fix is an index, a narrower filter, or a
// higher cap. A transaction DEADLINE means the read as a whole took too long,
// typically many statements each finishing just inside the per-statement cap —
// the fix is fewer statements or a shorter window. Reported as one error they are
// indistinguishable, and the wrong fix gets applied.
var (
	// ErrNilReadOnlyFunc — RunReadOnly was called with no body to run.
	ErrNilReadOnlyFunc = errors.New("read-only transaction function is nil")

	// ErrReadOnlyStatementTimeoutRequired — ReadOnlyOptions carried no positive
	// StatementTimeout. A zero cap is not an opt-out, it is the uncapped read
	// this helper exists to prevent, so it is refused before the transaction
	// opens rather than honoured silently.
	//
	// It is neither of the two timeout sentinels below: those say a read RAN OUT
	// of time, which is an operational fact; this says the caller never set a
	// bound, which is a programming fault.
	ErrReadOnlyStatementTimeoutRequired = errors.New("read-only statement timeout is required")

	// ErrReadOnlyTxDeadline — the transaction's context deadline fired, whether
	// it came from ReadOnlyOptions.TransactionTimeout or from the caller's own
	// context. It bounds the whole transaction: every statement, plus BEGIN and
	// the rollback.
	ErrReadOnlyTxDeadline = errors.New("read-only transaction deadline exceeded")

	// ErrReadOnlyStatementTimeout — PostgreSQL cancelled a single statement,
	// server-side, under the cap RunReadOnly set. Reported only when the
	// transaction's own deadline had NOT fired, because a client-side deadline
	// also arrives as a cancellation from the server and would otherwise be
	// reported as both.
	ErrReadOnlyStatementTimeout = errors.New("read-only statement timeout")
)

// ReadOnlyOptions bounds one read-only transaction in time.
//
// StatementTimeout is MANDATORY and must be positive. TransactionTimeout is
// optional and zero means "inherit whatever deadline the caller's context
// already carries", which is the common case for a read already bounded by its
// request.
type ReadOnlyOptions struct {
	// StatementTimeout caps EACH statement, server-side, for the life of this
	// transaction only. It is REQUIRED: zero or negative is refused with
	// ErrReadOnlyStatementTimeoutRequired before the transaction opens, because
	// zero is how PostgreSQL spells "no timeout" and a silently uncapped snapshot
	// read is exactly the runaway this helper exists to stop.
	//
	// WHY SERVER-SIDE AND NOT ONLY A CONTEXT: a context deadline cancels from the
	// client — the driver must notice, open a second connection and send a cancel
	// request, and until PostgreSQL acts on it the original query keeps burning
	// CPU and holding its snapshot. statement_timeout is enforced inside the
	// backend running the query, with no round trip, which is the only cap that
	// actually stops a runaway plan.
	//
	// A value under one millisecond is raised to one, never rounded down: zero is
	// how PostgreSQL spells "no timeout", so rounding down would silently disable
	// the cap the caller asked for.
	StatementTimeout time.Duration

	// TransactionTimeout bounds the WHOLE transaction, where StatementTimeout
	// bounds each statement separately. A read of a dozen statements, each
	// finishing just inside the per-statement cap, would otherwise hold a
	// connection and a snapshot for minutes.
	//
	// When the caller's context already has an earlier deadline, that one wins;
	// this never extends a deadline. ZERO IS AN OPT-OUT HERE, unlike
	// StatementTimeout: it means the transaction inherits the caller's deadline
	// and adds none of its own, which is what a read already bounded by its
	// request wants. The per-statement cap still applies either way, so zero here
	// never leaves the read unbounded.
	TransactionTimeout time.Duration
}

// TxBeginner is the slice of database/sql that RunReadOnly actually needs: the
// ability to open a transaction. *sql.DB and *sql.Conn both satisfy it.
//
// It is an interface rather than *sql.DB because this package hands callers a
// dbresolver.DB and a Client, and a signature demanding *sql.DB left every
// adopter reaching for Primary() — routing snapshot reads, the one workload that
// should never touch the write pool, onto the primary. Client.RunReadOnly is the
// direct answer; this widening is what lets a caller pass any pool it already
// holds.
type TxBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// RunReadOnly runs fn inside a REPEATABLE READ, READ ONLY transaction bounded in
// time, and always rolls it back.
//
// # What the isolation buys
//
// REPEATABLE READ fixes ONE snapshot for the whole transaction, so every
// statement fn runs reads the same instant of the database. That is what makes a
// multi-query read internally consistent: a total and its breakdown, taken as two
// statements, cannot disagree because rows committed between them are invisible
// to both. READ ONLY makes the intent enforceable by the server rather than
// merely documented.
//
// # Why it always rolls back
//
// A read-only transaction has nothing to commit, and a rollback releases the
// snapshot exactly as a commit would. Rolling back unconditionally means there is
// no success path that can forget to close, and no code path that could commit
// something a READ ONLY transaction was not supposed to hold. Leaving it open
// instead pins the oldest visible row version for as long as the connection sits
// idle in the pool, which is how one slow dashboard degrades everyone's vacuum.
//
// A rollback that genuinely fails is reported ALONGSIDE the original error rather
// than in place of it — the original is what the operator needs, and a
// transaction that would not close is a second, different fact. sql.ErrTxDone is
// not one of those: when the transaction's context is done, database/sql has
// already rolled back, and reporting that as a fault would point on-call at a
// leaked transaction to hunt.
//
// # Distinguishing the two ways a read runs out of time
//
// errors.Is(err, ErrReadOnlyStatementTimeout) means PostgreSQL cancelled one
// statement under the cap. errors.Is(err, ErrReadOnlyTxDeadline) means the
// transaction's context deadline fired. The deadline is checked FIRST, because a
// client-side cancellation also reaches the server as a cancelled query and would
// otherwise be reported as both.
//
// fn's error is always left in the chain, wrapped, so a caller can still classify
// the driver error underneath with errors.As. Nothing here redacts it: use
// commons/security/sanitize at the logging boundary, which preserves the chain
// while redacting the message.
//
// # What is not verified here
//
// fn receives the transaction and can run anything on it. A write inside a READ
// ONLY transaction is refused by PostgreSQL, not by this function.
//
// # Adopting from a local implementation
//
// The error returned here carries the DRIVER's own text, which for a connection
// failure includes the DSN. This package does not redact it, deliberately:
// coupling postgres to the redaction rules would make every service that opens a
// pool depend on them. Wrap at the LOGGING boundary instead, with
// commons/security/sanitize.Error, which redacts the message while errors.Is and
// errors.As keep classifying the driver error underneath.
//
// A service moving off its own helper should also expect StatementTimeout to be
// mandatory now: a call that previously passed a zero value and ran uncapped
// returns ErrReadOnlyStatementTimeoutRequired instead of opening a transaction.
func RunReadOnly(
	ctx context.Context,
	db TxBeginner,
	opts ReadOnlyOptions,
	fn func(ctx context.Context, tx *sql.Tx) error,
) (err error) {
	if isNilBeginner(db) {
		return ErrNilClient
	}

	if fn == nil {
		return ErrNilReadOnlyFunc
	}

	if opts.StatementTimeout <= 0 {
		return ErrReadOnlyStatementTimeoutRequired
	}

	if opts.TransactionTimeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, opts.TransactionTimeout)
		defer cancel()
	}

	tx, beginErr := db.BeginTx(ctx, readOnlyTxOptions())
	if beginErr != nil {
		return classifyReadOnly(ctx, fmt.Errorf("begin read-only transaction: %w", beginErr))
	}

	defer func() { err = closeReadOnly(tx, err) }()

	// A FAILED CAP IS A FAILED READ: if the SET does not land, no statement runs
	// under it, and an uncapped read is exactly the runaway this bounding exists
	// to stop.
	if capErr := applyStatementTimeout(ctx, tx, opts.StatementTimeout); capErr != nil {
		return classifyReadOnly(ctx, capErr)
	}

	if fnErr := fn(ctx, tx); fnErr != nil {
		return classifyReadOnly(ctx, fnErr)
	}

	return nil
}

// RunReadOnly runs fn against this client's READ pool under the same bounded
// snapshot contract as the package-level RunReadOnly, preferring the configured
// replica and falling back to the primary when none is configured.
//
// This is the method to reach for. A snapshot read is the one workload that
// should never occupy the write pool, and Primary() is the only *sql.DB the
// client hands out — so the package-level helper, called with what the client
// makes convenient, sends every dashboard query to the primary.
//
// It connects lazily on first use, exactly like Resolver.
func (c *Client) RunReadOnly(
	ctx context.Context,
	opts ReadOnlyOptions,
	fn func(ctx context.Context, tx *sql.Tx) error,
) error {
	if c == nil {
		return nilClientAssert("run read-only")
	}

	if _, err := c.Resolver(ctx); err != nil {
		return err
	}

	c.mu.RLock()
	db := c.replica

	if db == nil {
		db = c.primary
	}

	c.mu.RUnlock()

	return RunReadOnly(ctx, db, opts, fn)
}

// isNilBeginner reports whether db carries nothing to begin a transaction on. A
// nil *sql.DB inside an interface is a NON-nil interface, so the plain nil check
// misses it and BeginTx panics instead of returning the guard's error.
func isNilBeginner(db TxBeginner) bool {
	switch typed := db.(type) {
	case nil:
		return true
	case *sql.DB:
		return typed == nil
	case *sql.Conn:
		return typed == nil
	default:
		return false
	}
}

// readOnlyTxOptions is the isolation posture every RunReadOnly transaction opens
// under. It is a function of its own so the posture is decided in exactly one
// place: sqlmock discards driver.TxOptions, so no behaviour test can observe what
// a transaction really opened under, and a second decision point is how one of
// the two settings quietly goes missing on a new code path.
func readOnlyTxOptions() *sql.TxOptions {
	return &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
}

// applyStatementTimeout sends the per-statement cap for this transaction only.
//
// SET LOCAL is scoped to the transaction and reverts at its end, so it cannot
// leak onto the next user of a pooled connection. It is also a utility command
// and takes no snapshot, so under REPEATABLE READ the first statement fn runs is
// still what fixes the snapshot.
// The timeout is already known positive: RunReadOnly refuses a non-positive one
// before the transaction opens.
func applyStatementTimeout(ctx context.Context, tx *sql.Tx, timeout time.Duration) error {
	milliseconds := max(timeout.Milliseconds(), 1)

	// The interpolated value is an int64 derived from a time.Duration, so no
	// caller-supplied string reaches the statement and there is no injection
	// surface. PostgreSQL does not accept a bind parameter in SET, so this cannot
	// be parameterized.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", milliseconds)); err != nil {
		return fmt.Errorf("set read-only statement timeout: %w", err)
	}

	return nil
}

// classifyReadOnly tags cause with the sentinel that says WHERE the read ran out
// of time, leaving cause itself in the chain.
//
// THE CONTEXT IS ASKED FIRST, AND ABOUT BOTH OF ITS ANSWERS. PostgreSQL answers
// SQLSTATE 57014 to three different events: a statement that exceeded the cap,
// a transaction whose deadline fired, and a caller who cancelled. Only the first
// is a statement timeout. Reading the SQLSTATE before the context reported a
// deadline as a statement timeout; reading only the deadline reported a CANCEL as
// one — which points on-call at a query plan that never had a problem, and buries
// context.Canceled under a sentinel, so a caller cannot tell "the client walked
// away" from "this read is too slow" and retries something it should drop.
//
// A cancel is neither timeout, so it gets no sentinel: the cause is returned as
// it stands, and errors.Is(err, context.Canceled) still holds for the caller that
// has to decide whether to retry.
func classifyReadOnly(ctx context.Context, cause error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrReadOnlyTxDeadline, cause)
		}

		return cause
	}

	if matchesSQLState(cause, queryCanceled) {
		return fmt.Errorf("%w: %w", ErrReadOnlyStatementTimeout, cause)
	}

	return cause
}

// closeReadOnly rolls the transaction back and folds any genuine rollback failure
// into the result without displacing the original cause.
func closeReadOnly(tx *sql.Tx, cause error) error {
	rollbackErr := tx.Rollback()
	if rollbackErr == nil || errors.Is(rollbackErr, sql.ErrTxDone) {
		return cause
	}

	if cause == nil {
		return fmt.Errorf("rollback read-only transaction: %w", rollbackErr)
	}

	return fmt.Errorf("%w; rollback also failed: %w", cause, rollbackErr)
}
