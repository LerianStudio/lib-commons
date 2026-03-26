package runtime

import (
	"context"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
)

type recoveredPanic struct {
	value any
	stack []byte
}

func processRecoveredPanic(
	ctx context.Context,
	logger Logger,
	component, name string,
	policy PanicPolicy,
	withObservability bool,
	recovered *recoveredPanic,
) {
	if recovered == nil {
		return
	}

	// Always use the pre-captured stack regardless of observability mode
	logPanicWithStack(logger, name, recovered.value, recovered.stack)

	if withObservability {
		recordPanicObservability(ctx, recovered.value, recovered.stack, component, name)
	}

	if policy == CrashProcess {
		panic(recovered.value)
	}
}

func warnNilCallback(logger Logger, message, component, goroutine string) {
	if logger == nil {
		return
	}

	fields := []log.Field{log.String("goroutine", goroutine)}
	if component != "" {
		fields = append(fields, log.String("component", component))
	}

	logger.Log(context.Background(), log.LevelWarn, message, fields...)
}
