package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
	"github.com/bxcodec/dbresolver/v2"
)

type primaryDBProvider interface {
	Primary() (*sql.DB, error)
}

type resolverProvider interface {
	Resolver(ctx context.Context) (dbresolver.DB, error)
}

func resolvePrimaryDB(ctx context.Context, client primaryDBProvider) (*sql.DB, error) {
	if nilcheck.Interface(client) {
		return nil, ErrConnectionRequired
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if resolverClient, ok := client.(resolverProvider); ok {
		if _, err := resolverClient.Resolver(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize database resolver: %w", err)
		}
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
