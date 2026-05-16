package runtime

import (
	"context"

	libobsruntime "github.com/LerianStudio/lib-observability/runtime"
)

func safeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}

// Logger defines the minimal logging interface required by runtime.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.Logger instead.
type Logger = libobsruntime.Logger

// RecoverAndLog recovers from a panic, logs it with the stack trace, and continues execution.
//
// SHIM NOTE: recover() must be called directly by the deferred function — Go's runtime
// checks the call frame and returns nil if recover() is nested inside an intermediate
// wrapper. Because this function is the shim that callers defer, we must call recover()
// here rather than delegating to lib-observability's RecoverAndLog. After capturing the
// panic value we pass it to HandlePanicValue for logging and observability.
// This indirection will be removed once callers migrate to lib-observability directly.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.RecoverAndLog instead.
func RecoverAndLog(logger Logger, name string) {
	r := recover()
	if r == nil {
		return
	}

	libobsruntime.HandlePanicValue(context.Background(), logger, r, "", name)
}

// RecoverAndLogWithContext is like RecoverAndLog but with full observability integration.
//
// SHIM NOTE: Same recover() constraint as RecoverAndLog — must capture the panic here
// before delegating. Will be removed once callers migrate to lib-observability directly.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.RecoverAndLogWithContext instead.
func RecoverAndLogWithContext(ctx context.Context, logger Logger, component, name string) {
	r := recover()
	if r == nil {
		return
	}

	libobsruntime.HandlePanicValue(safeContext(ctx), logger, r, component, name)
}

// RecoverAndCrash recovers from a panic, logs it, and re-panics to crash the process.
//
// SHIM NOTE: Same recover() constraint as RecoverAndLog — must capture the panic here.
// The re-panic below is intentional and is the original contract of this function:
// it is used in critical paths where continuing after a panic would leave the process
// in a corrupt state. This is one of the very few places where panic is acceptable.
// Will be removed once callers migrate to lib-observability directly.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.RecoverAndCrash instead.
func RecoverAndCrash(logger Logger, name string) {
	r := recover()
	if r == nil {
		return
	}

	libobsruntime.HandlePanicValue(context.Background(), logger, r, "", name)
	panic(r) // intentional re-panic — this function's contract requires crashing the process
}

// RecoverAndCrashWithContext is like RecoverAndCrash but with full observability integration.
//
// SHIM NOTE: Same recover() constraint as RecoverAndLog. Re-panic is intentional — see
// RecoverAndCrash. Will be removed once callers migrate to lib-observability directly.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.RecoverAndCrashWithContext instead.
func RecoverAndCrashWithContext(ctx context.Context, logger Logger, component, name string) {
	r := recover()
	if r == nil {
		return
	}

	libobsruntime.HandlePanicValue(safeContext(ctx), logger, r, component, name)
	panic(r) // intentional re-panic — this function's contract requires crashing the process
}

// RecoverWithPolicy recovers from a panic and handles it according to the specified policy.
//
// SHIM NOTE: Same recover() constraint as RecoverAndLog. Re-panic only occurs when
// policy == CrashProcess, which preserves the original crash-the-process contract.
// Will be removed once callers migrate to lib-observability directly.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.RecoverWithPolicy instead.
func RecoverWithPolicy(logger Logger, name string, policy PanicPolicy) {
	r := recover()
	if r == nil {
		return
	}

	libobsruntime.HandlePanicValue(context.Background(), logger, r, "", name)

	if policy == CrashProcess {
		panic(r) // intentional re-panic — CrashProcess policy requires crashing the process
	}
}

// RecoverWithPolicyAndContext is like RecoverWithPolicy but with full observability integration.
//
// SHIM NOTE: Same recover() constraint as RecoverAndLog. Re-panic only on CrashProcess.
// Will be removed once callers migrate to lib-observability directly.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.RecoverWithPolicyAndContext instead.
func RecoverWithPolicyAndContext(ctx context.Context, logger Logger, component, name string, policy PanicPolicy) {
	r := recover()
	if r == nil {
		return
	}

	libobsruntime.HandlePanicValue(safeContext(ctx), logger, r, component, name)

	if policy == CrashProcess {
		panic(r) // intentional re-panic — CrashProcess policy requires crashing the process
	}
}

// HandlePanicValue processes a panic value that was already recovered by an external mechanism.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.HandlePanicValue instead.
func HandlePanicValue(ctx context.Context, logger Logger, panicValue any, component, name string) {
	libobsruntime.HandlePanicValue(safeContext(ctx), logger, panicValue, component, name)
}
