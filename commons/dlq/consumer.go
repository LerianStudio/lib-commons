package dlq

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	libOtel "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	libRuntime "github.com/LerianStudio/lib-commons/v5/commons/runtime"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// RetryFunc is invoked by the Consumer for each retryable message. Return nil
// on success (message is discarded), or an error to re-enqueue with incremented
// retry count and updated backoff.
type RetryFunc func(ctx context.Context, msg *FailedMessage) error

// ConsumerConfig holds tuning knobs for the background consumer.
type ConsumerConfig struct {
	// PollInterval is how often the consumer checks for retryable messages.
	// Default: 30s.
	PollInterval time.Duration
	// BatchSize is the max messages processed per source per poll cycle.
	// Default: 10.
	BatchSize int
	// Sources lists the DLQ source queue names to consume (e.g. "outbound", "inbound").
	Sources []string
}

// Consumer polls DLQ queues and retries messages via a caller-provided RetryFunc.
// Messages that succeed are discarded; messages that fail again are re-enqueued
// with incremented retry count and exponential backoff. Messages that exceed
// MaxRetries are logged as permanently failed and discarded.
type Consumer struct {
	handler   *Handler
	retryFunc RetryFunc
	logger    libLog.Logger
	tracer    trace.Tracer
	metrics   DLQMetrics
	module    string
	cfg       ConsumerConfig

	stopMu sync.Mutex
	stopCh chan struct{}
}

// ConsumerOption configures a Consumer at construction time.
type ConsumerOption func(*Consumer)

// WithConsumerLogger sets the logger used by the Consumer.
func WithConsumerLogger(l libLog.Logger) ConsumerOption {
	return func(c *Consumer) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithConsumerTracer sets the OpenTelemetry tracer used by the Consumer.
func WithConsumerTracer(t trace.Tracer) ConsumerOption {
	return func(c *Consumer) {
		if t != nil {
			c.tracer = t
		}
	}
}

// WithConsumerMetrics sets the metrics recorder used by the Consumer.
func WithConsumerMetrics(m DLQMetrics) ConsumerOption {
	return func(c *Consumer) {
		c.metrics = m
	}
}

// WithConsumerModule sets a module label for log and metric context.
func WithConsumerModule(module string) ConsumerOption {
	return func(c *Consumer) {
		if module != "" {
			c.module = module
		}
	}
}

// WithPollInterval sets the consumer poll interval.
func WithPollInterval(d time.Duration) ConsumerOption {
	return func(c *Consumer) {
		if d > 0 {
			c.cfg.PollInterval = d
		}
	}
}

// WithBatchSize sets the maximum messages processed per source per poll.
func WithBatchSize(n int) ConsumerOption {
	return func(c *Consumer) {
		if n > 0 {
			c.cfg.BatchSize = n
		}
	}
}

// WithSources sets the DLQ source queue names to consume. The input slice is
// cloned so that subsequent mutations by the caller do not race with the
// consumer's read loop.
func WithSources(sources ...string) ConsumerOption {
	return func(c *Consumer) {
		if len(sources) > 0 {
			cloned := make([]string, len(sources))
			copy(cloned, sources)
			c.cfg.Sources = cloned
		}
	}
}

// NewConsumer creates a DLQ consumer. handler and retryFn are required; returns
// an error if either is nil.
func NewConsumer(handler *Handler, retryFn RetryFunc, opts ...ConsumerOption) (*Consumer, error) {
	if handler == nil {
		return nil, ErrNilHandler
	}

	if retryFn == nil {
		return nil, ErrNilRetryFunc
	}

	c := &Consumer{
		handler:   handler,
		retryFunc: retryFn,
		logger:    libLog.NewNop(),
		tracer:    noop.NewTracerProvider().Tracer("dlq.consumer.noop"),
		cfg: ConsumerConfig{
			PollInterval: 30 * time.Second,
			BatchSize:    10,
		},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	// Inherit handler settings when not overridden via options.
	// Default to nopDLQMetrics to eliminate nil checks at every call site.
	if c.metrics == nil {
		if handler.metrics != nil {
			c.metrics = handler.metrics
		} else {
			c.metrics = nopDLQMetrics{}
		}
	}

	if c.module == "" {
		c.module = handler.module
	}

	return c, nil
}

// Run starts the consumer loop, polling on the configured interval until ctx is
// cancelled or Stop is called. Run blocks until shutdown.
func (c *Consumer) Run(ctx context.Context) {
	if c == nil {
		return
	}

	defer libRuntime.RecoverWithPolicyAndContext(ctx, c.logger, c.module, "dlq-consumer-loop", libRuntime.KeepRunning)

	c.stopMu.Lock()
	if c.stopCh != nil {
		// Already running or previous goroutine still draining — reject to
		// prevent overlapping Run loops. The deferred cleanup in the active
		// goroutine will nil c.stopCh once it fully exits.
		c.stopMu.Unlock()

		c.logger.Log(ctx, libLog.LevelWarn, "dlq consumer: Run() called while already running, ignoring")

		return
	}

	runStopCh := make(chan struct{})
	c.stopCh = runStopCh
	c.stopMu.Unlock()

	defer func() {
		c.stopMu.Lock()
		if c.stopCh == runStopCh {
			c.stopCh = nil
		}
		c.stopMu.Unlock()
	}()

	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()

	c.logger.Log(ctx, libLog.LevelInfo, "dlq consumer started",
		libLog.String("sources", fmt.Sprintf("%v", c.cfg.Sources)),
		libLog.String("interval", c.cfg.PollInterval.String()),
		libLog.Int("batch_size", c.cfg.BatchSize),
	)

	for {
		select {
		case <-ctx.Done():
			c.logger.Log(ctx, libLog.LevelInfo, "dlq consumer stopped")

			return
		case <-runStopCh:
			c.logger.Log(ctx, libLog.LevelInfo, "dlq consumer stopped")

			return
		case <-ticker.C:
			c.safeProcessOnce(ctx)
		}
	}
}

// Stop signals the consumer loop to exit. Safe to call multiple times.
func (c *Consumer) Stop() {
	if c == nil {
		return
	}

	c.stopMu.Lock()
	defer c.stopMu.Unlock()

	if c.stopCh != nil {
		select {
		case <-c.stopCh:
			// Already closed.
		default:
			close(c.stopCh)
		}
	}
}

// ProcessOnce executes a single poll cycle across all configured sources.
// Exported for testing; in production, use Run.
func (c *Consumer) ProcessOnce(ctx context.Context) {
	if c == nil {
		return
	}

	c.processOnce(ctx)
}

// safeProcessOnce wraps processOnce with panic recovery.
func (c *Consumer) safeProcessOnce(ctx context.Context) {
	defer libRuntime.RecoverWithPolicyAndContext(ctx, c.logger, c.module, "dlq-poll-cycle", libRuntime.KeepRunning)

	c.processOnce(ctx)
}

// processOnce iterates over each configured source and processes up to BatchSize
// messages per source.
func (c *Consumer) processOnce(ctx context.Context) {
	for _, source := range c.cfg.Sources {
		c.processSource(ctx, source)
	}
}

// processSource handles a single DLQ source: discovers tenant-scoped keys via
// Redis SCAN, then round-robin drains messages from each key.
func (c *Consumer) processSource(ctx context.Context, source string) {
	ctx, span := c.tracer.Start(ctx, "dlq.consumer.process_source")
	defer span.End()

	// Discover tenant-scoped keys (e.g. "dlq:tenant-A:outbound").
	tenantKeys, err := c.handler.ScanQueues(ctx, source)
	if err != nil {
		c.logger.Log(ctx, libLog.LevelWarn, "dlq consumer: tenant key scan failed",
			libLog.String("source", source),
			libLog.Err(err),
		)

		return
	}

	// Build a context per discovered tenant. Include the bare (non-tenant) context
	// only when no tenant keys were found — if tenant keys exist, draining the
	// global (non-tenant) key too would double-process the same logical queue.
	var keyContexts []context.Context

	for _, key := range tenantKeys {
		tenantID := c.handler.ExtractTenantFromKey(key, source)
		if tenantID == "" {
			continue
		}

		tenantCtx := tmcore.ContextWithTenantID(ctx, tenantID)
		keyContexts = append(keyContexts, tenantCtx)
	}

	if len(keyContexts) == 0 {
		keyContexts = append(keyContexts, ctx)
	}

	// Round-robin drain across all discovered keys up to BatchSize total.
	processed := 0
	for processed < c.cfg.BatchSize {
		progressed := false

		for _, keyCtx := range keyContexts {
			if processed >= c.cfg.BatchSize {
				break
			}

			if keyCtx.Err() != nil {
				return
			}

			if c.drainSource(keyCtx, source, 1) > 0 {
				processed++
				progressed = true
			}
		}

		if !progressed {
			return
		}
	}
}

// drainSource dequeues up to limit messages from a single source key and
// processes each one. Returns the count of messages processed.
//
// When a future-dated message is encountered, it is rotated to the tail of the
// queue and the next message is dequeued immediately. This head-of-line
// rotation ensures that ready messages behind future-dated ones are processed
// within the same poll cycle. The number of consecutive rotations is bounded
// by the queue length to prevent infinite loops when all messages are
// future-dated.
func (c *Consumer) drainSource(ctx context.Context, source string, limit int) int {
	processed := 0

	for range limit {
		select {
		case <-ctx.Done():
			return processed
		default:
		}

		// Query current queue length to bound rotations. This prevents an
		// infinite loop when every message in the queue is future-dated.
		qLen, err := c.handler.QueueLength(ctx, source)
		if err != nil {
			c.logger.Log(ctx, libLog.LevelWarn, "dlq consumer: queue length check failed",
				libLog.String("source", source),
				libLog.Err(err),
			)

			return processed
		}

		if qLen == 0 {
			return processed
		}

		// Cap rotation work per key to batchSize so one tenant with a large
		// backlog of future-dated messages cannot monopolise a poll cycle.
		rotationCap := int(qLen)
		if rotationCap > c.cfg.BatchSize {
			rotationCap = c.cfg.BatchSize
		}

		rotations := 0

		for rotations < rotationCap {
			select {
			case <-ctx.Done():
				return processed
			default:
			}

			msg, deqErr := c.handler.Dequeue(ctx, source)
			if deqErr != nil {
				if isRedisNilError(deqErr) {
					return processed
				}

				c.logger.Log(ctx, libLog.LevelWarn, "dlq consumer: dequeue failed",
					libLog.String("source", source),
					libLog.Err(deqErr),
				)

				return processed
			}

			if msg == nil {
				return processed
			}

			if c.processMessage(ctx, msg) {
				processed++

				break
			}

			// processMessage returned false — the message was not yet ready
			// and was re-enqueued at the tail. Continue to the next message.
			rotations++
		}
	}

	return processed
}

// processMessage handles a single dequeued message: validates timing, attempts
// retry, and decides whether to discard or re-enqueue. Returns true when actual
// work was performed (retry attempted or message exhausted), false when the
// message was merely bounced back because it is not yet ready for retry.
func (c *Consumer) processMessage(ctx context.Context, msg *FailedMessage) bool {
	ctx, span := c.tracer.Start(ctx, "dlq.consumer.process_message")
	defer span.End()

	// Restore tenant context from the persisted TenantID. Prefer the message's
	// tenant over the queue context to prevent cross-tenant retries after
	// dequeuing from a legacy or corrupted key.
	if msg.TenantID != "" {
		ctxTenant := tmcore.GetTenantIDContext(ctx)
		if ctxTenant != msg.TenantID {
			if ctxTenant != "" {
				c.logger.Log(ctx, libLog.LevelWarn, "dlq consumer: tenant mismatch, restoring message tenant",
					libLog.String("queue_tenant", ctxTenant),
					libLog.String("message_tenant", msg.TenantID),
					libLog.String("source", msg.Source),
				)
			}

			ctx = tmcore.ContextWithTenantID(ctx, msg.TenantID)
		}
	}

	now := time.Now().UTC()

	// Not yet time to retry — re-enqueue at the back so other messages proceed.
	if !msg.NextRetryAt.IsZero() && now.Before(msg.NextRetryAt) {
		if err := c.handler.Enqueue(ctx, msg); err != nil {
			libOtel.HandleSpanError(span, "dlq message lost on re-enqueue", err)

			c.logger.Log(ctx, libLog.LevelError, "dlq consumer: message lost — failed to re-enqueue not-yet-ready message",
				libLog.String("source", msg.Source),
				libLog.Int("retry_count", msg.RetryCount),
				libLog.Err(err),
			)

			metricSource := c.sanitizeMetricSource(msg.Source)
			c.metrics.RecordLost(ctx, metricSource)

			return true
		}

		return false
	}

	// Message exhausted — permanently failed, discard.
	if msg.RetryCount >= msg.MaxRetries {
		libOtel.HandleSpanError(span, "dlq message exhausted", ErrMessageExhausted)

		metricSource := c.sanitizeMetricSource(msg.Source)
		c.metrics.RecordExhausted(ctx, metricSource)

		c.logger.Log(ctx, libLog.LevelError, "dlq consumer: message permanently failed, discarding",
			libLog.String("source", msg.Source),
			libLog.Int("retry_count", msg.RetryCount),
			libLog.Int("max_retries", msg.MaxRetries),
			libLog.String("last_error", truncateString(msg.ErrorMessage, 200)),
		)

		return true
	}

	// Attempt retry with panic recovery to prevent message loss.
	retryErr := c.safeRetryFunc(ctx, msg)
	if retryErr != nil {
		// Retry failed — increment count, recalculate backoff, re-enqueue.
		msg.RetryCount++
		msg.ErrorMessage = retryErr.Error()
		msg.NextRetryAt = time.Now().UTC().Add(backoffDuration(msg.RetryCount))

		if requeueErr := c.handler.Enqueue(ctx, msg); requeueErr != nil {
			libOtel.HandleSpanError(span, "dlq message lost on re-enqueue", requeueErr)

			c.logger.Log(ctx, libLog.LevelError, "dlq consumer: message lost — failed to re-enqueue after retry failure",
				libLog.String("source", msg.Source),
				libLog.Int("retry_count", msg.RetryCount),
				libLog.Err(requeueErr),
			)

			metricSource := c.sanitizeMetricSource(msg.Source)
			c.metrics.RecordLost(ctx, metricSource)

			return true
		}

		c.logger.Log(ctx, libLog.LevelWarn, "dlq consumer: retry failed, re-enqueued",
			libLog.String("source", msg.Source),
			libLog.Int("retry_count", msg.RetryCount),
			libLog.Err(retryErr),
		)

		return true
	}

	// Retry succeeded — record metric and discard.
	metricSource := c.sanitizeMetricSource(msg.Source)
	c.metrics.RecordRetried(ctx, metricSource)

	c.logger.Log(ctx, libLog.LevelInfo, "dlq consumer: message retry succeeded",
		libLog.String("source", msg.Source),
		libLog.Int("retry_count", msg.RetryCount),
	)

	return true
}

// safeRetryFunc wraps the caller-provided retryFunc with panic recovery. If
// retryFunc panics, the panic is converted to an error so the caller can
// re-enqueue the message instead of losing it silently.
func (c *Consumer) safeRetryFunc(ctx context.Context, msg *FailedMessage) (retryErr error) {
	defer func() {
		if r := recover(); r != nil {
			retryErr = fmt.Errorf("dlq consumer: retryFunc panicked: %v", r)

			c.logger.Log(ctx, libLog.LevelError, "dlq consumer: retryFunc panic recovered",
				libLog.String("source", msg.Source),
				libLog.String("panic", fmt.Sprintf("%v", r)),
			)
		}
	}()

	return c.retryFunc(ctx, msg)
}

// sanitizeMetricSource returns the source label to use for metric recording.
// It validates the source against the configured Sources list. If the message
// source is not in the configured list (e.g. a corrupted or injected value),
// "unknown" is returned to prevent high-cardinality metric label pollution.
func (c *Consumer) sanitizeMetricSource(source string) string {
	if slices.Contains(c.cfg.Sources, source) {
		return source
	}

	return "unknown"
}
