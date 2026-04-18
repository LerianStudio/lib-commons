package streaming

import (
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
)

// Circuit-breaker state-flag values. Stored as int32 so the atomic reads in
// the publish hot path are lock-free. Named constants keep the call sites
// readable and the values stable when T4 expands the switch.
const (
	// flagCBClosed marks the circuit as CLOSED — publishDirect runs normally.
	flagCBClosed int32 = 0
	// flagCBHalfOpen marks the circuit as HALF-OPEN — the breaker allows a
	// limited number of probe calls through.
	flagCBHalfOpen int32 = 1
	// flagCBOpen marks the circuit as OPEN — publishDirect is short-circuited.
	// T3 surfaces ErrCircuitOpen here; T4 replaces with publishToOutbox.
	flagCBOpen int32 = 2
)

// cbServiceNamePrefix is the per-instance service name prefix registered with
// the circuit-breaker manager. Full name is "streaming.producer:<producerID>"
// so multiple Producers in one process each have their own breaker.
const cbServiceNamePrefix = "streaming.producer:"

// initCircuitBreaker wires the Producer up to a circuit-breaker manager. It:
//
//  1. Reuses a caller-supplied manager (via WithCircuitBreakerManager) or
//     constructs one from p.logger.
//  2. Derives the effective CB config from the HTTPServiceConfig preset with
//     non-zero overrides from Config (CBFailureRatio, CBMinRequests,
//     CBTimeout).
//  3. Calls GetOrCreate so this Producer's named breaker is registered.
//  4. Registers the state-change listener so cbStateFlag mirrors breaker
//     state transitions.
//
// Must be called after p.client and p.producerID are set — the service name
// depends on producerID and initCircuitBreaker will NOT close the client on
// failure (caller's responsibility in NewProducer).
//
// Errors are wrapped with fmt.Errorf so callers can errors.Is the underlying
// circuitbreaker sentinel (ErrNilLogger, ErrInvalidConfig, etc.).
func (p *Producer) initCircuitBreaker() error {
	p.cbServiceName = cbServiceNamePrefix + p.producerID

	// Resolve the circuit-breaker manager. Caller-supplied is preferred so
	// the service has a single shared manager (metrics, state listeners,
	// config governance). Otherwise build one from our logger.
	if p.cbManager == nil {
		mgr, err := circuitbreaker.NewManager(p.logger)
		if err != nil {
			// Failure here would only happen for a nil logger, which
			// NewProducer has already substituted to NewNop(). Propagate
			// rather than silently continue without a breaker.
			return fmt.Errorf("streaming: init circuit breaker manager: %w", err)
		}

		p.cbManager = mgr
	}

	// Build the CB config from the preset + overrides.
	cbCfg := buildCBConfig(p.cfg)

	cb, err := p.cbManager.GetOrCreate(p.cbServiceName, cbCfg)
	if err != nil {
		return fmt.Errorf("streaming: register circuit breaker %q: %w", p.cbServiceName, err)
	}

	p.cb = cb

	// Register the state-change listener AFTER the breaker exists so the
	// first legitimate transition can propagate the initial closed state.
	// The Manager invokes OnStateChange in a goroutine under
	// SafeGoWithContextAndComponent, so we do not need our own goroutine —
	// but we DO defend against panics with RecoverAndLog belt-and-suspenders.
	p.cbManager.RegisterStateChangeListener(&streamingStateListener{producer: p})

	return nil
}

// buildCBConfig resolves the effective circuit-breaker configuration. Start
// from the HTTP preset — it's the tightest preset that matches a wire-level
// producer (short timeout, fail-fast) — and apply non-zero Config overrides.
// Zero or negative values are treated as "use the preset", matching the
// documented env-var semantics.
func buildCBConfig(cfg Config) circuitbreaker.Config {
	cbCfg := circuitbreaker.HTTPServiceConfig()

	if cfg.CBFailureRatio > 0 {
		cbCfg.FailureRatio = cfg.CBFailureRatio
	}

	if cfg.CBMinRequests > 0 {
		// CBMinRequests is `int` in streaming.Config but uint32 in
		// circuitbreaker.Config. The preset is a small positive number; env
		// parsers reject negatives upstream, so clamping at zero is safe.
		//nolint:gosec // bounded by the positive-value guard above
		cbCfg.MinRequests = uint32(cfg.CBMinRequests)
	}

	if cfg.CBTimeout > 0 {
		cbCfg.Timeout = cfg.CBTimeout
	}

	return cbCfg
}
