//go:build unit

package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// TestConnect_AppliesCSOTTimeout verifies the client-level CSOT Timeout is set
// on the driver options, defaulting to defaultTimeout and honoring an explicit
// configured value.
func TestConnect_AppliesCSOTTimeout(t *testing.T) {
	t.Parallel()

	t.Run("default_when_unset", func(t *testing.T) {
		t.Parallel()

		var captured *options.ClientOptions

		deps := successDeps()
		deps.connect = func(_ context.Context, opts *options.ClientOptions) (*mongo.Client, error) {
			captured = opts
			return &mongo.Client{}, nil
		}

		_, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
		require.NoError(t, err)
		require.NotNil(t, captured.Timeout, "CSOT Timeout must be set")
		assert.Equal(t, defaultTimeout, *captured.Timeout)
	})

	t.Run("honors_explicit_value", func(t *testing.T) {
		t.Parallel()

		var captured *options.ClientOptions

		deps := successDeps()
		deps.connect = func(_ context.Context, opts *options.ClientOptions) (*mongo.Client, error) {
			captured = opts
			return &mongo.Client{}, nil
		}

		cfg := baseConfig()
		cfg.Timeout = 50 * time.Second

		_, err := NewClient(context.Background(), cfg, withDeps(deps))
		require.NoError(t, err)
		require.NotNil(t, captured.Timeout)
		assert.Equal(t, 50*time.Second, *captured.Timeout)
	})
}

// TestEnsureIndexes_DeadlineHandling verifies index creation gets a longer
// explicit deadline when the caller supplies none, and that a caller-provided
// per-operation deadline is respected (not overridden by the CSOT default or
// the longer index floor).
func TestEnsureIndexes_DeadlineHandling(t *testing.T) {
	t.Parallel()

	idx := mongo.IndexModel{Keys: bson.D{{Key: "f", Value: 1}}}

	t.Run("adds_floor_when_no_caller_deadline", func(t *testing.T) {
		t.Parallel()

		var (
			gotDeadline time.Time
			hadDeadline bool
		)

		deps := successDeps()
		deps.createIndex = func(ctx context.Context, _ *mongo.Client, _, _ string, _ mongo.IndexModel) error {
			gotDeadline, hadDeadline = ctx.Deadline()
			return nil
		}

		client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
		require.NoError(t, err)

		require.NoError(t, client.EnsureIndexes(context.Background(), "coll", idx))
		require.True(t, hadDeadline, "EnsureIndexes must impose a deadline when caller has none")
		// Floor is ensureIndexesTimeout, well above the 30s CSOT default.
		assert.Greater(t, time.Until(gotDeadline), time.Minute)
	})

	t.Run("respects_caller_deadline", func(t *testing.T) {
		t.Parallel()

		var gotDeadline time.Time

		deps := successDeps()
		deps.createIndex = func(ctx context.Context, _ *mongo.Client, _, _ string, _ mongo.IndexModel) error {
			gotDeadline, _ = ctx.Deadline()
			return nil
		}

		client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
		require.NoError(t, err)

		callerDeadline := time.Now().Add(2 * time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
		defer cancel()

		require.NoError(t, client.EnsureIndexes(ctx, "coll", idx))
		// The short caller deadline must NOT be extended to the index floor.
		assert.WithinDuration(t, callerDeadline, gotDeadline, 500*time.Millisecond)
	})
}
