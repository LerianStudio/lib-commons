package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/bxcodec/dbresolver/v2"
)

type resolverProvider interface {
	Resolver(ctx context.Context) (dbresolver.DB, error)
}

func resolvePrimaryDB(ctx context.Context, client resolverProvider) (*sql.DB, error) {
	if client == nil {
		return nil, ErrConnectionRequired
	}

	value := reflect.ValueOf(client)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil, ErrConnectionRequired
	}

	resolved, err := client.Resolver(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database connection: %w", err)
	}

	if resolved == nil {
		return nil, ErrNoPrimaryDB
	}

	primaryDBs := resolved.PrimaryDBs()
	if len(primaryDBs) == 0 {
		return nil, ErrNoPrimaryDB
	}

	if primaryDBs[0] == nil {
		return nil, ErrNoPrimaryDB
	}

	return primaryDBs[0], nil
}
