//go:build unit

package runtime

import (
	"context"
	"fmt"

	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
)

// simpleLogger is a minimal logger for examples.
type simpleLogger struct{}

func (l *simpleLogger) Log(_ context.Context, _ libLog.Level, _ string, _ ...libLog.Field) {}

func ExampleSafeGoWithContext() {
	ctx := context.Background()
	logger := &simpleLogger{}

	// Launch a goroutine with panic recovery and observability
	done := make(chan struct{})

	SafeGoWithContextAndComponent(ctx, logger, "transaction", "example-worker", KeepRunning,
		func(ctx context.Context) {
			defer close(done)

			fmt.Println("Worker started")
			// Work happens here...
			fmt.Println("Worker completed")
		})

	<-done
	// Output:
	// Worker started
	// Worker completed
}

func ExampleRecoverAndLogWithContext() {
	ctx := context.Background()
	logger := &simpleLogger{}

	func() {
		defer RecoverAndLogWithContext(ctx, logger, "example", "handler")

		fmt.Println("Before panic")
		// If a panic occurred here, it would be recovered and logged
		fmt.Println("After (no panic)")
	}()

	fmt.Println("Function completed normally")
	// Output:
	// Before panic
	// After (no panic)
	// Function completed normally
}

func ExampleInitPanicMetrics() {
	// During application startup, after telemetry initialization:
	// tl := opentelemetry.InitializeTelemetry(cfg)
	// runtime.InitPanicMetrics(tl.MetricsFactory)

	// Nil is safe (no-op):
	InitPanicMetrics(nil)

	// Metrics remain uninitialized until properly configured.
	pm := GetPanicMetrics()
	fmt.Printf("Metrics initialized: %v\n", pm != nil)
	// Output:
	// Metrics initialized: false
}

func ExampleSetErrorReporter() {
	// Create a custom error reporter (e.g., for Sentry)
	reporter := &customReporter{}

	// Configure during startup
	SetErrorReporter(reporter)

	// Later, panics will be reported automatically
	fmt.Println("Error reporter configured")

	// Clean up
	SetErrorReporter(nil)
	// Output:
	// Error reporter configured
}

type customReporter struct{}

func (r *customReporter) CaptureException(_ context.Context, _ error, _ map[string]string) {
	// In a real implementation, this would send to Sentry or similar
}
