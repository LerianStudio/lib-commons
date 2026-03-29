package dlq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	libBackoff "github.com/LerianStudio/lib-commons/v4/commons/backoff"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libOtel "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	libRedis "github.com/LerianStudio/lib-commons/v4/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// DLQMetrics records DLQ-specific counters. Implementations are optional;
// when nil, metric calls are silently skipped.
type DLQMetrics interface {
	RecordRetried(ctx context.Context, source string)
	RecordExhausted(ctx context.Context, source string)
}

// FailedMessage represents a message that failed processing and was routed to
// the dead letter queue for later retry.
type FailedMessage struct {
	Source       string `json:"source"`
	OriginalData []byte `json:"original_data"`
	ErrorMessage string `json:"error_message"`
	RetryCount   int    `json:"retry_count"`
	// MaxRetries is the maximum number of retry attempts. A value of 0 is treated
	// as "use handler default" and will be overwritten during Enqueue. To allow
	// zero retries (immediate discard on first failure), set MaxRetries to the
	// handler's configured value and RetryCount to that same value.
	MaxRetries  int       `json:"max_retries"`
	CreatedAt   time.Time `json:"created_at"`
	NextRetryAt time.Time `json:"next_retry_at,omitempty"`
	TenantID    string    `json:"tenant_id,omitempty"`
}

// Handler manages dead letter queue operations backed by Redis lists.
type Handler struct {
	conn       *libRedis.Client
	keyPrefix  string
	maxRetries int
	logger     libLog.Logger
	tracer     trace.Tracer
	metrics    DLQMetrics
	module     string
}

// Option configures a Handler at construction time.
type Option func(*Handler)

// WithLogger sets the logger used by the Handler.
func WithLogger(l libLog.Logger) Option {
	return func(h *Handler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithTracer sets the OpenTelemetry tracer used by the Handler.
func WithTracer(t trace.Tracer) Option {
	return func(h *Handler) {
		if t != nil {
			h.tracer = t
		}
	}
}

// WithMetrics sets the metrics recorder used by the Handler.
func WithMetrics(m DLQMetrics) Option {
	return func(h *Handler) {
		h.metrics = m
	}
}

// WithModule sets a module label used in log and metric context.
func WithModule(module string) Option {
	return func(h *Handler) {
		if module != "" {
			h.module = module
		}
	}
}

// New creates a Handler backed by the given Redis client. keyPrefix is prepended
// to all Redis keys (e.g. "dlq:"). maxRetries controls how many times a message
// may be retried before it is considered exhausted.
// Returns nil when conn is nil — all exported Handler methods already guard
// against a nil receiver and return ErrNilHandler, so callers are safe.
func New(conn *libRedis.Client, keyPrefix string, maxRetries int, opts ...Option) *Handler {
	if conn == nil {
		return nil
	}

	if maxRetries <= 0 {
		maxRetries = 3
	}

	if keyPrefix == "" {
		keyPrefix = "dlq:"
	}

	h := &Handler{
		conn:       conn,
		keyPrefix:  keyPrefix,
		maxRetries: maxRetries,
		logger:     libLog.NewNop(),
		tracer:     noop.NewTracerProvider().Tracer("dlq.noop"),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}

	return h
}

// backoffDuration calculates exponential backoff with jitter for retry timing.
// Base delay of 30s matches br-spb's original formula. Uses AWS Full Jitter
// strategy via lib-commons/backoff for better cluster behavior.
// The floor is 5s (not 30s) so that attempt 0 gets genuine jitter spread
// over [5s, 30s) rather than always resolving to exactly 30s.
func backoffDuration(retryCount int) time.Duration {
	const minBackoff = 5 * time.Second

	d := libBackoff.ExponentialWithJitter(30*time.Second, retryCount)
	if d < minBackoff {
		d = minBackoff
	}

	return d
}

// Enqueue adds a failed message to the DLQ. The message's TenantID is
// resolved from the context when not already set on the message itself.
// If msg.MaxRetries is 0 on the initial enqueue (CreatedAt is zero), it is
// overwritten with the handler's configured maxRetries value. See the
// MaxRetries field doc for how to express a "zero retries allowed" policy.
func (h *Handler) Enqueue(ctx context.Context, msg *FailedMessage) error {
	if h == nil {
		return ErrNilHandler
	}

	if msg == nil {
		return errors.New("dlq: enqueue: nil message")
	}

	if msg.Source == "" {
		return errors.New("dlq: enqueue: source must not be empty")
	}

	if err := validateKeySegment("source", msg.Source); err != nil {
		return err
	}

	ctx, span := h.tracer.Start(ctx, "dlq.enqueue")
	defer span.End()

	// Only stamp CreatedAt and MaxRetries on initial enqueue (zero-valued).
	// Re-enqueue paths (consumer retry-failed, not-yet-ready, prune) pass
	// messages that already carry the original values; overwriting them would
	// permanently lose the original failure timestamp and retry budget.
	initialEnqueue := msg.CreatedAt.IsZero()
	if initialEnqueue {
		msg.CreatedAt = time.Now().UTC()
	}

	if msg.MaxRetries == 0 {
		msg.MaxRetries = h.maxRetries
	}

	ctxTenant := tmcore.GetTenantIDContext(ctx)

	effectiveTenant := msg.TenantID
	if effectiveTenant == "" {
		effectiveTenant = ctxTenant
		msg.TenantID = effectiveTenant
	}

	if effectiveTenant != "" && ctxTenant != "" && effectiveTenant != ctxTenant {
		return fmt.Errorf("dlq: enqueue: tenant mismatch between message (%s) and context (%s)", effectiveTenant, ctxTenant)
	}

	// Recalculate NextRetryAt only on initial enqueue. On re-enqueue the
	// consumer has already incremented RetryCount and the caller is
	// responsible for timing; we preserve their NextRetryAt or let the
	// backoff be recalculated by the consumer path that sets RetryCount.
	if initialEnqueue && msg.RetryCount < msg.MaxRetries {
		msg.NextRetryAt = msg.CreatedAt.Add(backoffDuration(msg.RetryCount))
	}

	data, err := json.Marshal(msg)
	if err != nil {
		libOtel.HandleSpanError(span, "dlq marshal failed", err)

		return fmt.Errorf("dlq: enqueue: marshal: %w", err)
	}

	key := h.tenantScopedKeyForTenant(effectiveTenant, msg.Source)

	rds, err := h.conn.GetClient(ctx)
	if err != nil {
		libOtel.HandleSpanError(span, "dlq redis client unavailable", err)
		h.logEnqueueFallback(ctx, key, msg, err)

		return fmt.Errorf("dlq: enqueue: redis client: %w", err)
	}

	if pushErr := rds.RPush(ctx, key, data).Err(); pushErr != nil {
		libOtel.HandleSpanError(span, "dlq rpush failed", pushErr)
		h.logEnqueueFallback(ctx, key, msg, pushErr)

		return fmt.Errorf("dlq: enqueue: rpush: %w", pushErr)
	}

	return nil
}

// logEnqueueFallback logs message metadata when Redis is unreachable. The
// payload is redacted to prevent PII leakage into log aggregators.
func (h *Handler) logEnqueueFallback(ctx context.Context, key string, msg *FailedMessage, err error) {
	h.logger.Log(ctx, libLog.LevelError,
		"dlq: failed to enqueue message to Redis — payload redacted for PII safety",
		libLog.String("dlq_key", key),
		libLog.String("msg_source", msg.Source),
		libLog.Int("retry_count", msg.RetryCount),
		libLog.String("original_error", truncateString(msg.ErrorMessage, 200)),
		libLog.Err(err),
	)
}

// Dequeue atomically removes and returns the next message from the given source queue.
// NOTE: This uses LPop which is destructive. If the process crashes between Dequeue
// and a subsequent re-enqueue, the message is permanently lost. This provides
// at-most-once delivery semantics. For at-least-once, consider using LMOVE (Redis 6.2+).
func (h *Handler) Dequeue(ctx context.Context, source string) (*FailedMessage, error) {
	if h == nil {
		return nil, ErrNilHandler
	}

	ctx, span := h.tracer.Start(ctx, "dlq.dequeue")
	defer span.End()

	key := h.tenantScopedKey(ctx, source)

	rds, err := h.conn.GetClient(ctx)
	if err != nil {
		libOtel.HandleSpanError(span, "dlq redis client unavailable", err)

		return nil, fmt.Errorf("dlq: dequeue: redis client: %w", err)
	}

	data, err := rds.LPop(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("dlq: dequeue: %w", err)
	}

	var msg FailedMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		libOtel.HandleSpanError(span, "dlq unmarshal failed", err)

		return nil, fmt.Errorf("dlq: dequeue: unmarshal: %w", err)
	}

	return &msg, nil
}

// QueueLength returns the number of messages in the DLQ for the given source.
func (h *Handler) QueueLength(ctx context.Context, source string) (int64, error) {
	if h == nil {
		return 0, ErrNilHandler
	}

	key := h.tenantScopedKey(ctx, source)

	rds, err := h.conn.GetClient(ctx)
	if err != nil {
		return 0, fmt.Errorf("dlq: queue length: redis client: %w", err)
	}

	return rds.LLen(ctx, key).Result()
}

// ScanQueues discovers all tenant-scoped Redis keys matching the pattern
// "{keyPrefix}*:{source}". This enables a background consumer (running without
// tenant context) to find keys like "dlq:tenant-A:outbound".
//
// The SCAN command is used instead of KEYS to avoid blocking Redis on large
// keyspaces. Returns full Redis keys; the caller can use ExtractTenantFromKey
// to recover the tenant ID.
func (h *Handler) ScanQueues(ctx context.Context, source string) ([]string, error) {
	if h == nil {
		return nil, ErrNilHandler
	}

	ctx, span := h.tracer.Start(ctx, "dlq.scan_queues")
	defer span.End()

	pattern := fmt.Sprintf("%s*:%s", h.keyPrefix, source)
	globalKey := fmt.Sprintf("%s%s", h.keyPrefix, source)

	rds, err := h.conn.GetClient(ctx)
	if err != nil {
		libOtel.HandleSpanError(span, "dlq redis client unavailable", err)

		return nil, fmt.Errorf("dlq: scan queues: redis client: %w", err)
	}

	var keys []string

	var cursor uint64

	for {
		var batch []string

		var scanErr error

		batch, cursor, scanErr = rds.Scan(ctx, cursor, pattern, 100).Result()
		if scanErr != nil {
			libOtel.HandleSpanError(span, "dlq scan failed", scanErr)

			return nil, fmt.Errorf("dlq: scan queues: %w", scanErr)
		}

		for _, key := range batch {
			if key != globalKey {
				keys = append(keys, key)
			}
		}

		if cursor == 0 {
			break
		}
	}

	return keys, nil
}

// PruneExhaustedMessages removes up to limit messages from the DLQ source that
// have exceeded their maximum retry count. Returns the number of messages pruned.
//
// NOTE: This uses LPop (via Dequeue) which is destructive. If the process crashes
// between Dequeue and a subsequent re-enqueue of non-exhausted messages, those
// messages are permanently lost. This provides at-most-once delivery semantics.
// For at-least-once, consider using LMOVE (Redis 6.2+).
//
// Note: surviving messages are re-enqueued at the back of the queue. FIFO
// ordering relative to other messages in the same source is not preserved.
// This is acceptable for a dead letter queue — messages routed here are already
// out of their original processing order by definition — but callers that
// depend on strict ordering should be aware of this behavior.
func (h *Handler) PruneExhaustedMessages(ctx context.Context, source string, limit int) (int, error) {
	if h == nil {
		return 0, ErrNilHandler
	}

	if limit <= 0 {
		return 0, nil
	}

	ctx, span := h.tracer.Start(ctx, "dlq.prune_exhausted")
	defer span.End()

	pruned := 0

	for range limit {
		msg, err := h.Dequeue(ctx, source)
		if err != nil {
			// Empty queue or Redis error — stop pruning.
			break
		}

		if msg.RetryCount >= msg.MaxRetries {
			pruned++

			h.logger.Log(ctx, libLog.LevelWarn, "dlq: pruned exhausted message",
				libLog.String("source", msg.Source),
				libLog.Int("retry_count", msg.RetryCount),
				libLog.String("tenant_id", msg.TenantID),
			)

			continue
		}

		// Not exhausted — put it back.
		if err := h.Enqueue(ctx, msg); err != nil {
			h.logger.Log(ctx, libLog.LevelError, "dlq: failed to re-enqueue non-exhausted message during prune",
				libLog.String("source", msg.Source),
				libLog.Err(err),
			)
		}
	}

	return pruned, nil
}

// ExtractTenantFromKey extracts the tenant ID from a tenant-scoped Redis key.
// Given key="dlq:tenant-abc:outbound" and keyPrefix="dlq:", returns "tenant-abc".
// Returns empty string if the key does not match the expected format.
func (h *Handler) ExtractTenantFromKey(key, source string) string {
	if h == nil {
		return ""
	}

	prefix := h.keyPrefix
	suffix := ":" + source

	if len(key) <= len(prefix)+len(suffix) {
		return ""
	}

	if key[:len(prefix)] != prefix {
		return ""
	}

	if key[len(key)-len(suffix):] != suffix {
		return ""
	}

	return key[len(prefix) : len(key)-len(suffix)]
}

// tenantScopedKey constructs a Redis key including the tenant ID from context.
// With tenant:    "dlq:tenant-abc:outbound"
// Without tenant: "dlq:outbound"
func (h *Handler) tenantScopedKey(ctx context.Context, source string) string {
	return h.tenantScopedKeyForTenant(tmcore.GetTenantIDContext(ctx), source)
}

func (h *Handler) tenantScopedKeyForTenant(tenantID, source string) string {
	if tenantID != "" {
		if err := validateKeySegment("tenantID", tenantID); err != nil {
			// Log the invalid segment and fall back to the non-tenant key
			// rather than constructing a corrupted Redis key.
			h.logger.Log(context.Background(), libLog.LevelWarn, "dlq: tenantScopedKeyForTenant: invalid tenantID, using global key",
				libLog.String("tenant_id", tenantID),
				libLog.Err(err),
			)

			return fmt.Sprintf("%s%s", h.keyPrefix, source)
		}

		return fmt.Sprintf("%s%s:%s", h.keyPrefix, tenantID, source)
	}

	return fmt.Sprintf("%s%s", h.keyPrefix, source)
}

// validateKeySegment ensures that a Redis key segment (source or tenantID)
// does not contain characters that would corrupt key patterns or enable
// injection into SCAN glob patterns.
func validateKeySegment(name, value string) error {
	for _, c := range value {
		if c == ':' || c == '*' || c == '?' || c == '[' || c == ']' {
			return fmt.Errorf("dlq: %s %q contains disallowed character %q", name, value, c)
		}
	}

	return nil
}

// truncateString returns s unchanged when its rune count is within maxLen,
// otherwise it truncates at the maxLen-th rune boundary and appends "..."
// to signal that content was trimmed. Rune-aware truncation prevents
// splitting multi-byte UTF-8 sequences, which would produce invalid output.
// Used by logEnqueueFallback to prevent large PII-containing error messages
// from leaking into log aggregators.
func truncateString(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}

	// Walk runes and cut at the correct byte offset.
	byteLen := 0
	count := 0

	for _, r := range s {
		if count == maxLen {
			break
		}

		byteLen += utf8.RuneLen(r)
		count++
	}

	return s[:byteLen] + "..."
}
