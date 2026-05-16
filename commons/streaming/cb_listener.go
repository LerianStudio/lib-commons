package streaming

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-observability/runtime"
)

// streamingStateListener satisfies circuitbreaker.StateChangeListener for a
// single *Producer. The Manager notifies ALL registered listeners on every
// state transition across every service, so OnStateChange filters on
// producer.cbServiceName before acting.
//
// The Manager already runs each OnStateChange call in a
// SafeGoWithContextAndComponent goroutine with a 10-second context deadline
// (see circuitbreaker/manager.go). So we do NOT spawn our own goroutine and
// we do NOT hold any lock. OnStateChange performs a single atomic store and a
// single log call — both bounded in time, both panic-safe through
// runtime.RecoverAndLog, both per TRD §9 "no I/O in the listener".
//
// The constraint that makes this work: the circuit-breaker Manager's 10s
// listener deadline is plenty because the listener only touches atomics +
// logger. See TRD R5.
type streamingStateListener struct {
	// producer is the *Producer we mirror state onto. Never nil in normal
	// construction — listener registration happens after Producer is
	// assembled. Guarded defensively in OnStateChange anyway.
	producer *Producer
}

// OnStateChange is invoked by the circuit-breaker Manager on every state
// transition for any registered service. This implementation:
//
//  1. Recovers from panics via runtime.RecoverAndLog (belt-and-suspenders;
//     the Manager also wraps us in SafeGo).
//  2. Filters events by serviceName — other Producers in the same manager
//     don't own our state flag.
//  3. Maps the State enum to our int32 flag via an atomic store.
//  4. Emits a single structured log line per transition.
//
// No I/O. No broker calls. No outbox writes. Keeping this function sub-
// millisecond is a TRD R5 requirement; tests assert it.
func (l *streamingStateListener) OnStateChange(
	ctx context.Context,
	serviceName string,
	from circuitbreaker.State,
	to circuitbreaker.State,
) {
	if l == nil || l.producer == nil {
		return
	}

	// Defense in depth — the Manager already wraps listeners in SafeGo with
	// its own recover, but a panic in a logger implementation would otherwise
	// poison the Manager goroutine. RecoverAndLog is cheap and side-effect-
	// free on the happy path.
	defer runtime.RecoverAndLog(l.producer.logger, "streaming.cb_state_listener")

	// Filter on service name. The Manager notifies every listener for every
	// service; we only care about our own breaker. Cheap string compare, so
	// it's fine to do before the switch.
	if serviceName != l.producer.cbServiceName {
		return
	}

	// Producer identity (producer_id) goes on every log line per TRD §7.3
	// so ops tooling can correlate CB transitions with emit log lines. The
	// "service" field remains for back-compat with any pre-existing
	// dashboards that key on it.
	producerIDField := log.String("producer_id", l.producer.producerID)

	switch to {
	case circuitbreaker.StateClosed:
		l.producer.cbStateFlag.Store(flagCBClosed)
		l.producer.metrics.recordCircuitState(ctx, flagCBClosed)
		l.producer.logger.Log(ctx, log.LevelInfo, "streaming: circuit closed",
			producerIDField,
			log.String("service", serviceName),
			log.String("from", string(from)),
			log.String("to", string(to)),
		)
	case circuitbreaker.StateHalfOpen:
		l.producer.cbStateFlag.Store(flagCBHalfOpen)
		l.producer.metrics.recordCircuitState(ctx, flagCBHalfOpen)
		l.producer.logger.Log(ctx, log.LevelInfo, "streaming: circuit half-open",
			producerIDField,
			log.String("service", serviceName),
			log.String("from", string(from)),
			log.String("to", string(to)),
		)
	case circuitbreaker.StateOpen:
		l.producer.cbStateFlag.Store(flagCBOpen)
		l.producer.metrics.recordCircuitState(ctx, flagCBOpen)
		l.producer.logger.Log(ctx, log.LevelWarn, "streaming: circuit open",
			producerIDField,
			log.String("service", serviceName),
			log.String("from", string(from)),
			log.String("to", string(to)),
		)
	case circuitbreaker.StateUnknown:
		// Unknown state shouldn't happen for a registered breaker; log a
		// warning so operators notice if the circuitbreaker package ever
		// introduces a new state without updating this switch. NOT writing
		// to the metric gauge here — writing an unknown numeric would
		// corrupt the gauge semantics (0/1/2 closed/half-open/open).
		l.producer.logger.Log(ctx, log.LevelWarn, "streaming: circuit state unknown",
			producerIDField,
			log.String("service", serviceName),
			log.String("from", string(from)),
			log.String("to", string(to)),
		)
	}
}
