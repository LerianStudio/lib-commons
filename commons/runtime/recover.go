package runtime

import (
	"context"

	libobsruntime "github.com/LerianStudio/lib-observability/runtime"
)

// Logger defines the minimal logging interface required by runtime.
type Logger = libobsruntime.Logger

// RecoverAndLog recovers from a panic, logs it with the stack trace, and continues execution.
func RecoverAndLog(logger Logger, name string) {
	libobsruntime.RecoverAndLog(logger, name)
}

// RecoverAndLogWithContext is like RecoverAndLog but with full observability integration.
func RecoverAndLogWithContext(ctx context.Context, logger Logger, component, name string) {
	libobsruntime.RecoverAndLogWithContext(ctx, logger, component, name)
}

// RecoverAndCrash recovers from a panic, logs it, and re-panics to crash the process.
func RecoverAndCrash(logger Logger, name string) {
	libobsruntime.RecoverAndCrash(logger, name)
}

// RecoverAndCrashWithContext is like RecoverAndCrash but with full observability integration.
func RecoverAndCrashWithContext(ctx context.Context, logger Logger, component, name string) {
	libobsruntime.RecoverAndCrashWithContext(ctx, logger, component, name)
}

// RecoverWithPolicy recovers from a panic and handles it according to the specified policy.
func RecoverWithPolicy(logger Logger, name string, policy PanicPolicy) {
	libobsruntime.RecoverWithPolicy(logger, name, policy)
}

// RecoverWithPolicyAndContext is like RecoverWithPolicy but with full observability integration.
func RecoverWithPolicyAndContext(ctx context.Context, logger Logger, component, name string, policy PanicPolicy) {
	libobsruntime.RecoverWithPolicyAndContext(ctx, logger, component, name, policy)
}

// HandlePanicValue processes a panic value that was already recovered by an external mechanism.
func HandlePanicValue(ctx context.Context, logger Logger, panicValue any, component, name string) {
	libobsruntime.HandlePanicValue(ctx, logger, panicValue, component, name)
}
