package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-observability/v4/runtime"
	"github.com/gofiber/fiber/v3"
	"github.com/redis/go-redis/v9"
)

// ErrHeartbeatMisconfigured is what [Middleware.Err] wraps when
// [WithProcessingHeartbeat] cannot be honoured: the store does not implement
// [LeaseExtender], or the interval is not shorter than the fixed
// [WithProcessingTTL] lease it is meant to keep alive.
var ErrHeartbeatMisconfigured = errors.New("idempotency: processing heartbeat misconfigured")

// LeaseExtender is the optional store capability [WithProcessingHeartbeat]
// drives. It is a separate interface, not a fourth [Store] method, so stores
// written against the three-method contract keep compiling.
//
// Extend atomically resets the expiration of key to ttl only while the stored
// bytes still equal expected — the same processing bytes [Store.Complete]
// compares against. It returns false, without mutation, when the key expired,
// was completed, released, or re-acquired by another owner, and it never
// recreates an absent key. It must reject a non-positive ttl without mutation.
type LeaseExtender interface {
	Extend(ctx context.Context, key string, expected []byte, ttl time.Duration) (bool, error)
}

var _ LeaseExtender = (*redisStore)(nil)

var redisExtendScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
redis.call("PEXPIRE", KEYS[1], ARGV[2])
return 1
`)

// Extend implements [LeaseExtender] in one round trip.
func (s *redisStore) Extend(ctx context.Context, key string, expected []byte, ttl time.Duration) (bool, error) {
	ttlMillis, err := redisTTLMilliseconds(ttl)
	if err != nil {
		return false, err
	}

	client, err := s.conn.GetClient(ctx)
	if err != nil {
		return false, fmt.Errorf("get redis client: %w", err)
	}

	applied, err := redisExtendScript.Run(ctx, client, []string{key}, expected, ttlMillis).Int64()
	if err != nil {
		return false, fmt.Errorf("extend idempotency key: %w", err)
	}

	return applied == 1, nil
}

// WithProcessingHeartbeat renews the in-flight lease every interval while the
// handler runs, so a handler that legitimately outlives its lease — a streamed
// upload, say — keeps its key instead of letting a duplicate acquire it and run
// the mutation a second time. Each beat resets the lease to the value the
// request acquired it with ([WithProcessingTTL] or
// [WithProcessingTTLProvider], or the retention when neither is set). A
// request whose lease is shorter than three intervals beats every third of its
// lease instead, never faster than every 50ms, and logs one warning saying so;
// size the interval to a few beats per lease to keep that warning quiet. A
// lease below [MinHeartbeatLease] cannot be held at that floor and is refused:
// at construction for a fixed [WithProcessingTTL], per request (503, before
// acquisition) for a lease resolved by a provider or taken from the retention.
//
// The heartbeat starts when the handler starts and stops, synchronously, when
// it returns: no beat runs during or after the completion or release. A beat
// that errors is logged and the next one still runs; a beat the store refuses
// (the record is no longer this request's) is logged and ends the heartbeat.
// Neither ever reaches the caller.
//
// The store must implement [LeaseExtender]. The Redis store does. Otherwise, or
// when interval is not shorter than a fixed [WithProcessingTTL], construction
// records an error wrapping [ErrHeartbeatMisconfigured], reported by
// [Middleware.Err], and the middleware refuses every keyed mutating request
// with 503 rather than run it with a lease nobody renews. Check Err at boot.
// Non-positive values are ignored; an interval below 50ms is raised to 50ms
// silently.
func WithProcessingHeartbeat(interval time.Duration) Option {
	return func(m *Middleware) {
		if interval > 0 {
			m.heartbeatInterval = max(interval, heartbeatMinTick)
		}
	}
}

// Err reports a configuration the middleware cannot honour, detected once at
// construction. A non-nil result means keyed mutating requests are refused;
// composition roots should fail boot on it. A nil Middleware reports nil.
func (m *Middleware) Err() error {
	if m == nil {
		return nil
	}

	return m.configErr
}

// bindHeartbeat resolves the heartbeat's store capability once, at
// construction, so no request ever type-asserts the store.
func (m *Middleware) bindHeartbeat() {
	if m.heartbeatInterval <= 0 {
		return
	}

	extender, ok := m.store.(LeaseExtender)
	if !ok {
		m.configErr = fmt.Errorf("%w: store %T does not implement LeaseExtender", ErrHeartbeatMisconfigured, m.store)

		return
	}

	if m.processingTTL > 0 && m.processingTTL < MinHeartbeatLease {
		m.configErr = fmt.Errorf("%w: processing TTL %s is shorter than the heartbeat minimum lease %s",
			ErrHeartbeatMisconfigured, m.processingTTL, MinHeartbeatLease)

		return
	}

	if m.processingTTL > 0 && m.heartbeatInterval >= m.processingTTL {
		m.configErr = fmt.Errorf("%w: interval %s is not shorter than the processing TTL %s",
			ErrHeartbeatMisconfigured, m.heartbeatInterval, m.processingTTL)

		return
	}

	m.extender = extender
}

// runChainWithHeartbeat runs the handler with the lease renewed behind it. The
// deferred stop ends the heartbeat even when the handler panics, so no beat
// outlives the request.
func (m *Middleware) runChainWithHeartbeat(c fiber.Ctx, key string, processing []byte, lease time.Duration) error {
	if m.extender == nil {
		return m.runChain(c)
	}

	// Read on the request goroutine: the heartbeat goroutine never touches c.
	tenantID := recordTenant(c)

	// Construction checks the interval only against a fixed [WithProcessingTTL];
	// a provider or retention lease is known only here, so the tick follows it.
	tick := heartbeatTick(m.heartbeatInterval, lease)
	if tick < m.heartbeatInterval {
		m.logger.Log(c.Context(), obs.LevelWarn,
			"idempotency: processing heartbeat interval shortened to fit the lease",
			"configured_interval", m.heartbeatInterval,
			"effective_lease", lease,
			"tick", tick,
			"idempotency_key_digest", keyDigest(key),
			"tenant_id", tenantID,
		)
	}

	// Detached like the bookkeeping writes: the hold belongs to the handler,
	// not to the client connection, and is ended by stop alone.
	ctx, cancel := context.WithCancel(context.WithoutCancel(c.Context()))
	done := make(chan struct{})

	runtime.SafeGo(m.logger, "idempotency.processing_heartbeat", runtime.KeepRunning, func() {
		defer close(done)

		ticker := time.NewTicker(tick)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !m.beat(ctx, tenantID, key, processing, lease) {
					return
				}
			}
		}
	})

	defer func() {
		cancel()
		<-done
	}()

	return m.runChain(c)
}

// beat renews the lease once and reports whether the heartbeat should go on.
func (m *Middleware) beat(ctx context.Context, tenantID, key string, processing []byte, lease time.Duration) bool {
	beatCtx, cancel := context.WithTimeout(ctx, m.redisTimeout)
	defer cancel()

	applied, err := m.extender.Extend(beatCtx, key, processing, lease)
	if err != nil {
		if ctx.Err() == nil {
			m.logger.Log(ctx, obs.LevelWarn, "idempotency: processing heartbeat failed; retrying at the next interval",
				"idempotency_key_digest", keyDigest(key),
				"tenant_id", tenantID,
				"error", err,
			)
		}

		return true
	}

	if !applied {
		m.logger.Log(ctx, obs.LevelWarn, "idempotency: processing heartbeat found the lease no longer held; stopping",
			"idempotency_key_digest", keyDigest(key),
			"tenant_id", tenantID,
		)

		return false
	}

	return true
}

// heartbeatMinTick bounds how often one request may beat. It is a tenth of the
// default [WithRedisTimeout], so a beat never queues behind its predecessor on
// a healthy store, and it caps a pathological lease at 20 store calls a second
// per request. A lease that short cannot outlive one slow round trip anyway.
const heartbeatMinTick = 50 * time.Millisecond

// MinHeartbeatLease is the shortest in-flight lease [WithProcessingHeartbeat]
// can hold: three beats at the 50ms tick floor. A shorter lease cannot fit
// three beats, so one delayed renewal lets it lapse under a running handler.
// With the heartbeat on, a fixed [WithProcessingTTL] below it makes
// [Middleware.Err] non-nil, and a request whose resolved lease is below it is
// refused with 503 before acquisition.
const MinHeartbeatLease = 3 * heartbeatMinTick

// heartbeatTick is how often a request beats: the configured interval, or a
// third of the lease actually stored when that is shorter — so two beats may
// fail and the third still lands before the lease lapses — floored at
// heartbeatMinTick.
func heartbeatTick(interval, lease time.Duration) time.Duration {
	return max(min(interval, lease/3), heartbeatMinTick)
}

// refuseUnholdableLease answers a request whose effective lease is too short
// for the heartbeat to hold. It runs before acquisition, so nothing is spent
// and retrying is the correct instruction.
func (m *Middleware) refuseUnholdableLease(c fiber.Ctx, key string, lease time.Duration) error {
	m.logger.Log(c.Context(), obs.LevelError,
		"idempotency: lease shorter than the heartbeat minimum; refusing the request",
		"configured_interval", m.heartbeatInterval,
		"effective_lease", lease,
		"minimum_lease", MinHeartbeatLease,
		"idempotency_key_digest", keyDigest(key),
		"tenant_id", recordTenant(c),
	)

	return m.respondUnavailable(c)
}
