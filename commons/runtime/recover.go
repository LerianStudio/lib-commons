package runtime

import (
	"context"
	"runtime/debug"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
)

// Logger defines the minimal logging interface required by runtime.
// This interface is satisfied by github.com/LerianStudio/lib-commons/v4/commons/log.Logger.
type Logger interface {
	Log(ctx context.Context, level log.Level, msg string, fields ...log.Field)
}

// RecoverAndLog recovers from a panic, logs it with the stack trace, and
// continues execution. Use this in defer statements for handlers and workers
// where you want to prevent crashes.
//
// Note: This function does not record metrics or span events because it lacks
// context. For observability integration, use RecoverAndLogWithContext instead.
//
// Example:
//
//	func worker() {
//	    defer runtime.RecoverAndLog(logger, "worker")
//	    // ...
//	}
func RecoverAndLog(logger Logger, name string) {
	if r := recover(); r != nil {
		logPanic(logger, name, r)
	}
}

// RecoverAndLogWithContext is like RecoverAndLog but with full observability integration.
// It records metrics, span events, and reports to error tracking services.
//
// Parameters:
//   - ctx: Context for observability (metrics, tracing, error reporting)
//   - logger: Logger for structured logging
//   - component: The service component (e.g., "transaction", "onboarding")
//   - name: Descriptive name for the goroutine or handler
//
// Example:
//
//	func handler(ctx context.Context) {
//	    defer runtime.RecoverAndLogWithContext(ctx, logger, "transaction", "create_handler")
//	    // ...
//	}
func RecoverAndLogWithContext(ctx context.Context, logger Logger, component, name string) {
	if r := recover(); r != nil {
		stack := debug.Stack()
		logPanicWithStack(logger, name, r, stack)
		recordPanicObservability(ctx, r, stack, component, name)
	}
}

// RecoverAndCrash recovers from a panic, logs it with the stack trace, and
// re-panics to crash the process. Use this in defer statements for critical
// operations where continuing after a panic would be dangerous.
//
// Example:
//
//	func criticalOperation() {
//	    defer runtime.RecoverAndCrash(logger, "critical-op")
//	    // ...
//	}
func RecoverAndCrash(logger Logger, name string) {
	if r := recover(); r != nil {
		logPanic(logger, name, r)
		panic(r)
	}
}

// RecoverAndCrashWithContext is like RecoverAndCrash but with full observability integration.
// It records metrics and span events before re-panicking.
//
// Parameters:
//   - ctx: Context for observability (metrics, tracing, error reporting)
//   - logger: Logger for structured logging
//   - component: The service component (e.g., "transaction", "onboarding")
//   - name: Descriptive name for the goroutine or handler
func RecoverAndCrashWithContext(ctx context.Context, logger Logger, component, name string) {
	if r := recover(); r != nil {
		stack := debug.Stack()
		logPanicWithStack(logger, name, r, stack)
		recordPanicObservability(ctx, r, stack, component, name)
		panic(r)
	}
}

// RecoverWithPolicy recovers from a panic and handles it according to the
// specified policy. Use this when the recovery behavior needs to be determined
// at runtime.
//
// Note: This function does not record metrics or span events because it lacks
// context. For observability integration, use RecoverWithPolicyAndContext instead.
//
// Example:
//
//	func flexibleHandler(policy runtime.PanicPolicy) {
//	    defer runtime.RecoverWithPolicy(logger, "handler", policy)
//	    // ...
//	}
func RecoverWithPolicy(logger Logger, name string, policy PanicPolicy) {
	if r := recover(); r != nil {
		logPanic(logger, name, r)

		if policy == CrashProcess {
			panic(r)
		}
	}
}

// RecoverWithPolicyAndContext is like RecoverWithPolicy but with full observability integration.
// It records metrics, span events, and reports to error tracking services.
//
// Parameters:
//   - ctx: Context for observability (metrics, tracing, error reporting)
//   - logger: Logger for structured logging
//   - component: The service component (e.g., "transaction", "onboarding")
//   - name: Descriptive name for the goroutine or handler
//   - policy: How to handle the panic after logging/recording
//
// Example:
//
//	func worker(ctx context.Context, policy runtime.PanicPolicy) {
//	    defer runtime.RecoverWithPolicyAndContext(ctx, logger, "transaction", "balance_worker", policy)
//	    // ...
//	}
func RecoverWithPolicyAndContext(
	ctx context.Context,
	logger Logger,
	component, name string,
	policy PanicPolicy,
) {
	if recovered := recover(); recovered != nil {
		stack := debug.Stack()
		logPanicWithStack(logger, name, recovered, stack)
		recordPanicObservability(ctx, recovered, stack, component, name)

		if policy == CrashProcess {
			panic(recovered)
		}
	}
}

// logPanic logs the panic value and stack trace using the provided logger.
// This is the legacy function that captures stack internally.
func logPanic(logger Logger, name string, panicValue any) {
	stack := debug.Stack()
	logPanicWithStack(logger, name, panicValue, stack)
}

// logPanicWithStack logs the panic with a pre-captured stack trace.
// In production mode, panic values are redacted to prevent leaking sensitive data.
func logPanicWithStack(logger Logger, name string, panicValue any, stack []byte) {
	if logger == nil {
		// Last resort fallback - should never happen in production
		return
	}

	if IsProductionMode() {
		logger.Log(context.Background(), log.LevelError,
			"panic recovered",
			log.String("source", name),
			log.String("value", redactedPanicMsg),
		)

		return
	}

	logger.Log(context.Background(), log.LevelError,
		"panic recovered",
		log.String("source", name),
		log.Any("value", panicValue),
		log.String("stack_trace", string(stack)),
	)
}

// recordPanicObservability records panic information to all configured observability systems.
// This includes metrics, distributed tracing, and error reporting services.
func recordPanicObservability(
	ctx context.Context,
	panicValue any,
	stack []byte,
	component, name string,
) {
	// Record metric
	recordPanicMetric(ctx, component, name)

	// Record span event
	RecordPanicToSpanWithComponent(ctx, panicValue, stack, component, name)

	// Report to error tracking service (e.g., Sentry) if configured
	reportPanicToErrorService(ctx, panicValue, stack, component, name)
}

// HandlePanicValue processes a panic value that was already recovered by an external
// mechanism (e.g., Fiber's recover middleware). This function logs and records
// observability data without calling recover() itself.
//
// Use this when integrating with frameworks that provide their own panic recovery
// but still need our observability pipeline.
//
// Parameters:
//   - ctx: Context for observability (metrics, tracing, error reporting)
//   - logger: Logger for structured logging
//   - panicValue: The panic value recovered by the external mechanism
//   - component: The service component (e.g., "matcher", "ingestion")
//   - name: Descriptive name for the handler (e.g., "http_handler")
//
// Example (Fiber middleware):
//
//	recover.New(recover.Config{
//	    StackTraceHandler: func(c *fiber.Ctx, panicValue any) {
//	        ctx := extractContext(c)
//	        runtime.HandlePanicValue(ctx, logger, panicValue, "matcher", "http_handler")
//	    },
//	})
func HandlePanicValue(ctx context.Context, logger Logger, panicValue any, component, name string) {
	if panicValue == nil {
		return
	}

	stack := debug.Stack()
	logPanicWithStack(logger, name, panicValue, stack)
	recordPanicObservability(ctx, panicValue, stack, component, name)
}
