package consumer

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"maps"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// retryStateEntry holds per-tenant retry state for connection failure resilience.
type retryStateEntry struct {
	mu         sync.Mutex
	retryCount int
	degraded   bool
}

// reset clears retry counters and degraded flag. Must be called with no other goroutine
// holding the entry's mutex (e.g. after Load from sync.Map).
func (e *retryStateEntry) reset() {
	e.mu.Lock()
	e.retryCount = 0
	e.degraded = false
	e.mu.Unlock()
}

// isDegraded returns whether the tenant is marked degraded.
func (e *retryStateEntry) isDegraded() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.degraded
}

// incRetryAndMaybeMarkDegraded increments retry count, optionally marks degraded if count >= max,
// and returns the backoff delay and current retry count. justMarkedDegraded is true only when
// the entry was not degraded and is now marked degraded by this call.
func (e *retryStateEntry) incRetryAndMaybeMarkDegraded(maxBeforeDegraded int) (delay time.Duration, retryCount int, justMarkedDegraded bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delay = backoffDelay(e.retryCount)
	e.retryCount++

	prev := e.degraded
	if e.retryCount >= maxBeforeDegraded {
		e.degraded = true
	}

	justMarkedDegraded = !prev && e.degraded

	return delay, e.retryCount, justMarkedDegraded
}

// startTenantConsumer spawns a consumer goroutine for a tenant.
// MUST be called with c.mu held.
func (c *MultiTenantConsumer) startTenantConsumer(parentCtx context.Context, tenantID string) {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(parentCtx)
	logger := logcompat.New(baseLogger)

	parentCtx, span := tracer.Start(parentCtx, "consumer.multi_tenant_consumer.start_tenant_consumer")
	defer span.End()

	// Create a cancellable context for this tenant
	tenantCtx, cancel := context.WithCancel(parentCtx) //#nosec G118 -- cancel stored in c.tenants[tenantID] and called when tenant consumer is stopped

	// Store the cancel function (caller holds lock)
	c.tenants[tenantID] = cancel

	logger.InfofCtx(parentCtx, "starting consumer for tenant: %s", tenantID)

	// Spawn consumer goroutine
	go c.superviseTenantQueues(tenantCtx, tenantID)
}

// superviseTenantQueues runs the consumer loop for a single tenant.
func (c *MultiTenantConsumer) superviseTenantQueues(ctx context.Context, tenantID string) {
	// Set tenantID in context for handlers
	ctx = core.SetTenantIDInContext(ctx, tenantID)

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.consume_for_tenant")
	defer span.End()

	logger = logger.WithFields("tenant_id", tenantID)
	logger.InfoCtx(ctx, "consumer started for tenant")

	// Get all registered handlers (read-only, no lock needed after initial registration)
	c.mu.RLock()

	handlers := make(map[string]HandlerFunc, len(c.handlers))
	maps.Copy(handlers, c.handlers)

	c.mu.RUnlock()

	// Consume from each registered queue
	for queueName, handler := range handlers {
		go c.consumeTenantQueue(ctx, tenantID, queueName, handler, logger)
	}

	// Wait for context cancellation
	<-ctx.Done()
	logger.InfoCtx(ctx, "consumer stopped for tenant")
}

// consumeTenantQueue consumes messages from a specific queue for a tenant.
// Each connection attempt creates a short-lived span to avoid accumulating events
// on a long-lived span that would grow unbounded over the consumer's lifetime.
func (c *MultiTenantConsumer) consumeTenantQueue(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	_ *logcompat.Logger,
) {
	baseLogger, _, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled
	logger := logcompat.New(baseLogger).WithFields("tenant_id", tenantID, "queue", queueName)

	// Guard against nil RabbitMQ manager (e.g., during lazy mode testing)
	if c.rabbitmq == nil {
		logger.WarnCtx(ctx, "RabbitMQ manager is nil, cannot consume from queue")
		return
	}

	for {
		select {
		case <-ctx.Done():
			logger.InfoCtx(ctx, "queue consumer stopped")
			return
		default:
		}

		shouldContinue := c.attemptConsumeConnection(ctx, tenantID, queueName, handler, logger)
		if !shouldContinue {
			return
		}

		logger.WarnCtx(ctx, "channel closed, reconnecting...")
	}
}

// attemptConsumeConnection attempts to establish a channel and consume messages.
// Returns true if the loop should continue (reconnect), false if it should stop.
// Uses exponential backoff with per-tenant retry state for connection failures.
func (c *MultiTenantConsumer) attemptConsumeConnection(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	logger *logcompat.Logger,
) bool {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled

	connCtx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.consume_connection")
	defer span.End()

	state := c.getRetryState(tenantID)

	// Get channel for this tenant's vhost
	ch, err := c.rabbitmq.GetChannel(connCtx, tenantID)
	if err != nil {
		// If the tenant is suspended or purged, stop the consumer instead of retrying.
		// Retrying a suspended/purged tenant would cause infinite reconnect loops.
		if core.IsTenantSuspendedError(err) || core.IsTenantPurgedError(err) {
			logger.WarnfCtx(ctx, "tenant %s is suspended/purged, stopping consumer: %v", tenantID, err)
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant suspended/purged, stopping consumer", err)
			c.evictSuspendedTenant(ctx, tenantID, logger)

			return false
		}

		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.WarnfCtx(ctx, "tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.WarnfCtx(ctx, "failed to get channel for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(span, "failed to get channel", err)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
			return true
		}
	}

	// Set QoS
	if err := ch.Qos(c.config.PrefetchCount, 0, false); err != nil {
		_ = ch.Close() // Close channel to prevent leak

		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.WarnfCtx(ctx, "tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.WarnfCtx(ctx, "failed to set QoS for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(span, "failed to set QoS", err)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
			return true
		}
	}

	// Start consuming
	msgs, err := ch.Consume(
		queueName,
		"",    // consumer tag
		false, // auto-ack
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,   // args
	)
	if err != nil {
		_ = ch.Close() // Close channel to prevent leak

		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.WarnfCtx(ctx, "tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.WarnfCtx(ctx, "failed to start consuming for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(span, "failed to start consuming", err)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
			return true
		}
	}

	// Connection succeeded: reset retry state
	c.resetRetryState(tenantID)

	logger.InfofCtx(ctx, "consuming started for tenant %s on queue %s", tenantID, queueName)

	// Setup channel close notification
	notifyClose := make(chan *amqp.Error, 1)
	ch.NotifyClose(notifyClose)

	// Process messages (blocks until channel closes or context is cancelled)
	c.processMessages(ctx, tenantID, queueName, handler, msgs, notifyClose, logger)

	return true
}

// processMessages processes messages from the channel until it closes.
// Each message is processed with its own span to avoid accumulating events on a long-lived span.
func (c *MultiTenantConsumer) processMessages(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	msgs <-chan amqp.Delivery,
	notifyClose <-chan *amqp.Error,
	_ *logcompat.Logger,
) {
	baseLogger, _, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled
	logger := logcompat.New(baseLogger).WithFields("tenant_id", tenantID, "queue", queueName)

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-notifyClose:
			if err != nil {
				logger.WarnfCtx(ctx, "channel closed with error: %v", err)
			}

			return
		case msg, ok := <-msgs:
			if !ok {
				logger.WarnCtx(ctx, "message channel closed")
				return
			}

			c.handleMessage(ctx, tenantID, queueName, handler, msg, logger)
		}
	}
}

// handleMessage processes a single message with its own span.
func (c *MultiTenantConsumer) handleMessage(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	msg amqp.Delivery,
	logger *logcompat.Logger,
) {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled

	// Process message with tenant context
	msgCtx := core.SetTenantIDInContext(ctx, tenantID)

	// Extract trace context from message headers
	msgCtx = libOpentelemetry.ExtractTraceContextFromQueueHeaders(msgCtx, msg.Headers)

	// Create a per-message span
	msgCtx, span := tracer.Start(msgCtx, "consumer.multi_tenant_consumer.handle_message")
	defer span.End()

	if err := handler(msgCtx, msg); err != nil {
		logger.ErrorfCtx(ctx, "handler error for queue %s: %v", queueName, err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "handler error", err)

		if nackErr := msg.Nack(false, true); nackErr != nil {
			logger.ErrorfCtx(ctx, "failed to nack message: %v", nackErr)
		}
	} else {
		// Ack on success
		if ackErr := msg.Ack(false); ackErr != nil {
			logger.ErrorfCtx(ctx, "failed to ack message: %v", ackErr)
		}
	}
}

// initialBackoff is the base delay for exponential backoff on connection failures.
const initialBackoff = 5 * time.Second

// maxBackoff is the maximum delay between retry attempts.
const maxBackoff = 40 * time.Second

// maxRetryBeforeDegraded is the number of consecutive failures before marking a tenant as degraded.
const maxRetryBeforeDegraded = 3

// backoffDelay calculates the exponential backoff delay for a given retry count
// with +/-25% jitter to prevent thundering herd when multiple tenants retry simultaneously.
// Base sequence: 5s, 10s, 20s, 40s, 40s, ... (before jitter).
func backoffDelay(retryCount int) time.Duration {
	delay := initialBackoff
	for range retryCount {
		delay *= 2
		if delay > maxBackoff {
			delay = maxBackoff

			break
		}
	}

	// Apply +/-25% jitter: multiply by a random factor in [0.75, 1.25).
	// Uses crypto/rand to satisfy gosec G404.
	var b [8]byte

	_, _ = crand.Read(b[:])

	jitter := 0.75 + float64(binary.LittleEndian.Uint64(b[:]))/(1<<64)*0.5

	return time.Duration(float64(delay) * jitter)
}

// getRetryState returns the retry state entry for a tenant, creating one if it does not exist.
func (c *MultiTenantConsumer) getRetryState(tenantID string) *retryStateEntry {
	entry, _ := c.retryState.LoadOrStore(tenantID, &retryStateEntry{})

	val, ok := entry.(*retryStateEntry)
	if !ok {
		return &retryStateEntry{}
	}

	return val
}

// resetRetryState resets the retry counter and degraded flag for a tenant after a successful connection.
// It reuses the existing entry when present (reset in place) to avoid allocation churn; only stores
// a new entry when the tenant has no entry yet.
func (c *MultiTenantConsumer) resetRetryState(tenantID string) {
	if entry, ok := c.retryState.Load(tenantID); ok {
		if state, ok := entry.(*retryStateEntry); ok {
			state.reset()
			return
		}
	}

	c.retryState.Store(tenantID, &retryStateEntry{})
}

// ensureConsumerStarted ensures a consumer is running for the given tenant.
// It uses double-check locking with a per-tenant mutex to guarantee exactly-once
// consumer spawning under concurrent access.
// This is the primary entry point for on-demand consumer creation in lazy mode.
//
// Consumers are only started for tenants that are known (resolved via discovery or
// sync). Unknown tenants are rejected to prevent starting consumers for tenants
// that have not been validated by the sync loop.
func (c *MultiTenantConsumer) ensureConsumerStarted(ctx context.Context, tenantID string) {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.ensure_consumer_started")
	defer span.End()

	// Fast path: check if consumer is already active (read lock only)
	c.mu.RLock()

	_, exists := c.tenants[tenantID]
	known := c.knownTenants[tenantID]
	closed := c.closed
	c.mu.RUnlock()

	if exists || closed {
		return
	}

	// Reject unknown tenants: they haven't been discovered or validated yet.
	// The sync loop will add them to knownTenants when they appear.
	if !known {
		logger.WarnfCtx(ctx, "rejecting consumer start for unknown tenant: %s (not yet resolved by sync)", tenantID)

		return
	}

	// Slow path: acquire per-tenant mutex for double-check locking
	lockVal, _ := c.consumerLocks.LoadOrStore(tenantID, &sync.Mutex{})

	tenantMu, ok := lockVal.(*sync.Mutex)
	if !ok {
		return
	}

	tenantMu.Lock()
	defer tenantMu.Unlock()

	// Double-check under per-tenant lock
	c.mu.RLock()
	_, exists = c.tenants[tenantID]
	closed = c.closed
	c.mu.RUnlock()

	if exists || closed {
		return
	}

	// Use stored parentCtx if available (from Run()), otherwise use the provided ctx.
	// Protected by c.mu.RLock because Run() writes parentCtx concurrently.
	c.mu.RLock()

	startCtx := ctx
	if c.parentCtx != nil {
		startCtx = c.parentCtx
	}

	c.mu.RUnlock()

	logger.InfofCtx(ctx, "on-demand consumer start for tenant: %s", tenantID)

	c.mu.Lock()
	c.startTenantConsumer(startCtx, tenantID)
	c.mu.Unlock()
}

// EnsureConsumerStarted is the public API for triggering on-demand consumer spawning.
// It is safe for concurrent use by multiple goroutines.
// If the consumer for the given tenant is already running, this is a no-op.
func (c *MultiTenantConsumer) EnsureConsumerStarted(ctx context.Context, tenantID string) {
	c.ensureConsumerStarted(ctx, tenantID)
}

// IsDegraded returns true if the given tenant is currently in a degraded state
// due to repeated connection failures (>= maxRetryBeforeDegraded consecutive failures).
func (c *MultiTenantConsumer) IsDegraded(tenantID string) bool {
	entry, ok := c.retryState.Load(tenantID)
	if !ok {
		return false
	}

	state, ok := entry.(*retryStateEntry)
	if !ok {
		return false
	}

	return state.isDegraded()
}
