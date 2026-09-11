//go:build unit

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/LerianStudio/lib-commons/v7/commons/postgres"
)

// A dashboard read runs several aggregates that must agree with each other. One
// REPEATABLE READ snapshot makes that true by construction: a row committed
// between two of the statements is invisible to both, so a total and its
// breakdown cannot disagree.
func ExampleRunReadOnly() {
	db, mock, err := sqlmock.New()
	if err != nil {
		fmt.Println("mock:", err)

		return
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL statement_timeout = 10000").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT count").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(7))
	mock.ExpectQuery("SELECT count").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(3))
	mock.ExpectRollback()

	var total, failed int

	err = postgres.RunReadOnly(context.Background(), db, postgres.ReadOnlyOptions{
		StatementTimeout:   10 * time.Second,
		TransactionTimeout: 15 * time.Second,
	}, func(ctx context.Context, tx *sql.Tx) error {
		if scanErr := tx.QueryRowContext(ctx, "SELECT count(*) FROM deliveries").Scan(&total); scanErr != nil {
			return scanErr
		}

		return tx.QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE failed").Scan(&failed)
	})

	fmt.Println("err:", err)
	fmt.Println("both counts from one snapshot:", total, failed)

	// Output:
	// err: <nil>
	// both counts from one snapshot: 7 3
}

// The two ways a read can run out of time need different fixes, so they are
// different sentinels: a slow plan wants an index, a slow transaction wants
// fewer statements.
func ExampleRunReadOnly_timeouts() {
	db, mock, err := sqlmock.New()
	if err != nil {
		fmt.Println("mock:", err)

		return
	}
	defer db.Close()

	mock.MatchExpectationsInOrder(false)
	mock.ExpectBegin()
	mock.ExpectRollback()

	err = postgres.RunReadOnly(context.Background(), db,
		postgres.ReadOnlyOptions{TransactionTimeout: 20 * time.Millisecond},
		func(ctx context.Context, _ *sql.Tx) error {
			<-ctx.Done()

			return ctx.Err()
		})

	fmt.Println("whole transaction ran out of time:", errors.Is(err, postgres.ErrReadOnlyTxDeadline))
	fmt.Println("not reported as a statement timeout:", !errors.Is(err, postgres.ErrReadOnlyStatementTimeout))

	// Output:
	// whole transaction ran out of time: true
	// not reported as a statement timeout: true
}
