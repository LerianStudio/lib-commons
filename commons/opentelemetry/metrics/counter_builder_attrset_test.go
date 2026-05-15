//go:build unit

package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

// ---------------------------------------------------------------------------
// CounterBuilder.WithAttributeSet
//
// Locks the new pre-built attribute.Set path: semantic parity with
// WithAttributes, precedence over the slice path, empty-set behavior, and
// allocation-count on the hot path.
// ---------------------------------------------------------------------------

// TestCounterBuilder_WithAttributeSet_SemanticEquivalence verifies that
// WithAttributeSet produces the same exported attribute set as the equivalent
// WithAttributes call. Two counters emit one Add each with the same attribute
// payload via different paths; both must yield a single data point with
// matching attributes and the same recorded value.
func TestCounterBuilder_WithAttributeSet_SemanticEquivalence(t *testing.T) {
	factory, reader := newTestFactory(t)

	// Slice path
	sliceCounter, err := factory.Counter(Metric{Name: "parity_slice_counter"})
	require.NoError(t, err)

	sliceBuilder := sliceCounter.WithAttributes(
		attribute.String("result", "hit"),
		attribute.String("pool", "primary"),
	)
	require.NoError(t, sliceBuilder.Add(context.Background(), 7))

	// Pre-built Set path
	setCounter, err := factory.Counter(Metric{Name: "parity_set_counter"})
	require.NoError(t, err)

	preSet := attribute.NewSet(
		attribute.String("result", "hit"),
		attribute.String("pool", "primary"),
	)
	setBuilder := setCounter.WithAttributeSet(preSet)
	require.NoError(t, setBuilder.Add(context.Background(), 7))

	rm := collectMetrics(t, reader)

	sliceM := findMetricByName(rm, "parity_slice_counter")
	require.NotNil(t, sliceM)
	setM := findMetricByName(rm, "parity_set_counter")
	require.NotNil(t, setM)

	sliceDPs := sumDataPoints(t, sliceM)
	setDPs := sumDataPoints(t, setM)

	require.Len(t, sliceDPs, 1)
	require.Len(t, setDPs, 1)

	assert.Equal(t, sliceDPs[0].Value, setDPs[0].Value, "recorded values must match")

	// Both data points must carry the same attribute payload.
	assert.True(t, hasAttribute(sliceDPs[0].Attributes, "result", "hit"))
	assert.True(t, hasAttribute(sliceDPs[0].Attributes, "pool", "primary"))
	assert.True(t, hasAttribute(setDPs[0].Attributes, "result", "hit"))
	assert.True(t, hasAttribute(setDPs[0].Attributes, "pool", "primary"))
	assert.Equal(t, sliceDPs[0].Attributes.Len(), setDPs[0].Attributes.Len(),
		"both paths must emit the same number of attributes")
}

// TestCounterBuilder_WithAttributeSet_EmptySet verifies that an empty
// attribute.Set still routes through the Set path and produces a data point
// with zero attributes (matching the empty-slice behavior).
func TestCounterBuilder_WithAttributeSet_EmptySet(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "empty_set_counter"})
	require.NoError(t, err)

	builder := counter.WithAttributeSet(attribute.NewSet())
	require.NoError(t, builder.Add(context.Background(), 3))
	require.NoError(t, builder.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "empty_set_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.Equal(t, int64(4), dps[0].Value, "Add(3) + AddOne should sum to 4")
	assert.Equal(t, 0, dps[0].Attributes.Len(),
		"empty Set must produce zero attributes on the data point")
}

// TestCounterBuilder_WithAttributeSet_PrecedenceOverSlice locks the documented
// precedence: when a builder carries both a slice (from WithAttributes) and a
// pre-built Set (from WithAttributeSet), the Set wins on the hot path and the
// slice is ignored.
//
// We chain WithAttributes(slice-only attrs) then WithAttributeSet(set-only
// attrs) and verify the recorded data point carries ONLY the set-only
// attributes — proof that the slice path is not consulted on Add.
func TestCounterBuilder_WithAttributeSet_PrecedenceOverSlice(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "precedence_counter"})
	require.NoError(t, err)

	preSet := attribute.NewSet(attribute.String("source", "set"))

	chained := counter.
		WithAttributes(attribute.String("source", "slice")).
		WithAttributeSet(preSet)
	require.NoError(t, chained.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "precedence_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1, "Set path must produce exactly one data point; the slice attrs must not surface as a second series")

	assert.True(t, hasAttribute(dps[0].Attributes, "source", "set"),
		"WithAttributeSet must take precedence and surface the Set's attribute")
	assert.False(t, hasAttribute(dps[0].Attributes, "source", "slice"),
		"slice-path attribute must NOT surface when WithAttributeSet is in effect")
}

// TestCounterBuilder_WithAttributeSet_DownstreamWithAttributesClearsSet
// verifies the documented downstream-branch semantics: chaining WithAttributes
// or WithLabels AFTER WithAttributeSet returns the new builder to the slice
// path, so the new attributes are honored.
func TestCounterBuilder_WithAttributeSet_DownstreamWithAttributesClearsSet(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "downstream_clears_counter"})
	require.NoError(t, err)

	preSet := attribute.NewSet(attribute.String("origin", "set"))

	// After WithAttributes, the new builder is on the slice path again.
	chained := counter.
		WithAttributeSet(preSet).
		WithAttributes(attribute.String("extra", "added"))
	require.NoError(t, chained.AddOne(context.Background()))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "downstream_clears_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 1)
	assert.True(t, hasAttribute(dps[0].Attributes, "extra", "added"),
		"downstream WithAttributes attribute must be honored after returning to slice path")
}

// TestCounterBuilder_WithAttributeSet_NilReceiver verifies the nil-safety
// invariant matches the other builder methods.
func TestCounterBuilder_WithAttributeSet_NilReceiver(t *testing.T) {
	var c *CounterBuilder
	got := c.WithAttributeSet(attribute.NewSet(attribute.String("k", "v")))
	assert.Nil(t, got, "nil receiver must return nil builder, matching WithLabels/WithAttributes")
}

// TestCounterBuilder_WithAttributeSet_Immutability verifies that branching
// off the same parent with two different pre-built Sets produces two
// independent data points, matching the established WithLabels immutability
// contract.
func TestCounterBuilder_WithAttributeSet_Immutability(t *testing.T) {
	factory, reader := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "set_immut_counter"})
	require.NoError(t, err)

	hitSet := attribute.NewSet(attribute.String("result", "hit"))
	missSet := attribute.NewSet(attribute.String("result", "miss"))

	hitBuilder := counter.WithAttributeSet(hitSet)
	missBuilder := counter.WithAttributeSet(missSet)

	require.NoError(t, hitBuilder.Add(context.Background(), 100))
	require.NoError(t, missBuilder.Add(context.Background(), 3))

	rm := collectMetrics(t, reader)
	m := findMetricByName(rm, "set_immut_counter")
	require.NotNil(t, m)

	dps := sumDataPoints(t, m)
	require.Len(t, dps, 2, "two Set branches must produce two separate data points")

	foundHit, foundMiss := false, false
	for _, dp := range dps {
		if hasAttribute(dp.Attributes, "result", "hit") {
			foundHit = true
			assert.Equal(t, int64(100), dp.Value)
		}
		if hasAttribute(dp.Attributes, "result", "miss") {
			foundMiss = true
			assert.Equal(t, int64(3), dp.Value)
		}
	}
	assert.True(t, foundHit, "must find result=hit data point")
	assert.True(t, foundMiss, "must find result=miss data point")
}

// TestCounterBuilder_WithAttributeSet_NegativeValueRejected confirms the
// monotonicity guard still fires on the Set path.
func TestCounterBuilder_WithAttributeSet_NegativeValueRejected(t *testing.T) {
	factory, _ := newTestFactory(t)

	counter, err := factory.Counter(Metric{Name: "neg_set_counter"})
	require.NoError(t, err)

	builder := counter.WithAttributeSet(attribute.NewSet(attribute.String("k", "v")))
	err = builder.Add(context.Background(), -1)
	require.ErrorIs(t, err, ErrNegativeCounterValue)
}

// BenchmarkCounterBuilder_Add benchmarks both paths so the perf claim is
// visible in `go test -bench=. -benchmem`. Not a correctness gate — kept
// alongside the assertion-based tests so reviewers can see absolute numbers.
func BenchmarkCounterBuilder_Add_SetPath(b *testing.B) {
	factory := NewNopFactory()
	counter, err := factory.Counter(Metric{Name: "bench_set_counter"})
	if err != nil {
		b.Fatal(err)
	}
	preSet := attribute.NewSet(
		attribute.String("result", "hit"),
		attribute.String("pool", "primary"),
	)
	builder := counter.WithAttributeSet(preSet)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = builder.Add(ctx, 1)
	}
}

func BenchmarkCounterBuilder_Add_SlicePath(b *testing.B) {
	factory := NewNopFactory()
	counter, err := factory.Counter(Metric{Name: "bench_slice_counter"})
	if err != nil {
		b.Fatal(err)
	}
	builder := counter.WithAttributes(
		attribute.String("result", "hit"),
		attribute.String("pool", "primary"),
	)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = builder.Add(ctx, 1)
	}
}
