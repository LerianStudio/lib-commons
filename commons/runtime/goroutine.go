package runtime

import (
	"context"

	libobsruntime "github.com/LerianStudio/lib-observability/runtime"
)

// SafeGo launches a goroutine with panic recovery.
func SafeGo(logger Logger, name string, policy PanicPolicy, fn func()) {
	libobsruntime.SafeGo(logger, name, policy, fn)
}

// SafeGoWithContext launches a goroutine with panic recovery and context propagation.
func SafeGoWithContext(ctx context.Context, logger Logger, name string, policy PanicPolicy, fn func(context.Context)) {
	libobsruntime.SafeGoWithContext(ctx, logger, name, policy, fn)
}

// SafeGoWithContextAndComponent is like SafeGoWithContext but also records the component name.
func SafeGoWithContextAndComponent(ctx context.Context, logger Logger, component, name string, policy PanicPolicy, fn func(context.Context)) {
	libobsruntime.SafeGoWithContextAndComponent(ctx, logger, component, name, policy, fn)
}
