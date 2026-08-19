package redis

import (
	"context"

	"github.com/LerianStudio/lib-observability/v2/log"
	"github.com/LerianStudio/lib-observability/v3/redisobs"
	"github.com/redis/go-redis/v9"
)

// instrumentClient applies the platform's default redis telemetry to a freshly
// built go-redis client: command spans plus the db.client.connections.* pool
// instruments, tagged with db.system=redis and the pool.name redisotel derives
// from the server address.
//
// Centralizing this here is the point of the whole wiring: a service gets the
// same series, with the same names and labels, by bumping lib-commons — nothing
// to wire per service (lib-observability docs/metrics-contract.md).
//
// Providers are resolved from the process-global OpenTelemetry providers, which
// Telemetry.ApplyGlobals installs at bootstrap. A service that never calls
// ApplyGlobals gets no-op instrumentation, never an error.
//
// WHY Setup AND NOT Instrument: this Client REPLACES its go-redis client — on a
// reconnect, on an IAM token refresh, on any failover. The pool-stat
// instruments are ASYNCHRONOUS: their callbacks are owned by the MeterProvider,
// not by the client, so they happily outlive the client they observe. Instrument
// hands back nothing that can cancel them, so every replacement would leave
// another set of callbacks reporting a dead pool for the rest of the process
// lifetime, accumulating one set per reconnect. Setup returns the cleanup that
// releases them, and the caller is responsible for running it — see
// releaseInstrumentation for where.
//
// Telemetry must never cost the cache: on failure the client is used as-is and
// the reason is logged at warn. The returned cleanup is always non-nil and safe
// to call more than once.
func (c *Client) instrumentClient(ctx context.Context, rdb redis.UniversalClient) redisobs.CleanupFunc {
	cleanup, err := redisobs.Setup(rdb)
	if err != nil {
		c.logTelemetry(ctx, log.LevelWarn,
			"redis auto-instrumentation degraded; continuing", log.Err(err))
	}

	// Setup contracts a callable cleanup even on error, but stay defensive: a nil
	// here would panic the very connect path this is supposed to leave untouched.
	if cleanup == nil {
		return func() error { return nil }
	}

	return cleanup
}

// releaseInstrumentation unregisters the telemetry bound to ONE go-redis client.
// It MUST run whenever that client is replaced, discarded after a failed
// verification, or closed — otherwise the asynchronous pool-stat callbacks keep
// observing a client that no longer exists.
//
// The release is asynchronous: redisobs closes a channel that redisotel watches
// from its own goroutine, so the instruments disappear shortly after this
// returns rather than synchronously inside it. Nothing observes the dead client
// in the meantime beyond the next collection cycle at most.
//
// Cleanups are idempotent (redisobs guarantees it), so calling this twice for
// the same client is safe — a Close racing a swap never double-releases.
//
// Callers already hold c.mu; this only closes a channel and never re-enters the
// Client, so it is safe to run under that lock.
func (c *Client) releaseInstrumentation(ctx context.Context, cleanup redisobs.CleanupFunc) {
	if cleanup == nil {
		return
	}

	if err := cleanup(); err != nil {
		c.logTelemetry(ctx, log.LevelWarn,
			"failed to unregister redis client metrics", log.Err(err))
	}
}

// logTelemetry emits a telemetry-related log entry. It tolerates a nil logger on
// purpose: telemetry reporting must not be the thing that panics a hand-built
// Client that reached the reconnect path without going through New.
func (c *Client) logTelemetry(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
	if c == nil || c.logger == nil {
		return
	}

	if !c.logger.Enabled(level) {
		return
	}

	c.logger.Log(ctx, level, msg, fields...)
}
