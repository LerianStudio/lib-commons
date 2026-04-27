package postgres

import (
	"context"
	"database/sql"
	"fmt"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	"go.opentelemetry.io/otel/trace"
)

func tracerFromContext(ctx context.Context) trace.Tracer {
	logger, tracer, meter, trackingErr := libCommons.NewTrackingFromContext(ctx)
	_ = logger
	_ = meter
	_ = trackingErr

	return tracer
}

func ensureRowsAffected(result sql.Result) error {
	rows, err := rowsAffected(result)
	if err != nil {
		return err
	}

	if rows == 0 {
		return ErrStateTransitionConflict
	}

	return nil
}

func ensureRowsAffectedExact(result sql.Result, expected int64) error {
	rows, err := rowsAffected(result)
	if err != nil {
		return err
	}

	if rows != expected {
		return ErrStateTransitionConflict
	}

	return nil
}

func rowsAffected(result sql.Result) (int64, error) {
	if result == nil {
		return 0, ErrStateTransitionConflict
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}

	return rows, nil
}
