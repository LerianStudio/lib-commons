// Package runtime provides panic recovery utilities for services with
// full observability integration.
//
// This package offers policy-based panic recovery primitives that integrate
// with lib-commons logging, OpenTelemetry metrics/tracing, and optional
// error tracking services like Sentry.
//
// # Panic Policies
//
// Two panic policies are supported:
//
//   - KeepRunning: Log the panic and stack trace, then continue execution.
//     Use this for worker goroutines and HTTP/gRPC handlers.
//
//   - CrashProcess: Log the panic and stack trace, then re-panic to crash
//     the process. Use this for critical invariant violations where continuing
//     would cause data corruption.
//
// # Safe Goroutine Launching
//
// Use SafeGo and SafeGoWithContext to launch goroutines with automatic panic
// recovery and observability:
//
//	// Basic (no observability)
//	runtime.SafeGo(logger, "background-task", runtime.KeepRunning, func() {
//	    doWork()
//	})
//
//	// With full observability (recommended)
//	runtime.SafeGoWithContextAndComponent(ctx, logger, "transaction", "balance-sync", runtime.KeepRunning,
//	    func(ctx context.Context) {
//	        syncBalances(ctx)
//	    })
//
// # Deferred Recovery
//
// Use RecoverAndLog, RecoverAndCrash, or RecoverWithPolicy in defer statements.
// Context-aware variants provide full observability:
//
//	func handler(ctx context.Context) {
//	    defer runtime.RecoverAndLogWithContext(ctx, logger, "transaction", "handler")
//	    // Panics here will be logged, recorded as metrics, and added to the trace
//	}
//
// # Observability Integration
//
// The package integrates with three observability systems:
//
//  1. Metrics: Records panic_recovered_total counter with component and goroutine_name labels.
//     Initialize with InitPanicMetrics(metricsFactory).
//
//  2. Tracing: Records panic.recovered span events with stack traces and sets span status to Error.
//     Automatically uses the span from the context.
//
//  3. Error Reporting: Optionally reports panics to services like Sentry.
//     Configure with SetErrorReporter(reporter).
//
// # Initialization
//
// During application startup, initialize the observability integrations:
//
//	tl, err := opentelemetry.NewTelemetry(cfg)
//	if err != nil {
//	    return err
//	}
//	runtime.InitPanicMetrics(tl.MetricsFactory)
//
//	// Optional: Configure Sentry or other error reporter
//	runtime.SetErrorReporter(mySentryReporter)
//
// # Stack Traces
//
// All recovery functions capture and log the full stack trace using
// runtime/debug.Stack() for debugging purposes.
//
// Deprecated: This package is a compatibility shim. Import github.com/LerianStudio/lib-observability/runtime instead.
// This package will be removed in a future major version of lib-commons.
package runtime
