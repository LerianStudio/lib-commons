//go:build unit

package commons

import (
	"context"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

func TestCloneContextValues(t *testing.T) {
	t.Parallel()

	t.Run("nil context value returns empty non-nil struct", func(t *testing.T) {
		t.Parallel()

		// context.Background() has no CustomContextKey value.
		clone := cloneContextValues(context.Background())

		require.NotNil(t, clone)
		assert.Empty(t, clone.HeaderID)
		assert.Nil(t, clone.Logger)
		assert.Nil(t, clone.Tracer)
		assert.Nil(t, clone.MetricFactory)
		assert.Nil(t, clone.AttrBag)
	})

	t.Run("context with wrong type returns empty non-nil struct", func(t *testing.T) {
		t.Parallel()

		// Store a string instead of *CustomContextKeyValue.
		ctx := context.WithValue(context.Background(), CustomContextKey, "not-a-struct")
		clone := cloneContextValues(ctx)

		require.NotNil(t, clone)
		assert.Empty(t, clone.HeaderID)
	})

	t.Run("preserves existing values", func(t *testing.T) {
		t.Parallel()

		nopLogger := &log.NopLogger{}
		tracer := otel.Tracer("test-clone")

		original := &CustomContextKeyValue{
			HeaderID: "hdr-abc",
			Logger:   nopLogger,
			Tracer:   tracer,
		}
		ctx := context.WithValue(context.Background(), CustomContextKey, original)

		clone := cloneContextValues(ctx)

		require.NotNil(t, clone)
		assert.Equal(t, "hdr-abc", clone.HeaderID)
		assert.Equal(t, nopLogger, clone.Logger)
		assert.Equal(t, tracer, clone.Tracer)
	})

	t.Run("deep-copies AttrBag so mutating clone does not affect original", func(t *testing.T) {
		t.Parallel()

		original := &CustomContextKeyValue{
			HeaderID: "hdr-deep",
			AttrBag: []attribute.KeyValue{
				attribute.String("tenant.id", "t1"),
				attribute.String("region", "us-east"),
			},
		}
		ctx := context.WithValue(context.Background(), CustomContextKey, original)

		clone := cloneContextValues(ctx)

		// Verify initial equality.
		require.Len(t, clone.AttrBag, 2)
		assert.Equal(t, original.AttrBag, clone.AttrBag)

		// Mutate the clone's AttrBag.
		clone.AttrBag[0] = attribute.String("tenant.id", "MUTATED")
		clone.AttrBag = append(clone.AttrBag, attribute.String("extra", "added"))

		// Original must be unchanged.
		assert.Equal(t, "t1", original.AttrBag[0].Value.AsString())
		assert.Len(t, original.AttrBag, 2)
	})

	t.Run("empty AttrBag is shallow-copied without deep-copy allocation", func(t *testing.T) {
		t.Parallel()

		original := &CustomContextKeyValue{
			HeaderID: "hdr-empty-bag",
			AttrBag:  []attribute.KeyValue{},
		}
		ctx := context.WithValue(context.Background(), CustomContextKey, original)

		clone := cloneContextValues(ctx)

		// The struct copy (*clone = *existing) propagates the empty slice.
		// The deep-copy branch is skipped (len == 0), so the clone gets the
		// original's empty-but-non-nil slice header. This is correct behavior:
		// no allocation needed for an empty bag.
		assert.Empty(t, clone.AttrBag)
		assert.Equal(t, "hdr-empty-bag", clone.HeaderID)
	})

	t.Run("clone is independent — modifying clone fields does not affect original", func(t *testing.T) {
		t.Parallel()

		nopLogger := &log.NopLogger{}
		original := &CustomContextKeyValue{
			HeaderID: "hdr-independent",
			Logger:   nopLogger,
		}
		ctx := context.WithValue(context.Background(), CustomContextKey, original)

		clone := cloneContextValues(ctx)
		clone.HeaderID = "CHANGED"
		clone.Logger = nil

		// Original must remain intact.
		assert.Equal(t, "hdr-independent", original.HeaderID)
		assert.Equal(t, nopLogger, original.Logger)
	})
}

func TestCloneContextValues_Concurrent(t *testing.T) {
	t.Parallel()

	// Two goroutines derive independent clones from the same parent context.
	// They both mutate their clone's AttrBag without data races.
	original := &CustomContextKeyValue{
		HeaderID: "hdr-concurrent",
		AttrBag: []attribute.KeyValue{
			attribute.String("shared", "value"),
		},
	}
	parentCtx := context.WithValue(context.Background(), CustomContextKey, original)

	const goroutines = 50

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			clone := cloneContextValues(parentCtx)

			// Each goroutine mutates its own clone.
			clone.AttrBag = append(clone.AttrBag, attribute.Int("goroutine", id))
			clone.HeaderID = "modified"
		}(i)
	}

	wg.Wait()

	// After all goroutines complete, the original must be untouched.
	assert.Equal(t, "hdr-concurrent", original.HeaderID)
	assert.Len(t, original.AttrBag, 1)
	assert.Equal(t, "value", original.AttrBag[0].Value.AsString())
}
