//go:build unit

package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoDeletePublishedBefore_NotInitialized(t *testing.T) {
	t.Parallel()

	deleted, err := uninitializedMongoRepo().DeletePublishedBefore(context.Background(), time.Now().UTC(), nil, 10)
	require.ErrorIs(t, err, ErrRepositoryNotInitialized)
	require.Zero(t, deleted)
}

func TestMongoDeletePublishedBefore_NonPositiveLimitNeverTouchesTheDatabase(t *testing.T) {
	t.Parallel()

	// The zero client would fail on any database access.
	for _, limit := range []int{0, -1} {
		deleted, err := initializedMongoRepo().DeletePublishedBefore(context.Background(), time.Now().UTC(), nil, limit)
		require.NoError(t, err)
		require.Zero(t, deleted)
	}
}

func TestMongoPublishedBeforeFilter(t *testing.T) {
	t.Parallel()

	before := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	require.Equal(t, bson.M{
		mongoFieldStatus:    outbox.OutboxStatusPublished,
		mongoFieldCreatedAt: bson.M{mongoOperatorLT: before},
	}, publishedBeforeFilter(before, []string{"", "  "}))

	require.Equal(t, bson.M{
		mongoFieldStatus:    outbox.OutboxStatusPublished,
		mongoFieldCreatedAt: bson.M{mongoOperatorLT: before},
		"event_type":        bson.M{"$nin": []string{"leilao.solicitado", "margem.solicitada"}},
	}, publishedBeforeFilter(before, []string{"leilao.solicitado", " margem.solicitada "}))
}
