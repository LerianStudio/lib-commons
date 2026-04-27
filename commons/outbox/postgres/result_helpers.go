package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

func logSanitizedError(logger libLog.Logger, ctx context.Context, message string, err error) {
	if nilcheck.Interface(logger) || err == nil {
		return
	}

	logger.Log(ctx, libLog.LevelError, message, libLog.String("error", outbox.SanitizeErrorMessageForStorage(err.Error())))
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
