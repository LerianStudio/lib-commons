package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
)

type primaryDBProvider interface {
	Primary() (*sql.DB, error)
}

func resolvePrimaryDB(_ context.Context, client primaryDBProvider) (*sql.DB, error) {
	if nilcheck.Interface(client) {
		return nil, ErrConnectionRequired
	}

	resolved, err := client.Primary()
	if err != nil {
		return nil, fmt.Errorf("failed to get database connection: %w", err)
	}

	if resolved == nil {
		return nil, ErrNoPrimaryDB
	}

	return resolved, nil
}
