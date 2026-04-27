//go:build unit && !race

// Package systemplane — automated AC15 performance threshold gates.
//
// Build-tag note: `!race` is required because the race detector adds
// 10-50× latency overhead per atomic/mutex op, which makes sub-microsecond
// thresholds meaningless. The race-enabled test lane (make ci / -race)
// silently skips this file; run `go test -tags=unit -run=TestPerf_` without
// `-race` to exercise the gate.
//
// PRD AC15 targets the eager-mode tenant read path:
//
//   - GetForTenant hit:  < 1µs per op
//   - GetForTenant miss: < 2µs per op (eager cascade to legacy global default)
//
// The benchmarks in bench_tenant_test.go observe these numbers but are
// informational unless someone runs `go test -bench=.` and reads the
// output. A silent regression — say, 900ns ballooning to 3µs because
// someone wrapped GetForTenant in a defer-heavy middleware — would go
// unnoticed in regular CI.
//
// These tests convert the targets into hard gates. They reuse the same
// in-memory benchTenantStore the benchmarks use (no production deps),
// compute the average per-op latency via testing.Benchmark (which itself
// re-runs until it has a stable sample), and fail if the measured value
// exceeds the threshold.
//
// Gating:
//
//   - testing.Short() skips the gate entirely — `go test -short` stays fast.
//   - Architecture filter: only amd64 and arm64. On unusual CPUs (e.g. a
//     containerized CI emulator) sub-microsecond targets lose meaning.
//
// These gates deliberately do NOT replace the manual benchmark runs used
// to tune the code path; they exist purely to trip a regression wire.
package systemplane

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// skipPerfGateIfUnsupported consolidates the preconditions for the two
// AC15 gates so any future gate can reuse the same guard.
func skipPerfGateIfUnsupported(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping AC15 perf gate under -short")
	}

	switch runtime.GOARCH {
	case "amd64", "arm64":
		// Supported.
	default:
		t.Skipf("skipping AC15 perf gate on GOARCH=%s (target meaningful only on amd64/arm64)", runtime.GOARCH)
	}
}

// TestPerf_GetForTenant_HitPathUnderThreshold enforces the PRD AC15 target
// for the eager-mode cache-hit path: GetForTenant must complete in under
// 1µs per op on commodity hardware.
//
// The test reuses the exact setup that BenchmarkGetForTenant_Hit uses so
// the threshold and the tracked benchmark stay in lockstep.
func TestPerf_GetForTenant_HitPathUnderThreshold(t *testing.T) {
	skipPerfGateIfUnsupported(t)

	const threshold = time.Microsecond // AC15: < 1µs

	result := testing.Benchmark(func(b *testing.B) {
		c, _ := newBenchClient(b, "global", "fee.rate", 0.05)

		ctx := core.ContextWithTenantID(context.Background(), "tenant-A")

		if err := c.SetForTenant(ctx, "global", "fee.rate", 0.10, "bench"); err != nil {
			b.Fatalf("SetForTenant prime: %v", err)
		}

		b.ReportAllocs()

		// b.Loop() requires Go 1.24+ (inside testing.Benchmark). The repo is
		// pinned to Go 1.25+ in go.mod, so this is safe — but the loop form
		// is new enough to be worth flagging.
		for b.Loop() {
			_, _, _ = c.GetForTenant(ctx, "global", "fee.rate")
		}
	})

	if result.N == 0 {
		t.Fatal("testing.Benchmark produced zero iterations")
	}

	avg := time.Duration(result.NsPerOp())
	if avg >= threshold {
		t.Fatalf("GetForTenant hit path %v/op exceeds AC15 target %v/op (regression)", avg, threshold)
	}

	t.Logf("GetForTenant hit path: %v/op (target < %v/op, N=%d)", avg, threshold, result.N)
}

// TestPerf_GetForTenant_MissPathUnderThreshold enforces the PRD AC15 target
// for the eager-mode cache-miss (cascade-to-global) path: GetForTenant must
// complete in under 2µs per op on commodity hardware.
//
// The test reuses the exact setup that BenchmarkGetForTenant_Miss_Eager
// uses so the threshold and the tracked benchmark stay in lockstep.
func TestPerf_GetForTenant_MissPathUnderThreshold(t *testing.T) {
	skipPerfGateIfUnsupported(t)

	const threshold = 2 * time.Microsecond // AC15: < 2µs

	result := testing.Benchmark(func(b *testing.B) {
		c, _ := newBenchClient(b, "global", "fee.rate", 0.05)

		ctx := core.ContextWithTenantID(context.Background(), "tenant-B")
		// No override written — every read falls through to the registered
		// default on the legacy global cache.

		b.ReportAllocs()

		for b.Loop() {
			_, _, _ = c.GetForTenant(ctx, "global", "fee.rate")
		}
	})

	if result.N == 0 {
		t.Fatal("testing.Benchmark produced zero iterations")
	}

	avg := time.Duration(result.NsPerOp())
	if avg >= threshold {
		t.Fatalf("GetForTenant miss path %v/op exceeds AC15 target %v/op (regression)", avg, threshold)
	}

	t.Logf("GetForTenant miss path: %v/op (target < %v/op, N=%d)", avg, threshold, result.N)
}
