//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	return db, mock
}

// noopFn is a body that touches nothing, for the cases where the body is not
// what is under test.
func noopFn(context.Context, *sql.Tx) error { return nil }

func TestReadOnlyTxOptionsPosture(t *testing.T) {
	t.Parallel()

	// sqlmock discards driver.TxOptions, so no behaviour test can observe the
	// isolation level a transaction really opened under. What this test buys is
	// that the posture is decided in exactly one place, so the mutation surface
	// a reviewer must check is one function.
	opts := readOnlyTxOptions()

	require.NotNil(t, opts)
	assert.Equal(t, sql.LevelRepeatableRead, opts.Isolation,
		"a snapshot read needs one snapshot for the whole transaction")
	assert.True(t, opts.ReadOnly, "READ ONLY makes the intent enforceable rather than documented")
}

func TestRunReadOnlyHappyPath(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL statement_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT 1`).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectRollback()

	var seen int

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{
		StatementTimeout:   10 * time.Second,
		TransactionTimeout: 15 * time.Second,
	}, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT 1").Scan(&seen)
	})

	require.NoError(t, err)
	assert.Equal(t, 1, seen)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunReadOnlySendsStatementTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		timeout   time.Duration
		wantQuery string
		wantSent  bool
	}{
		{name: "seconds", timeout: 10 * time.Second, wantQuery: "SET LOCAL statement_timeout = 10000", wantSent: true},
		{name: "milliseconds", timeout: 250 * time.Millisecond, wantQuery: "SET LOCAL statement_timeout = 250", wantSent: true},
		{
			name:      "sub-millisecond rounds up, never to the disabling zero",
			timeout:   100 * time.Microsecond,
			wantQuery: "SET LOCAL statement_timeout = 1",
			wantSent:  true,
		},
		{name: "zero sends nothing", timeout: 0, wantSent: false},
		{name: "negative sends nothing", timeout: -time.Second, wantSent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock := newMockDB(t)

			mock.ExpectBegin()

			if tt.wantSent {
				mock.ExpectExec(regexp.QuoteMeta(tt.wantQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
			}

			mock.ExpectRollback()

			err := RunReadOnly(t.Context(), db, ReadOnlyOptions{StatementTimeout: tt.timeout}, noopFn)

			require.NoError(t, err)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestRunReadOnlyStatementTimeoutIsSetBeforeTheBodyRuns(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	// Ordered expectations: the cap must land BEFORE the body's first statement,
	// or the first statement runs uncapped.
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL statement_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT now\(\)`).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(time.Now()))
	mock.ExpectRollback()

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{StatementTimeout: time.Second},
		func(ctx context.Context, tx *sql.Tx) error {
			var at time.Time

			return tx.QueryRowContext(ctx, "SELECT now()").Scan(&at)
		})

	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunReadOnlyFailedCapIsAFailedRead(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	capErr := errors.New("permission denied")

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL statement_timeout`).WillReturnError(capErr)
	mock.ExpectRollback()

	called := false

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{StatementTimeout: time.Second},
		func(context.Context, *sql.Tx) error {
			called = true

			return nil
		})

	require.Error(t, err)
	require.ErrorIs(t, err, capErr)
	assert.False(t, called, "an uncapped read is the runaway this bound exists to stop; the body must not run")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunReadOnlyRollsBackOnSuccess(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	// A read-only transaction has nothing to commit, and rolling back releases
	// the snapshot it has been holding just as a commit would. Leaving it open
	// pins the oldest visible row version for as long as the connection sits in
	// the pool.
	mock.ExpectBegin()
	mock.ExpectRollback()

	require.NoError(t, RunReadOnly(t.Context(), db, ReadOnlyOptions{}, noopFn))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunReadOnlyRollsBackOnBodyError(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	mock.ExpectBegin()
	mock.ExpectRollback()

	bodyErr := errors.New("aggregate failed")

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{}, func(context.Context, *sql.Tx) error {
		return bodyErr
	})

	require.ErrorIs(t, err, bodyErr, "the body's error must stay in the chain")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunReadOnlyBeginFailure(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	beginErr := errors.New("too many connections")
	mock.ExpectBegin().WillReturnError(beginErr)

	called := false

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{}, func(context.Context, *sql.Tx) error {
		called = true

		return nil
	})

	require.ErrorIs(t, err, beginErr)
	assert.False(t, called)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRunReadOnlyDistinguishesStatementTimeoutFromDeadline(t *testing.T) {
	t.Parallel()

	pgxCanceled := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	pqCanceled := &pq.Error{Code: "57014", Message: "canceling statement due to statement timeout"}
	otherPgErr := &pgconn.PgError{Code: "23505", Message: "duplicate key"}

	tests := []struct {
		name       string
		txTimeout  time.Duration
		bodyErr    error
		waitForCtx bool
		wantIs     error
		wantNotIs  error
	}{
		{
			name:      "pgx query_canceled is a statement timeout",
			bodyErr:   pgxCanceled,
			wantIs:    ErrReadOnlyStatementTimeout,
			wantNotIs: ErrReadOnlyTxDeadline,
		},
		{
			name:      "lib/pq query_canceled is a statement timeout",
			bodyErr:   pqCanceled,
			wantIs:    ErrReadOnlyStatementTimeout,
			wantNotIs: ErrReadOnlyTxDeadline,
		},
		{
			name:      "an unrelated SQLSTATE is neither",
			bodyErr:   otherPgErr,
			wantNotIs: ErrReadOnlyStatementTimeout,
		},
		{
			name:      "a plain error is neither",
			bodyErr:   errors.New("boom"),
			wantNotIs: ErrReadOnlyStatementTimeout,
		},
		{
			name:       "the whole-transaction deadline wins over the server's cancel",
			txTimeout:  20 * time.Millisecond,
			bodyErr:    pgxCanceled,
			waitForCtx: true,
			wantIs:     ErrReadOnlyTxDeadline,
			wantNotIs:  ErrReadOnlyStatementTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock := newMockDB(t)

			mock.MatchExpectationsInOrder(false)
			mock.ExpectBegin()
			mock.ExpectRollback()

			err := RunReadOnly(t.Context(), db, ReadOnlyOptions{TransactionTimeout: tt.txTimeout},
				func(ctx context.Context, _ *sql.Tx) error {
					if tt.waitForCtx {
						<-ctx.Done()
					}

					return tt.bodyErr
				})

			require.Error(t, err)
			require.ErrorIs(t, err, tt.bodyErr, "the driver error must stay in the chain for the caller to classify")

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}

			if tt.wantNotIs != nil {
				require.NotErrorIs(t, err, tt.wantNotIs)
			}
		})
	}
}

func TestRunReadOnlyDeadlineFiresWithNoBodyError(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	mock.MatchExpectationsInOrder(false)
	mock.ExpectBegin()
	mock.ExpectRollback()

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{TransactionTimeout: 20 * time.Millisecond},
		func(ctx context.Context, _ *sql.Tx) error {
			<-ctx.Done()

			return ctx.Err()
		})

	require.ErrorIs(t, err, ErrReadOnlyTxDeadline)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRunReadOnlyDeadlineIsNotAppliedWhenZero(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	mock.ExpectBegin()
	mock.ExpectRollback()

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{}, func(ctx context.Context, _ *sql.Tx) error {
		_, hasDeadline := ctx.Deadline()
		assert.False(t, hasDeadline, "a zero TransactionTimeout must not invent a deadline")

		return nil
	})

	require.NoError(t, err)
}

func TestRunReadOnlyHonoursAParentDeadline(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	mock.MatchExpectationsInOrder(false)
	mock.ExpectBegin()
	mock.ExpectRollback()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	err := RunReadOnly(ctx, db, ReadOnlyOptions{}, func(ctx context.Context, _ *sql.Tx) error {
		<-ctx.Done()

		return ctx.Err()
	})

	require.ErrorIs(t, err, ErrReadOnlyTxDeadline,
		"a deadline the caller set is still the transaction's deadline")
}

func TestRunReadOnlyGuards(t *testing.T) {
	t.Parallel()

	db, _ := newMockDB(t)

	tests := []struct {
		name    string
		db      *sql.DB
		fn      func(context.Context, *sql.Tx) error
		wantErr error
	}{
		{name: "nil db", db: nil, fn: noopFn, wantErr: ErrNilClient},
		{name: "nil fn", db: db, fn: nil, wantErr: ErrNilReadOnlyFunc},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, RunReadOnly(t.Context(), tt.db, ReadOnlyOptions{}, tt.fn), tt.wantErr)
		})
	}
}

func TestRunReadOnlyRollbackFailureIsReportedAlongsideTheCause(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	bodyErr := errors.New("aggregate failed")
	rollbackErr := errors.New("connection reset")

	mock.ExpectBegin()
	mock.ExpectRollback().WillReturnError(rollbackErr)

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{}, func(context.Context, *sql.Tx) error {
		return bodyErr
	})

	require.ErrorIs(t, err, bodyErr, "the original cause is what the operator needs")
	assert.Contains(t, err.Error(), rollbackErr.Error(),
		"a transaction that really would not close is a second, different fact")
}

func TestRunReadOnlySuccessfulReadReportsARollbackFailure(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	rollbackErr := errors.New("connection reset")

	mock.ExpectBegin()
	mock.ExpectRollback().WillReturnError(rollbackErr)

	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{}, noopFn)

	require.ErrorIs(t, err, rollbackErr,
		"a read whose transaction would not close did not cleanly release its snapshot")
}

func TestRunReadOnlyAlreadyClosedTransactionIsNotASecondFault(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	mock.ExpectBegin()
	mock.ExpectRollback().WillReturnError(sql.ErrTxDone)

	// database/sql rolls the transaction back itself once its context is done,
	// so the explicit rollback then reports "already committed or rolled back".
	// Appending that to every timeout would point on-call at a leaked
	// transaction to hunt.
	err := RunReadOnly(t.Context(), db, ReadOnlyOptions{}, noopFn)

	require.NoError(t, err)
}
