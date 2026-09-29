// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"
	"time"

	obsbridge "github.com/LerianStudio/lib-commons/v7/commons/obs/obsbridge"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/LerianStudio/lib-commons/v7/commons/backoff"
	"github.com/LerianStudio/lib-commons/v7/commons/rabbitmq"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/logcompat"
)

// Headers the library writes on messages it republishes. Handlers may read
// them; none of them ever carries error text.
const (
	// HeaderRetryAttempt counts the retries a message has already been through.
	// It is an application header because the broker's own bookkeeping cannot
	// be relied on: x-death is overwritten on each dead-letter hop and
	// x-delivery-count exists only on quorum queues.
	HeaderRetryAttempt = "x-lc-retry-attempt"
	// HeaderOriginExchange keeps the exchange a message first arrived through,
	// so the retry hop through the default exchange does not lose it.
	HeaderOriginExchange = "x-lc-origin-exchange"
	// HeaderOriginRoutingKey keeps the routing key a message first arrived with.
	HeaderOriginRoutingKey = "x-lc-origin-routing-key"
	// HeaderDeadLetterQueue names the queue a dead-lettered message left.
	HeaderDeadLetterQueue = "x-lc-dead-letter-queue"
	// HeaderDeadLetterAttempts is the number of deliveries handled before the
	// message was dead-lettered.
	HeaderDeadLetterAttempts = "x-lc-dead-letter-attempts"
	// HeaderDeadLetterClass says why the message was dead-lettered:
	// DeadLetterClassHandler or DeadLetterClassRetryExhausted.
	HeaderDeadLetterClass = "x-lc-dead-letter-class"
)

// Values of HeaderDeadLetterClass.
const (
	// DeadLetterClassHandler marks a message the handler refused permanently.
	DeadLetterClassHandler = "handler"
	// DeadLetterClassRetryExhausted marks a message that failed on every attempt
	// its RetryPolicy allowed.
	DeadLetterClassRetryExhausted = "retry-exhausted"
)

var (
	// ErrDeadLetterRouteRequired is returned by RegisterQueue when the policy
	// does not name exactly one dead-letter route.
	ErrDeadLetterRouteRequired = errors.New("consumer: exactly one dead-letter route is required: a RoutingKey (explicit publish) or ViaQueueDLX")
	// ErrInvalidRetryPolicy is returned by RegisterQueue for a negative or
	// inverted RetryPolicy.
	ErrInvalidRetryPolicy = errors.New("consumer: invalid retry policy")
	// ErrConsumerNotRunning is returned by StartActiveTenants before Run or after Close.
	ErrConsumerNotRunning = errors.New("consumer: multi-tenant consumer is not running; call Run first")
	// ErrNilConsumer is returned by methods called on a nil *MultiTenantConsumer.
	ErrNilConsumer = errors.New("consumer: nil MultiTenantConsumer")
)

// Disposition is what the consumer does with a message whose handler failed,
// on a queue registered with RegisterQueue.
type Disposition uint8

const (
	// DispositionRetry republishes the message after a backed-off delay, up to
	// RetryPolicy.MaxAttempts deliveries, then dead-letters it.
	DispositionRetry Disposition = iota + 1
	// DispositionDeadLetter sends the message to the queue's dead-letter route
	// at once. Use it for permanent refusals (forged signature, divergent tenant
	// claim, digest mismatch): retrying them cannot succeed.
	DispositionDeadLetter
	// DispositionAck acknowledges the message despite the error, logged at WARN.
	DispositionAck
)

// String returns the disposition name used in logs.
func (d Disposition) String() string {
	switch d {
	case DispositionRetry:
		return "retry"
	case DispositionDeadLetter:
		return "dead-letter"
	case DispositionAck:
		return "ack"
	default:
		return "unknown"
	}
}

// dispositionError carries a handler's disposition through error wrapping.
type dispositionError struct {
	disposition Disposition
	err         error
}

func (e *dispositionError) Error() string {
	if e.err == nil {
		return e.disposition.String()
	}

	return e.disposition.String() + ": " + e.err.Error()
}

func (e *dispositionError) Unwrap() error { return e.err }

// RetryLater marks err as transient: the message is retried with backoff.
// On a RegisterQueue queue an unmarked error means the same thing.
// The result is never nil, so RetryLater(nil) still asks for a retry.
func RetryLater(err error) error {
	return &dispositionError{disposition: DispositionRetry, err: err}
}

// DeadLetter marks err as permanent: the message goes to the dead-letter route
// without being retried. The result is never nil.
func DeadLetter(err error) error {
	return &dispositionError{disposition: DispositionDeadLetter, err: err}
}

// Ack marks err as one the message should be acknowledged despite, for example
// a duplicate already processed. The result is never nil.
func Ack(err error) error {
	return &dispositionError{disposition: DispositionAck, err: err}
}

// DispositionOf reports the disposition a handler error asks for: DispositionAck
// for nil, the marked disposition for an error built with RetryLater, DeadLetter
// or Ack (found through any wrapping, errors.Join included), and DispositionRetry
// for any other error.
//
// Dispositions apply only to queues registered with RegisterQueue; a queue
// registered with Register requeues every error.
func DispositionOf(err error) Disposition {
	if err == nil {
		return DispositionAck
	}

	var marked *dispositionError
	if errors.As(err, &marked) {
		return marked.disposition
	}

	return DispositionRetry
}

// RetryPolicy bounds the retries of a RegisterQueue queue.
type RetryPolicy struct {
	// MaxAttempts is the number of deliveries a message gets, the first one
	// included, before it is dead-lettered with class retry-exhausted.
	// 1 means no retry. Zero takes the default.
	MaxAttempts int
	// InitialDelay is the base of the exponential, fully jittered delay.
	// Zero takes the default.
	InitialDelay time.Duration
	// MaxDelay caps each delay. Zero takes the default.
	MaxDelay time.Duration
}

// DefaultRetryPolicy returns 5 attempts, 1s initial delay and 30s maximum delay.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 5, InitialDelay: time.Second, MaxDelay: 30 * time.Second}
}

// DeadLetterRoute says where a dead-lettered message goes. Exactly one of the
// two routes must be set.
type DeadLetterRoute struct {
	// Exchange and RoutingKey select an explicit publish, confirmed and
	// mandatory, of the original body and headers plus the x-lc-dead-letter-*
	// headers. This is the recommended route: an unroutable dead-letter fails
	// loudly and the message is requeued instead of lost. Exchange may be empty
	// (the default exchange, where RoutingKey is a queue name).
	Exchange, RoutingKey string

	// ViaQueueDLX answers a dead-letter with Nack(requeue=false) and lets the
	// broker route it to the queue's x-dead-letter-exchange. The caller asserts
	// that the queue was declared with one and that it exists: RabbitMQ silently
	// DISCARDS a message nacked from a queue without a working DLX, and the
	// library cannot inspect the queue's arguments to check.
	//
	// The broker dead-letters under the routing key the message was last
	// published with, and a message that went through the retry hop was last
	// published to the default exchange with the queue name as its key. The
	// DLQ's binding to the DLX must therefore match the queue name too, or
	// RabbitMQ also discards the message: keep the catch-all "#" binding of
	// rabbitmq.DeclareDLQTopology, or declare the queue with
	// x-dead-letter-routing-key so every dead-letter uses one fixed key.
	//
	// The broker forwards the message as it is, x-lc-retry-attempt included,
	// so a redrive from the DLQ back to the queue must drop that header to give
	// the message a fresh retry budget. The explicit-publish route drops it.
	ViaQueueDLX bool
}

// TopologyFunc declares a tenant's queue topology on the consume channel. It
// runs on every connect and reconnect, before Qos and Consume, so a queue and
// its dead-letter exchange exist in the tenant's vhost before consumption.
// Declarations must be idempotent. A typical hook for ViaQueueDLX calls
// rabbitmq.DeclareDLQTopology(ch, rabbitmq.WithDLXExchangeName(dlx),
// rabbitmq.WithDLQName(dlq)), whose default "#" binding matches any routing
// key, and then declares the queue itself with ch.QueueDeclare, passing
// rabbitmq.GetDLXArgs(dlx) as its arguments. A DLQ bound with a narrower key
// (rabbitmq.WithDLQBindingKey) must receive a fixed key instead:
//
//	args := rabbitmq.GetDLXArgs(dlx)
//	args["x-dead-letter-routing-key"] = bindingKey // then declare queueName with args
type TopologyFunc func(ctx context.Context, tenantID, queueName string, ch rabbitmq.AMQPChannel) error

// QueuePolicy opts a queue into dispositions. See RegisterQueue.
type QueuePolicy struct {
	// Retry bounds retries. The zero value means DefaultRetryPolicy().
	Retry RetryPolicy
	// DeadLetter is required.
	DeadLetter DeadLetterRoute
	// Topology is optional.
	Topology TopologyFunc
}

// normalizeQueuePolicy validates the policy and fills zero retry fields with
// the defaults.
func normalizeQueuePolicy(policy QueuePolicy) (QueuePolicy, error) {
	route := policy.DeadLetter

	publishRoute := route.RoutingKey != ""
	if route.ViaQueueDLX == publishRoute || (route.ViaQueueDLX && route.Exchange != "") {
		return QueuePolicy{}, ErrDeadLetterRouteRequired
	}

	retry := policy.Retry
	if retry.MaxAttempts < 0 || retry.InitialDelay < 0 || retry.MaxDelay < 0 {
		return QueuePolicy{}, fmt.Errorf("%w: negative value in %+v", ErrInvalidRetryPolicy, retry)
	}

	defaults := DefaultRetryPolicy()

	if retry.MaxAttempts == 0 {
		retry.MaxAttempts = defaults.MaxAttempts
	}

	if retry.InitialDelay == 0 {
		retry.InitialDelay = defaults.InitialDelay
	}

	if retry.MaxDelay == 0 {
		retry.MaxDelay = defaults.MaxDelay
	}

	if retry.InitialDelay > retry.MaxDelay {
		return QueuePolicy{}, fmt.Errorf("%w: InitialDelay %s exceeds MaxDelay %s", ErrInvalidRetryPolicy, retry.InitialDelay, retry.MaxDelay)
	}

	policy.Retry = retry

	return policy, nil
}

// RegisterQueue adds a queue handler, like Register, and opts the queue into
// dispositions: the handler's error decides the message's fate (see
// DispositionOf), instead of every error being requeued at once.
//
//   - Retry (any unmarked error, or RetryLater): after a delay of
//     min(ExponentialWithJitter(InitialDelay, n), MaxDelay), where n is the
//     retries already done, the message is republished to the same queue
//     through the default exchange, confirmed and mandatory, with
//     x-lc-retry-attempt set to n+1; the original is acked only after the
//     broker confirms. The delay is taken in the tenant queue's consumer, which
//     processes one message at a time: it is the backpressure that stops a
//     transient failure from hot-looping, and it holds the messages behind the
//     failing one for its duration (head-of-line blocking). The delivery the
//     handler sees keeps the exchange and routing key it first arrived with.
//     The republish drops the user-id property, which the broker would refuse
//     from a connection of a different user.
//   - Once a message has been delivered MaxAttempts times it is dead-lettered
//     with class retry-exhausted.
//   - DeadLetter: the message goes to policy.DeadLetter at once, class handler.
//   - Ack: the message is acknowledged; the error is logged at WARN.
//
// A republish or dead-letter publish that fails leaves the message on the
// queue: it is nacked with requeue, never dropped. While the publish channel
// stays usable (the broker nacked or returned the message) the requeue waits a
// backoff that grows with each consecutive failure, up to MaxDelay. When the
// publish closes the channel (a dead-letter exchange missing from the tenant's
// vhost is the common cause) the consumer reconnects through the tenant
// backoff, which keeps growing across those reconnects and marks the tenant
// degraded; a later confirmed publish on that queue clears both. Delivery is
// at least once: a crash between a confirmed republish and the ack of the
// original delivers the message twice, so handlers must be idempotent.
//
// Each tenant's consumer of the queue opens its own confirm-mode publish
// channel on the tenant connection, and runs policy.Topology before consuming.
// A failure opening either takes the same backoff and degraded-tenant path as
// a failed consume.
//
// RegisterQueue returns ErrNilConsumer on a nil receiver, an error when
// RabbitMQ is not configured, core.ErrNilHandlerFunc for a nil handler,
// ErrDeadLetterRouteRequired unless exactly one dead-letter route is set, and
// ErrInvalidRetryPolicy for negative or inverted delays. Calling Register for
// the same queue afterwards returns it to requeue-every-error behavior.
func (c *MultiTenantConsumer) RegisterQueue(queueName string, handler HandlerFunc, policy QueuePolicy) error {
	if c == nil {
		return ErrNilConsumer
	}

	if c.rabbitmq == nil {
		return errors.New("consumer.RegisterQueue: RabbitMQ manager is required for queue consumption; use WithRabbitMQ() option")
	}

	if handler == nil {
		return fmt.Errorf("consumer.RegisterQueue: queue %q: %w", queueName, core.ErrNilHandlerFunc)
	}

	normalized, err := normalizeQueuePolicy(policy)
	if err != nil {
		return fmt.Errorf("consumer.RegisterQueue: queue %q: %w", queueName, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.handlers == nil {
		c.handlers = make(map[string]HandlerFunc)
	}

	if c.policies == nil {
		c.policies = make(map[string]*QueuePolicy)
	}

	c.handlers[queueName] = handler
	c.policies[queueName] = &normalized

	if c.logger != nil {
		c.logger.Infof("registered handler with disposition policy for queue: %s", queueName)
	}

	return nil
}

// DeliveryAttempt returns the number of retries a delivery has already been
// through, read from the x-lc-retry-attempt header: 0 when absent, negative or
// not an integer. It is a count the library maintains, not an authenticated
// value: whoever can publish to the queue can set it.
func DeliveryAttempt(d amqp.Delivery) int {
	var n int64

	switch v := d.Headers[HeaderRetryAttempt].(type) {
	case int:
		n = int64(v)
	case int8:
		n = int64(v)
	case int16:
		n = int64(v)
	case int32:
		n = int64(v)
	case int64:
		n = v
	case uint8:
		n = int64(v)
	case uint16:
		n = int64(v)
	case uint32:
		n = int64(v)
	default:
		return 0
	}

	if n < 0 {
		return 0
	}

	if n > math.MaxInt32 {
		return math.MaxInt32
	}

	return int(n)
}

// StartActiveTenants starts a consumer for every tenant the tenant manager
// lists as active for this service, and returns how many active tenants have
// a running consumer afterwards.
//
// The consumer is lazy by default: Run starts nothing, and a tenant's consumer
// starts on its first tenant lifecycle event or EnsureConsumerStarted call. A
// process that receives no request for a tenant (a worker) therefore consumes
// nothing after a restart until one of those happens; such a process calls
// StartActiveTenants once after Run.
//
// It returns ErrNilConsumer on a nil receiver, ErrConsumerNotRunning before Run
// or after Close, and the tenant listing error, wrapped. Without RabbitMQ
// (HTTP-only mode) there is nothing to start and it returns (0, nil). A
// listed tenant naming a status other than active is skipped.
func (c *MultiTenantConsumer) StartActiveTenants(ctx context.Context) (int, error) {
	if c == nil {
		return 0, ErrNilConsumer
	}

	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.RLock()
	running := c.parentCtx != nil && !c.closed
	c.mu.RUnlock()

	if !running {
		return 0, ErrConsumerNotRunning
	}

	if c.rabbitmq == nil {
		return 0, nil
	}

	if c.pmClient == nil {
		return 0, errors.New("consumer.StartActiveTenants: tenant manager client is not configured")
	}

	summaries, err := c.pmClient.GetActiveTenantsByService(ctx, c.config.Service)
	if err != nil {
		return 0, fmt.Errorf("consumer.StartActiveTenants: list active tenants: %w", err)
	}

	started := 0

	for _, summary := range summaries {
		if summary == nil {
			continue
		}

		tenantID := strings.TrimSpace(summary.ID)
		if tenantID == "" || (summary.Status != "" && !strings.EqualFold(summary.Status, "active")) {
			continue
		}

		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return started, ErrConsumerNotRunning
		}

		if c.knownTenants == nil {
			c.knownTenants = make(map[string]bool)
		}

		c.knownTenants[tenantID] = true
		c.mu.Unlock()

		c.EnsureConsumerStarted(ctx, tenantID)

		c.mu.RLock()
		_, active := c.tenants[tenantID]
		c.mu.RUnlock()

		if active {
			started++
		}
	}

	return started, nil
}

// dispositionPublisher is the confirmed publish a disposition needs;
// *rabbitmq.ConfirmablePublisher satisfies it.
type dispositionPublisher interface {
	PublishAndWaitConfirm(ctx context.Context, exchange, routingKey string, mandatory, immediate bool, msg amqp.Publishing) error
	Close() error
}

type waitFunc func(ctx context.Context, d time.Duration) error

// dispositionRuntime is one tenant queue's disposition state. It lives as long
// as that queue's consumer loop, across reconnects, and is used by that loop's
// goroutine only, so it needs no lock. publisher is replaced on every connect.
type dispositionRuntime struct {
	queue     string
	policy    QueuePolicy
	publisher dispositionPublisher
	wait      waitFunc
	// publishFailures counts consecutive failed publishes across reconnects;
	// it grows the backoff so a broken dead-letter route cannot hot-loop, and
	// only a confirmed publish resets it.
	publishFailures int
	// onRecovered runs when a publish is confirmed after failures.
	onRecovered func()
}

func newDispositionRuntime(queue string, policy QueuePolicy, publisher dispositionPublisher, wait waitFunc) *dispositionRuntime {
	if wait == nil {
		wait = backoff.WaitContext
	}

	return &dispositionRuntime{queue: queue, policy: policy, publisher: publisher, wait: wait}
}

// delay returns min(ExponentialWithJitter(InitialDelay, attempt), MaxDelay).
func (rt *dispositionRuntime) delay(attempt int) time.Duration {
	return min(backoff.ExponentialWithJitter(rt.policy.Retry.InitialDelay, attempt), rt.policy.Retry.MaxDelay)
}

// handleWithDisposition runs the handler and disposes of the message by the
// queue's policy. It returns a non-nil error when the publish channel became
// unusable and the caller must back off and reconnect.
func (c *MultiTenantConsumer) handleWithDisposition(
	ctx context.Context,
	tenantID string,
	handler HandlerFunc,
	rt *dispositionRuntime,
	msg amqp.Delivery,
	logger *logcompat.Logger,
) error {
	_, tracer, _, _ := obsbridge.TrackingFromContext(ctx) //nolint:dogsled

	msgCtx := core.ContextWithTenantID(ctx, tenantID)
	msgCtx = libOpentelemetry.ExtractTraceContextFromQueueHeaders(msgCtx, msg.Headers)

	msgCtx, span := tracer.Start(msgCtx, "consumer.multi_tenant_consumer.handle_message")
	defer span.End()

	restoreOriginRouting(&msg, rt.queue)

	attempt := DeliveryAttempt(msg)

	err := handler(msgCtx, msg)
	if err == nil {
		ackDelivery(ctx, msg, logger)
		return nil
	}

	disposition := DispositionOf(err)

	logger.ErrorfCtx(ctx, "handler error for queue %s (attempt %d, disposition %s): %v", rt.queue, attempt+1, disposition, err)
	libOpentelemetry.HandleSpanBusinessErrorEvent(span, "handler error", err)

	switch disposition {
	case DispositionAck:
		logger.WarnfCtx(ctx, "acknowledging message on queue %s despite handler error", rt.queue)
		ackDelivery(ctx, msg, logger)

		return nil
	case DispositionDeadLetter:
		return rt.deadLetter(ctx, msg, attempt+1, DeadLetterClassHandler, logger)
	default:
		if attempt+1 >= rt.policy.Retry.MaxAttempts {
			return rt.deadLetter(ctx, msg, attempt+1, DeadLetterClassRetryExhausted, logger)
		}

		return rt.retry(ctx, msg, attempt, logger)
	}
}

// retry waits the backed-off delay, republishes the message to its queue with
// the attempt incremented, and acks the original after the confirm.
func (rt *dispositionRuntime) retry(ctx context.Context, msg amqp.Delivery, attempt int, logger *logcompat.Logger) error {
	if err := rt.wait(ctx, rt.delay(attempt)); err != nil {
		nackRequeue(ctx, msg, logger)

		return nil //nolint:nilerr // an interrupted delay requeues the message; the publisher is still usable
	}

	headers := cloneHeaders(msg.Headers)
	headers[HeaderRetryAttempt] = headerInt(attempt + 1)
	headers[HeaderOriginExchange] = msg.Exchange
	headers[HeaderOriginRoutingKey] = msg.RoutingKey

	if err := rt.publisher.PublishAndWaitConfirm(ctx, "", rt.queue, true, false, republishing(msg, headers)); err != nil {
		return rt.publishFailed(ctx, msg, "retry republish", err, logger)
	}

	rt.publishConfirmed()
	ackDelivery(ctx, msg, logger)

	return nil
}

// deadLetter sends the message to the queue's dead-letter route. The explicit
// publish drops x-lc-retry-attempt (x-lc-dead-letter-attempts keeps the count),
// so a message redriven from the dead-letter queue starts a fresh retry budget.
func (rt *dispositionRuntime) deadLetter(ctx context.Context, msg amqp.Delivery, attempts int, class string, logger *logcompat.Logger) error {
	route := rt.policy.DeadLetter

	if route.ViaQueueDLX {
		logger.WarnfCtx(ctx, "dead-lettering message from queue %s through its DLX (class %s, attempts %d)", rt.queue, class, attempts)

		if err := msg.Nack(false, false); err != nil {
			logger.ErrorfCtx(ctx, "failed to nack message for dead-lettering: %v", err)
		}

		return nil
	}

	headers := cloneHeaders(msg.Headers)
	delete(headers, HeaderRetryAttempt)
	headers[HeaderDeadLetterQueue] = rt.queue
	headers[HeaderDeadLetterAttempts] = headerInt(attempts)
	headers[HeaderDeadLetterClass] = class

	if err := rt.publisher.PublishAndWaitConfirm(ctx, route.Exchange, route.RoutingKey, true, false, republishing(msg, headers)); err != nil {
		return rt.publishFailed(ctx, msg, "dead-letter publish", err, logger)
	}

	rt.publishConfirmed()
	logger.WarnfCtx(ctx, "dead-lettered message from queue %s to exchange %q routing key %q (class %s, attempts %d)",
		rt.queue, route.Exchange, route.RoutingKey, class, attempts)
	ackDelivery(ctx, msg, logger)

	return nil
}

// publishFailed leaves the message on its queue with a nack with requeue.
// While the publisher is still usable it first waits a backoff that grows with
// each consecutive failure. When the publisher cannot be used again it returns
// the error at once: the caller then backs off through the tenant reconnect
// path, whose delay keeps growing because publishFailures survives reconnects.
func (rt *dispositionRuntime) publishFailed(ctx context.Context, msg amqp.Delivery, what string, err error, logger *logcompat.Logger) error {
	failures := rt.publishFailures
	rt.publishFailures++

	if publisherUnusable(err) {
		logger.ErrorfCtx(ctx, "%s failed for queue %s (%d consecutive), requeueing the message and reconnecting: %v",
			what, rt.queue, rt.publishFailures, err)
		nackRequeue(ctx, msg, logger)

		return fmt.Errorf("%s: %w", what, err)
	}

	logger.ErrorfCtx(ctx, "%s failed for queue %s (%d consecutive), requeueing the message: %v",
		what, rt.queue, rt.publishFailures, err)

	_ = rt.wait(ctx, rt.delay(failures)) // a cancelled wait only shortens the backoff; the message is requeued either way

	nackRequeue(ctx, msg, logger)

	return nil
}

// publishConfirmed resets the failure count after a confirmed publish and
// reports the recovery when there were failures to recover from.
func (rt *dispositionRuntime) publishConfirmed() {
	if rt.publishFailures == 0 {
		return
	}

	rt.publishFailures = 0

	if rt.onRecovered != nil {
		rt.onRecovered()
	}
}

// publisherUnusable reports whether a publish error leaves the confirm channel
// closed, so the consumer must reconnect to get a working one.
func publisherUnusable(err error) bool {
	return errors.Is(err, rabbitmq.ErrPublisherClosed) ||
		errors.Is(err, rabbitmq.ErrPublisherNotReady) ||
		errors.Is(err, rabbitmq.ErrConfirmTimeout) ||
		errors.Is(err, rabbitmq.ErrRecoveryExhausted)
}

// restoreOriginRouting gives the handler the exchange and routing key a
// retried message first arrived with. Only a message that came through the
// library's retry hop (default exchange, routing key = queue) is rewritten.
func restoreOriginRouting(msg *amqp.Delivery, queue string) {
	if msg.Exchange != "" || msg.RoutingKey != queue {
		return
	}

	exchange, hasExchange := msg.Headers[HeaderOriginExchange].(string)
	routingKey, hasKey := msg.Headers[HeaderOriginRoutingKey].(string)

	if !hasExchange || !hasKey {
		return
	}

	msg.Exchange = exchange
	msg.RoutingKey = routingKey
}

func republishing(msg amqp.Delivery, headers amqp.Table) amqp.Publishing {
	return amqp.Publishing{
		Headers:         headers,
		ContentType:     msg.ContentType,
		ContentEncoding: msg.ContentEncoding,
		DeliveryMode:    msg.DeliveryMode,
		Priority:        msg.Priority,
		CorrelationId:   msg.CorrelationId,
		ReplyTo:         msg.ReplyTo,
		Expiration:      msg.Expiration,
		MessageId:       msg.MessageId,
		Timestamp:       msg.Timestamp,
		Type:            msg.Type,
		AppId:           msg.AppId,
		Body:            msg.Body,
	}
}

func cloneHeaders(headers amqp.Table) amqp.Table {
	clone := make(amqp.Table, len(headers)+3)
	maps.Copy(clone, headers)

	return clone
}

func headerInt(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}

	if n < 0 {
		return 0
	}

	return int32(n)
}

func ackDelivery(ctx context.Context, msg amqp.Delivery, logger *logcompat.Logger) {
	if err := msg.Ack(false); err != nil {
		logger.ErrorfCtx(ctx, "failed to ack message: %v", err)
	}
}

func nackRequeue(ctx context.Context, msg amqp.Delivery, logger *logcompat.Logger) {
	if err := msg.Nack(false, true); err != nil {
		logger.ErrorfCtx(ctx, "failed to nack message: %v", err)
	}
}

// consumeChannel is the consume-side channel surface; *amqp.Channel satisfies it.
type consumeChannel interface {
	rabbitmq.AMQPChannel
	Qos(prefetchCount, prefetchSize int, global bool) error
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	NotifyClose(c chan *amqp.Error) chan *amqp.Error
	Close() error
}

func (c *MultiTenantConsumer) openConsumeChannel(ctx context.Context, tenantID string) (consumeChannel, error) {
	if c.openConsumeChannelFn != nil {
		return c.openConsumeChannelFn(ctx, tenantID)
	}

	ch, err := c.rabbitmq.GetChannel(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return ch, nil
}

func (c *MultiTenantConsumer) openDispositionPublisher(ctx context.Context, tenantID string) (dispositionPublisher, error) {
	if c.openPublisherFn != nil {
		return c.openPublisherFn(ctx, tenantID)
	}

	ch, err := c.rabbitmq.GetChannel(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	var opts []rabbitmq.ConfirmablePublisherOption
	if c.logger != nil {
		opts = append(opts, rabbitmq.WithLogger(c.logger.Base()))
	}

	publisher, err := rabbitmq.NewConfirmablePublisherFromChannel(ch, opts...)
	if err != nil {
		_ = ch.Close()
		return nil, err
	}

	return publisher, nil
}
