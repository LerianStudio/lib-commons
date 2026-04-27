package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

func queryOutboxEvents(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	args []any,
	limit int,
	errorPrefix string,
) ([]*outbox.OutboxEvent, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errorPrefix, err)
	}

	defer rows.Close()

	events := make([]*outbox.OutboxEvent, 0, limit)

	for rows.Next() {
		event, scanErr := scanOutboxEvent(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scanning outbox event: %w", scanErr)
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return events, nil
}
