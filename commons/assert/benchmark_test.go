//go:build unit

package assert

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

// Benchmarks verify assertions are lightweight enough for always-on usage.
// Target: < 100ns for hot path (condition is true), zero allocations.

// --- Core Assertion Benchmarks (Hot Path) ---

func BenchmarkThat_True(b *testing.B) {
	asserter := New(context.Background(), nil, "", "")
	for i := 0; i < b.N; i++ {
		_ = asserter.That(context.Background(), true, "benchmark test")
	}
}

func BenchmarkThat_TrueWithContext(b *testing.B) {
	asserter := New(context.Background(), nil, "", "")
	for i := 0; i < b.N; i++ {
		_ = asserter.That(
			context.Background(),
			true,
			"benchmark test",
			"key1",
			"value1",
			"key2",
			42,
		)
	}
}

func BenchmarkNotNil_NonNil(b *testing.B) {
	asserter := New(context.Background(), nil, "", "")

	v := "test"

	for i := 0; i < b.N; i++ {
		_ = asserter.NotNil(context.Background(), v, "benchmark test")
	}
}

func BenchmarkNotNil_NonNilPointer(b *testing.B) {
	asserter := New(context.Background(), nil, "", "")

	x := 42
	ptr := &x

	for i := 0; i < b.N; i++ {
		_ = asserter.NotNil(context.Background(), ptr, "benchmark test")
	}
}

func BenchmarkNotEmpty_NonEmpty(b *testing.B) {
	asserter := New(context.Background(), nil, "", "")

	s := "test"

	for i := 0; i < b.N; i++ {
		_ = asserter.NotEmpty(context.Background(), s, "benchmark test")
	}
}

func BenchmarkNoError_NilError(b *testing.B) {
	asserter := New(context.Background(), nil, "", "")
	for i := 0; i < b.N; i++ {
		_ = asserter.NoError(context.Background(), nil, "benchmark test")
	}
}

// --- Predicate Benchmarks ---

func BenchmarkPositive(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Positive(int64(i + 1))
	}
}

func BenchmarkNonNegative(b *testing.B) {
	for i := 0; i < b.N; i++ {
		NonNegative(int64(i))
	}
}

func BenchmarkInRange(b *testing.B) {
	for i := 0; i < b.N; i++ {
		InRange(5, 0, 10)
	}
}

func BenchmarkValidUUID(b *testing.B) {
	uuid := "123e4567-e89b-12d3-a456-426614174000"
	for i := 0; i < b.N; i++ {
		ValidUUID(uuid)
	}
}

func BenchmarkValidAmount(b *testing.B) {
	amount := decimal.NewFromFloat(1234.56)
	for i := 0; i < b.N; i++ {
		ValidAmount(amount)
	}
}

func BenchmarkPositiveDecimal(b *testing.B) {
	amount := decimal.NewFromFloat(1234.56)
	for i := 0; i < b.N; i++ {
		PositiveDecimal(amount)
	}
}

func BenchmarkValidScale(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ValidScale(8)
	}
}

// --- Helper Function Benchmarks ---

func BenchmarkIsNil_NonNil(b *testing.B) {
	v := "test"
	for i := 0; i < b.N; i++ {
		isNil(v)
	}
}

func BenchmarkIsNil_TypedNilPointer(b *testing.B) {
	var ptr *int
	for i := 0; i < b.N; i++ {
		isNil(ptr)
	}
}

// --- Combined Usage Benchmarks ---

// BenchmarkTypicalAssertion simulates a typical assertion pattern.
func BenchmarkTypicalAssertion(b *testing.B) {
	asserter := New(context.Background(), nil, "", "")
	id := "123e4567-e89b-12d3-a456-426614174000"
	amount := decimal.NewFromFloat(100.50)

	for i := 0; i < b.N; i++ {
		_ = asserter.That(context.Background(), ValidUUID(id), "invalid id", "id", id)
		_ = asserter.That(
			context.Background(),
			PositiveDecimal(amount),
			"invalid amount",
			"amount",
			amount,
		)
	}
}
