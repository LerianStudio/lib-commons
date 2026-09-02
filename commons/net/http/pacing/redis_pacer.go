package pacing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"

	"github.com/LerianStudio/lib-commons/v6/commons/internal/nilcheck"
	libRedis "github.com/LerianStudio/lib-commons/v6/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	constant "github.com/LerianStudio/lib-observability/v4/constants"
	libTracing "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

// Public tuning defaults.
const (
	// DefaultMaxRate is the highest rate a provider may return unless the caller
	// narrows it with WithMaxRate. It exists so a mistyped runtime knob cannot
	// hand an unbounded rate to an external rail.
	DefaultMaxRate = 1000.0

	// DefaultPollInterval bounds how long one wait lasts before the rate
	// providers are read again, so a rate raised at runtime is honored without a
	// restart.
	DefaultPollInterval = 250 * time.Millisecond
)

const (
	tracerName = "pacing"

	namespaceTenant      = "tenant"
	namespaceInstitution = "inst"

	keyRoot     = "pacing:"
	clockSuffix = "clock"

	// minRate is the slowest representable rate: one call per day. Anything
	// slower is a configuration mistake rather than pacing, and it would push the
	// stored arrival time past any useful key lifetime.
	minRate = 1.0 / 86400.0

	// minRetrySleep keeps a refused evaluation from busy-looping when Redis
	// reports a sub-millisecond wait.
	minRetrySleep = time.Millisecond

	// stateTTLMillis is how long a bucket's last-grant timestamp and the clock
	// high-water mark survive without traffic. It is deliberately flat and equal
	// to the slowest representable interval (one day), not a multiple of the
	// current interval: a rate lowered at runtime must not be able to expire the
	// state that enforces it.
	stateTTLMillis = 24 * 60 * 60 * 1000

	// clockToleranceMicros is how far the backend clock may step backwards before
	// the primitive refuses to issue permits. It is 5 milliseconds: NTP slew never
	// moves backwards, so this only absorbs measurement noise. A real backwards
	// step (a failover onto a lagging node) is larger and is rejected, because a
	// backwards clock re-issues a budget that was already spent.
	clockToleranceMicros = 5000

	// digestBytes is the truncated SHA-256 length used for bucket identities.
	digestBytes = 8

	// markerClockBackwards is the sentinel the Lua script reports when the
	// backend clock moved behind the recorded high-water mark.
	markerClockBackwards = "PACING_CLOCK_BACKWARDS"
)

// Sentinel errors. Every one of them is a refusal: none of them means "proceed".
var (
	// ErrPacerUnavailable reports a missing pacer or Redis connection.
	ErrPacerUnavailable = errors.New("pacing: pacer is unavailable")

	// ErrInvalidPrefix reports an application prefix that is not key-safe.
	ErrInvalidPrefix = errors.New("pacing: prefix is invalid")

	// ErrInvalidIdentity reports a bucket identity that was rejected. The
	// rejected value is never embedded in the message.
	ErrInvalidIdentity = errors.New("pacing: bucket identity is invalid")

	// ErrNoBuckets reports an acquire with nothing to pace. An unpaced outbound
	// call is refused rather than allowed.
	ErrNoBuckets = errors.New("pacing: acquire requires at least one bucket")

	// ErrDuplicateBucket reports the same bucket supplied twice, which would
	// charge one budget twice for one call.
	ErrDuplicateBucket = errors.New("pacing: acquire received the same bucket twice")

	// ErrInvalidRate reports a rate outside the permitted range, or a missing
	// rate provider.
	ErrInvalidRate = errors.New("pacing: rate is outside the permitted range")

	// ErrInvalidPollInterval reports a poll interval below minRetrySleep.
	ErrInvalidPollInterval = errors.New("pacing: poll interval is invalid")

	// ErrRateUnavailable reports a rate provider that failed. The current rate is
	// then unknown, so no permit is issued.
	ErrRateUnavailable = errors.New("pacing: rate provider failed")

	// ErrBackendUnavailable reports a Redis command failure, timeout, or a reply
	// this primitive cannot interpret.
	ErrBackendUnavailable = errors.New("pacing: rate-limit backend is unavailable")

	// ErrClockWentBackwards reports a backend clock behind its own high-water
	// mark, which would silently re-issue an already spent budget.
	ErrClockWentBackwards = errors.New("pacing: rate-limit backend clock went backwards")

	// ErrWaitAborted reports a context that ended before a permit was granted.
	// It always wraps the context error as well.
	ErrWaitAborted = errors.New("pacing: wait aborted before a permit was granted")
)

// pacingScript admits one call per bucket per emission interval, over every
// supplied bucket at once, with a burst of one.
//
// KEYS[1] is the clock high-water mark; KEYS[2..] are the bucket keys.
// ARGV[1] is the permitted backwards clock step in microseconds, ARGV[2] the key
// lifetime in milliseconds, and ARGV[3..] the per-bucket emission intervals in
// microseconds, positionally aligned with KEYS[2..].
//
// A bucket stores the microsecond timestamp of its LAST GRANT, never its next
// admission. The next admission is derived on each evaluation as
// last + the interval supplied on THAT evaluation, so a rate raised at runtime
// shortens the wait immediately. Storing the next admission instead would freeze
// the old rate into the key and make a caller wait out the slow interval before
// the new rate could apply.
//
// The key lifetime is a flat ARGV[2] rather than a multiple of the current
// interval, so a rate LOWERED at runtime cannot expire the state that enforces
// it and hand out one unpaced call.
//
// Reply: {granted, wait_micros}. On a refusal (granted = 0) NO bucket is
// written, so a permit is never burned in one bucket while another blocks. The
// clock high-water mark is written on every evaluation because it records an
// observation of time, not a permit.
//
// Every integer written to Redis goes through string.format('%.0f', …). Redis
// converts a Lua number argument with a shortest-round-trip formatter, so the
// value would survive without this — but the spelling it picks for a 16-digit
// microsecond timestamp is version- and backend-dependent, and may be exponent
// notation. Formatting here pins one plain-integer representation across Redis,
// Valkey and test doubles, and keeps the stored value readable to an operator.
var pacingScript = redis.NewScript(`
local now = redis.call('TIME')
local now_us = tonumber(now[1]) * 1000000 + tonumber(now[2])

local tolerance = tonumber(ARGV[1])
local state_ttl = tonumber(ARGV[2])
local buckets = #KEYS - 1

if tolerance == nil or tolerance < 0 or state_ttl == nil or state_ttl < 1 or buckets < 1 then
  return redis.error_reply('PACING_MALFORMED evaluation arguments')
end

local mark = redis.call('GET', KEYS[1])
local mark_us = tonumber(mark)

if mark and mark_us == nil then
  return redis.error_reply('PACING_MALFORMED clock watermark')
end

if mark_us ~= nil and now_us < mark_us - tolerance then
  return redis.error_reply('PACING_CLOCK_BACKWARDS')
end

local high_water = now_us

if mark_us ~= nil and mark_us > high_water then
  high_water = mark_us
end

redis.call('SET', KEYS[1], string.format('%.0f', high_water), 'PX', state_ttl)

local allow_at = now_us

for i = 1, buckets do
  local interval = tonumber(ARGV[i + 2])

  if interval == nil or interval < 1 then
    return redis.error_reply('PACING_MALFORMED interval')
  end

  local raw = redis.call('GET', KEYS[i + 1])
  local last = tonumber(raw)

  if raw and last == nil then
    return redis.error_reply('PACING_MALFORMED bucket state')
  end

  local earliest = now_us

  if last ~= nil then
    earliest = last + interval
  end

  allow_at = math.max(allow_at, earliest)
end

if allow_at > now_us then
  return {0, allow_at - now_us}
end

local granted_at = string.format('%.0f', now_us)

for i = 1, buckets do
  redis.call('SET', KEYS[i + 1], granted_at, 'PX', state_ttl)
end

return {1, 0}
`)

// RateProvider reports the currently permitted rate for one bucket, in requests
// per second. It is read on every wait, so a rate changed at runtime takes
// effect without a restart.
//
// Zero is a valid answer and means paused: Acquire then blocks until the
// provider reports a positive rate or the context ends. An error is not a rate:
// it fails the acquire closed.
type RateProvider func(ctx context.Context) (float64, error)

// Bucket is one validated pacing identity together with its dynamic rate. Build
// one with TenantBucket or InstitutionBucket; the zero value is rejected.
//
// The identity is digested at construction and the raw value is dropped, so no
// later error, log field, span attribute, or Redis key can carry it.
type Bucket struct {
	namespace string
	digest    string
	rate      RateProvider
}

// TenantBucket builds a tenant-scoped bucket. The identity is canonicalized
// through tenant-manager/core.CanonicalTenantID, so the dashed and dashless
// spellings of one UUID collapse onto one budget. Every identity that
// canonicalization accepts is accepted here, including slugs and "default";
// nothing else is.
func TenantBucket(tenantID string, rate RateProvider) (Bucket, error) {
	canonical, err := tmcore.CanonicalTenantID(tenantID)
	if err != nil {
		// Deliberately not wrapped: CanonicalTenantID embeds the rejected value in
		// its message, and a tenant identity must not travel inside an error that
		// a caller will log.
		return Bucket{}, fmt.Errorf("%w: tenant identity rejected by canonicalization", ErrInvalidIdentity)
	}

	return newBucket(namespaceTenant, canonical, rate)
}

// InstitutionBucket builds an institution-scoped bucket in its own namespace, so
// an institution identity can never collide with a tenant identity.
//
// Institution identities are not canonicalized — they have no UUID form to
// collapse — and are validated only for the identifier grammar that keeps a
// Redis key safe: an ASCII alphanumeric first byte, alphanumeric, '-' or '_'
// after it, and a bounded length. tenant-manager/core.IsValidTenantID is that
// grammar, reused here for its shape and not for any tenant meaning.
func InstitutionBucket(institutionID string, rate RateProvider) (Bucket, error) {
	if !tmcore.IsValidTenantID(institutionID) {
		return Bucket{}, fmt.Errorf("%w: institution identity is not a safe identifier", ErrInvalidIdentity)
	}

	return newBucket(namespaceInstitution, institutionID, rate)
}

func newBucket(namespace, identity string, rate RateProvider) (Bucket, error) {
	if rate == nil {
		return Bucket{}, fmt.Errorf("%w: bucket has no rate provider", ErrInvalidRate)
	}

	return Bucket{namespace: namespace, digest: digestIdentity(identity), rate: rate}, nil
}

// digestIdentity is the only representation of an identity this package stores
// or reports. An operator can recompute it as the first digestBytes bytes of the
// SHA-256 of the canonical identity, hex encoded.
func digestIdentity(identity string) string {
	sum := sha256.Sum256([]byte(identity))

	return hex.EncodeToString(sum[:digestBytes])
}

// Pacer paces outbound calls against budgets shared by every process that points
// at the same Redis. It owns the evaluation script and the retry timing;
// applications supply a constant prefix, bucket identities, and the rate
// providers.
type Pacer struct {
	conn         *libRedis.Client
	prefix       string
	maxRate      float64
	pollInterval time.Duration
	logger       obs.Logger
}

// Option configures a Pacer.
type Option func(*Pacer)

// WithMaxRate sets the highest rate a provider may return. A provider reporting
// more than this fails the acquire closed instead of pacing an external rail at
// a rate nobody agreed to.
func WithMaxRate(maxRate float64) Option {
	return func(p *Pacer) { p.maxRate = maxRate }
}

// WithPollInterval bounds one wait, which is also how often the rate providers
// are re-read while a call is held.
func WithPollInterval(interval time.Duration) Option {
	return func(p *Pacer) { p.pollInterval = interval }
}

// WithLogger attaches a structured logger used to report fail-closed refusals.
func WithLogger(logger obs.Logger) Option {
	return func(p *Pacer) {
		if !nilcheck.Interface(logger) {
			p.logger = logger
		}
	}
}

// NewPacer builds a Pacer for one application prefix. The prefix must be a safe
// identifier; it namespaces every key this pacer touches and doubles as the
// Redis Cluster hash tag that keeps those keys in one slot.
func NewPacer(conn *libRedis.Client, prefix string, opts ...Option) (*Pacer, error) {
	if conn == nil {
		return nil, ErrPacerUnavailable
	}

	if !tmcore.IsValidTenantID(prefix) {
		return nil, fmt.Errorf("%w: prefix must be a safe identifier", ErrInvalidPrefix)
	}

	pacer := &Pacer{
		conn:         conn,
		prefix:       prefix,
		maxRate:      DefaultMaxRate,
		pollInterval: DefaultPollInterval,
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		opt(pacer)
	}

	if pacer.maxRate <= 0 || math.IsNaN(pacer.maxRate) || math.IsInf(pacer.maxRate, 0) {
		return nil, fmt.Errorf("%w: maximum rate must be a positive number", ErrInvalidRate)
	}

	if pacer.maxRate < minRate {
		return nil, fmt.Errorf("%w: maximum rate must be at least one call per day", ErrInvalidRate)
	}

	if pacer.pollInterval < minRetrySleep {
		return nil, fmt.Errorf("%w: poll interval must be at least %s", ErrInvalidPollInterval, minRetrySleep)
	}

	return pacer, nil
}

// Acquire blocks until every supplied bucket admits one outbound call, and
// charges all of them in one atomic evaluation. A refusal charges none of them.
//
// It returns nil only when a permit was issued. Every error is a refusal to
// proceed: a rate that could not be read, a rate outside the permitted range, a
// Redis failure, an uninterpretable reply, a backend clock that moved backwards,
// or a context that ended while waiting. There is no fail-open mode.
//
// A permit issued and then abandoned — because the context ended during the
// caller's own work, for instance — stays spent. That direction is deliberate:
// the shared budget is under-spent, never over-spent.
func (p *Pacer) Acquire(ctx context.Context, buckets ...Bucket) error {
	if p == nil || p.conn == nil {
		return ErrPacerUnavailable
	}

	keys, err := p.keysFor(buckets)
	if err != nil {
		return err
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "pacing.acquire")
	defer span.End()

	span.SetAttributes(
		attribute.String(constant.AttrDBSystem, constant.DBSystemRedis),
		attribute.Int("pacing.bucket_count", len(buckets)),
	)

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return abortedWait(ctxErr)
		}

		intervals, paused, err := p.intervalsFor(ctx, buckets)
		if err != nil {
			p.logWarn(ctx, "pacing refused an outbound call", err)
			libTracing.HandleSpanError(span, "pacing rate evaluation failed", err)

			return err
		}

		if paused {
			if waitErr := p.sleep(ctx, p.pollInterval); waitErr != nil {
				return waitErr
			}

			continue
		}

		granted, retryAfter, err := p.evaluate(ctx, keys, intervals)
		if err != nil {
			// An aborted wait is the caller's own context ending, not a refusal
			// worth an operator's attention, so it stays out of the warning log.
			if !errors.Is(err, ErrWaitAborted) {
				p.logWarn(ctx, "pacing refused an outbound call", err)
			}

			libTracing.HandleSpanError(span, "pacing backend evaluation failed", err)

			return err
		}

		if granted {
			return nil
		}

		if waitErr := p.sleep(ctx, clampRetry(retryAfter, p.pollInterval)); waitErr != nil {
			return waitErr
		}
	}
}

// keysFor validates the buckets and returns the EVAL key list: the clock
// high-water mark first, then one key per bucket in the supplied order.
func (p *Pacer) keysFor(buckets []Bucket) ([]string, error) {
	if len(buckets) == 0 {
		return nil, ErrNoBuckets
	}

	keyspace := p.keyspace()
	keys := make([]string, 0, len(buckets)+1)
	keys = append(keys, keyspace+clockSuffix)
	seen := make(map[string]struct{}, len(buckets))

	for _, bucket := range buckets {
		if bucket.namespace == "" || bucket.digest == "" || bucket.rate == nil {
			return nil, fmt.Errorf("%w: bucket was not built by TenantBucket or InstitutionBucket", ErrInvalidIdentity)
		}

		key := keyspace + bucket.namespace + ":" + bucket.digest
		if _, duplicate := seen[key]; duplicate {
			return nil, ErrDuplicateBucket
		}

		seen[key] = struct{}{}
		keys = append(keys, key)
	}

	return keys, nil
}

// keyspace is the shared prefix of every key this pacer touches. The braces are
// a Redis Cluster hash tag: they force the clock watermark and every bucket of
// one application into one slot, which is what lets a single EVAL span them.
func (p *Pacer) keyspace() string {
	return keyRoot + "{" + p.prefix + "}:"
}

// intervalsFor reads every rate provider and converts the answers to emission
// intervals. It reads all of them even after finding a paused bucket, so an
// out-of-range rate on a later bucket cannot hide behind an earlier zero.
func (p *Pacer) intervalsFor(ctx context.Context, buckets []Bucket) ([]int64, bool, error) {
	intervals := make([]int64, len(buckets))
	paused := false

	for i, bucket := range buckets {
		rate, err := bucket.rate(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrRateUnavailable, err)
		}

		if err := p.validateRate(rate); err != nil {
			return nil, false, err
		}

		if rate == 0 {
			paused = true

			continue
		}

		intervals[i] = intervalMicros(rate)
	}

	return intervals, paused, nil
}

func (p *Pacer) validateRate(rate float64) error {
	switch {
	case math.IsNaN(rate), math.IsInf(rate, 0), rate < 0:
		return fmt.Errorf("%w: rate is not a usable number", ErrInvalidRate)
	case rate == 0:
		return nil
	case rate > p.maxRate:
		return fmt.Errorf("%w: rate exceeds the configured maximum", ErrInvalidRate)
	case rate < minRate:
		return fmt.Errorf("%w: rate is slower than one call per day", ErrInvalidRate)
	default:
		return nil
	}
}

// intervalMicros is the minimum spacing between two calls at the given rate,
// rounded up so the emitted rate is never faster than the permitted one.
//
// The caller has already accepted the rate through validateRate, so it is finite
// and positive; math.Ceil of a positive quotient is therefore at least 1 and
// needs no floor of its own.
func intervalMicros(rate float64) int64 {
	return int64(math.Ceil(1_000_000 / rate))
}

// evaluate runs one atomic evaluation. It reports whether a permit was issued
// and, when it was not, how long the earliest grant is away.
func (p *Pacer) evaluate(ctx context.Context, keys []string, intervals []int64) (bool, time.Duration, error) {
	client, err := p.conn.GetClient(ctx)
	if err != nil {
		return false, 0, classifyBackendError(ctx, err)
	}

	args := make([]any, 0, len(intervals)+2)
	args = append(args, clockToleranceMicros, stateTTLMillis)

	for _, interval := range intervals {
		args = append(args, interval)
	}

	result, err := pacingScript.Run(ctx, client, keys, args...).Slice()
	if err != nil {
		return false, 0, classifyBackendError(ctx, err)
	}

	return parseEvaluation(result)
}

// classifyBackendError separates a context that ended, a backwards backend
// clock, and every other backend failure. All three refuse the call; only the
// message differs. A context end is the caller's own doing and is reported as
// an aborted wait rather than a false backend-health signal, and the clock
// message is the one signal an operator needs to go look at the Redis node's
// clock.
func classifyBackendError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return abortedWait(ctxErr)
	}

	if strings.Contains(err.Error(), markerClockBackwards) {
		return fmt.Errorf("%w: %w", ErrClockWentBackwards, err)
	}

	return fmt.Errorf("%w: %w", ErrBackendUnavailable, err)
}

func parseEvaluation(result []any) (bool, time.Duration, error) {
	if len(result) != 2 {
		return false, 0, fmt.Errorf("%w: script returned %d values", ErrBackendUnavailable, len(result))
	}

	granted, ok := result[0].(int64)
	if !ok {
		return false, 0, fmt.Errorf("%w: script returned %T for the grant flag", ErrBackendUnavailable, result[0])
	}

	waitMicros, ok := result[1].(int64)
	if !ok {
		return false, 0, fmt.Errorf("%w: script returned %T for the wait", ErrBackendUnavailable, result[1])
	}

	if granted == 1 {
		return true, 0, nil
	}

	if granted != 0 || waitMicros <= 0 {
		return false, 0, fmt.Errorf("%w: script refused without a usable wait", ErrBackendUnavailable)
	}

	return false, time.Duration(waitMicros) * time.Microsecond, nil
}

// sleep waits for d, or returns as soon as the context ends. Both call sites
// pass at least minRetrySleep, so there is deliberately no zero-duration
// shortcut: one that returned nil would let a dead context proceed.
func (p *Pacer) sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return abortedWait(ctx.Err())
	case <-timer.C:
		return nil
	}
}

func (p *Pacer) logWarn(ctx context.Context, msg string, err error) {
	if nilcheck.Interface(p.logger) {
		return
	}

	p.logger.Log(ctx, obs.LevelWarn, msg,
		"error", err,
		"pacing_prefix", p.prefix,
	)
}

// clampRetry caps a wait at the poll interval so a rate raised at runtime is
// picked up, and floors it so a sub-millisecond wait does not busy-loop.
func clampRetry(retryAfter, poll time.Duration) time.Duration {
	return max(min(retryAfter, poll), minRetrySleep)
}

func abortedWait(err error) error {
	return fmt.Errorf("%w: %w", ErrWaitAborted, err)
}
