package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-commons/v7/commons/backoff"
	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-observability/v4/runtime"
	amqp "github.com/rabbitmq/amqp091-go"
)

// recoveryAttemptResult indicates the outcome of a single recovery attempt.
type recoveryAttemptResult int

const (
	recoveryAttemptRetry   recoveryAttemptResult = iota // retry next attempt
	recoveryAttemptSuccess                              // recovery succeeded
	recoveryAttemptAborted                              // recovery aborted externally
)

// Publisher confirm errors.
var (
	// ErrConnectionRequired aliases ErrNilConnection for naming consistency in publisher constructors.
	ErrConnectionRequired = ErrNilConnection
	// ErrPublisherRequired is returned by a constructor handed a nil confirmable publisher.
	ErrPublisherRequired = errors.New("confirmable publisher is required")
	// ErrChannelRequired is returned by a constructor handed a nil channel.
	ErrChannelRequired = errors.New("rabbitmq channel is required")
	// ErrPublisherNotReady is returned when a publish is attempted before the publisher holds a channel.
	ErrPublisherNotReady = errors.New("confirmable publisher not initialized")
	// ErrConfirmModeUnavailable is returned when the broker refuses to put the channel in confirm mode.
	ErrConfirmModeUnavailable = errors.New("channel does not support confirm mode")
	// ErrPublishNacked is returned when the broker negatively acknowledges a published message.
	ErrPublishNacked = errors.New("message was nacked by broker")
	// ErrPublishReturned is returned when the broker hands a message back as unroutable:
	// no queue was bound to its routing key, so it would otherwise have been discarded.
	ErrPublishReturned = errors.New("message was returned as unroutable by broker")
	// ErrConfirmTimeout is returned when the broker does not confirm a publish within the timeout.
	ErrConfirmTimeout = errors.New("confirmation timed out")
	// ErrReturnNotificationUnsupported is returned when the supplied channel
	// cannot report unroutable messages. Construction fails closed rather than
	// publishing without return detection, because a broker ACKs a message it
	// discarded for want of a queue: confirms alone would report success.
	ErrReturnNotificationUnsupported = errors.New("channel does not support return notifications")
	// ErrPublisherClosed is returned when a publish is attempted after Close.
	ErrPublisherClosed = errors.New("publisher is closed")
	// ErrReconnectAfterClose is returned when Reconnect is called on a publisher that was explicitly closed.
	ErrReconnectAfterClose = errors.New("cannot reconnect: publisher was explicitly closed")
	// ErrReconnectWhileOpen is returned when Reconnect is called while the publisher still holds an open channel.
	ErrReconnectWhileOpen = errors.New("cannot reconnect: publisher is still open, call Close first")
	// ErrRecoveryExhausted is returned when automatic recovery gave up after its last attempt.
	ErrRecoveryExhausted = errors.New("automatic recovery exhausted all attempts")
)

const (
	// DefaultConfirmTimeout is the default timeout for waiting on broker confirmation.
	DefaultConfirmTimeout = 5 * time.Second

	// confirmChannelBuffer is the buffer size for the confirmation channel.
	// Should be >= max unconfirmed messages to avoid blocking.
	confirmChannelBuffer = 256

	// DefaultMaxRecoveryAttempts is the default number of recovery attempts before giving up.
	DefaultMaxRecoveryAttempts = 10

	// DefaultRecoveryBackoffInitial is the starting backoff duration for recovery retries.
	DefaultRecoveryBackoffInitial = 1 * time.Second

	// DefaultRecoveryBackoffMax is the maximum backoff duration between recovery retries.
	DefaultRecoveryBackoffMax = 30 * time.Second
)

// HealthState represents the current connection health of a ConfirmablePublisher.
type HealthState int

const (
	// HealthStateConnected indicates the publisher has a healthy AMQP channel
	// and is ready to publish messages.
	HealthStateConnected HealthState = iota

	// HealthStateReconnecting indicates the publisher detected a channel closure
	// and is actively attempting to recover by obtaining a new channel.
	HealthStateReconnecting

	// HealthStateDegraded indicates the publisher's confirmation stream was
	// corrupted (e.g., confirm timeout or context cancellation). The underlying
	// channel has been invalidated but auto-recovery may restore it. If no
	// auto-recovery is configured, callers should call Reconnect() to recover.
	HealthStateDegraded

	// HealthStateDisconnected indicates the publisher has exhausted all recovery
	// attempts and is no longer able to publish. Manual intervention is required.
	HealthStateDisconnected
)

// String returns a human-readable representation of the health state.
func (h HealthState) String() string {
	switch h {
	case HealthStateConnected:
		return "connected"
	case HealthStateReconnecting:
		return "reconnecting"
	case HealthStateDegraded:
		return "degraded"
	case HealthStateDisconnected:
		return "disconnected"
	default:
		return "unknown"
	}
}

// ChannelProvider is a function that returns a new AMQP channel for recovery.
// It is called by the auto-recovery goroutine when the current channel closes.
// The returned channel must be a fresh, dedicated channel (not shared with
// other publishers). The provider should handle its own connection management
// internally.
type ChannelProvider func() (ConfirmableChannel, error)

// HealthCallback is called when the publisher's connection health changes.
type HealthCallback func(HealthState)

// recoveryConfig holds the auto-recovery configuration.
// A nil recoveryConfig means auto-recovery is disabled.
type recoveryConfig struct {
	provider       ChannelProvider
	healthCallback HealthCallback
	maxAttempts    int
	backoffInitial time.Duration
	backoffMax     time.Duration
}

// ConfirmableChannel defines the interface for AMQP channel operations with confirms.
type ConfirmableChannel interface {
	Confirm(noWait bool) error
	NotifyPublish(confirm chan amqp.Confirmation) chan amqp.Confirmation
	NotifyClose(c chan *amqp.Error) chan *amqp.Error
	PublishWithContext(
		ctx context.Context,
		exchange, key string,
		mandatory, immediate bool,
		msg amqp.Publishing,
	) error
	Close() error
}

// returnNotifier is the capability used to observe unroutable messages.
//
// It is asserted on the dynamic type rather than declared on ConfirmableChannel
// so adding return detection does not break every caller-side implementation of
// that interface. *amqp.Channel satisfies it; a test double must add the method
// or publisher construction fails with ErrReturnNotificationUnsupported.
type returnNotifier interface {
	NotifyReturn(c chan amqp.Return) chan amqp.Return
}

// returnNotifierFor reports whether the channel can announce unroutable
// messages, WITHOUT touching it.
//
// Fails closed: a channel that cannot report returns cannot prove a message was
// routed, and publishing over it would silently lose unroutable messages.
//
// The check is separate from registration, and runs before Confirm, so a
// rejected channel goes back to its owner exactly as it arrived. Enabling
// confirm mode is irreversible and registering a confirmation listener nobody
// drains would eventually block the connection's dispatch loop.
func returnNotifierFor(ch ConfirmableChannel) (returnNotifier, error) {
	notifier, ok := ch.(returnNotifier)
	if !ok || nilcheck.Interface(notifier) {
		return nil, ErrReturnNotificationUnsupported
	}

	return notifier, nil
}

// registerReturnListener subscribes a fresh buffered return channel.
func registerReturnListener(notifier returnNotifier) chan amqp.Return {
	returns := make(chan amqp.Return, confirmChannelBuffer)
	notifier.NotifyReturn(returns)

	return returns
}

// takeReturn reports whether a return is already buffered, without blocking.
//
// Correlation rests on two facts. AMQP delivers basic.return before the
// basic.ack for the same message, and amqp091-go dispatches both from one
// goroutine into buffered channels, so a return for the message just published
// is queued by the time its confirmation is received. Publishes are serialized
// by publishMu, so at most one message is in flight per publisher and the
// buffered return can only belong to it.
func takeReturn(returns <-chan amqp.Return) (amqp.Return, bool) {
	select {
	case ret, ok := <-returns:
		return ret, ok
	default:
		return amqp.Return{}, false
	}
}

// drainReturns discards returns left over from an aborted publish so a stale
// one is never attributed to the next message.
func drainReturns(returns <-chan amqp.Return) {
	for {
		select {
		case _, ok := <-returns:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// ConfirmablePublisher wraps an AMQP channel with publisher confirms enabled.
type ConfirmablePublisher struct {
	ch                    ConfirmableChannel
	confirms              chan amqp.Confirmation
	returns               chan amqp.Return
	closedCh              chan struct{}
	closeOnce             *sync.Once
	done                  chan struct{}
	logger                obs.Logger
	confirmTimeout        time.Duration
	invalidConfirmTimeout struct {
		set   bool
		value time.Duration
	}
	recovery          *recoveryConfig
	mu                sync.RWMutex
	publishMu         sync.Mutex
	health            HealthState
	closed            bool
	shutdown          bool
	recoveryExhausted bool
}

// ConfirmablePublisherOption configures a ConfirmablePublisher.
type ConfirmablePublisherOption func(*ConfirmablePublisher)

// WithLogger sets a structured logger for the publisher.
func WithLogger(logger obs.Logger) ConfirmablePublisherOption {
	return func(pub *ConfirmablePublisher) {
		if nilcheck.Interface(logger) {
			return
		}

		pub.logger = logger
	}
}

// WithConfirmTimeout sets the timeout for waiting on broker confirmation.
func WithConfirmTimeout(timeout time.Duration) ConfirmablePublisherOption {
	return func(pub *ConfirmablePublisher) {
		if timeout > 0 {
			pub.confirmTimeout = timeout
			pub.invalidConfirmTimeout.set = false
			pub.invalidConfirmTimeout.value = 0

			return
		}

		pub.invalidConfirmTimeout.set = true
		pub.invalidConfirmTimeout.value = timeout
	}
}

// WithAutoRecovery enables automatic channel recovery.
func WithAutoRecovery(provider ChannelProvider) ConfirmablePublisherOption {
	return func(pub *ConfirmablePublisher) {
		if provider == nil {
			return
		}

		ensureRecoveryConfig(pub)

		pub.recovery.provider = provider
	}
}

// WithMaxRecoveryAttempts sets maximum consecutive recovery attempts.
func WithMaxRecoveryAttempts(maxAttempts int) ConfirmablePublisherOption {
	return func(pub *ConfirmablePublisher) {
		if maxAttempts <= 0 {
			return
		}

		ensureRecoveryConfig(pub)

		pub.recovery.maxAttempts = maxAttempts
	}
}

// WithRecoveryBackoff sets the initial and max backoff durations for recovery.
func WithRecoveryBackoff(initial, maxBackoff time.Duration) ConfirmablePublisherOption {
	return func(pub *ConfirmablePublisher) {
		if initial <= 0 || maxBackoff <= 0 {
			return
		}

		if initial > maxBackoff {
			logIfConfigured(
				pub.logger,
				obs.LevelWarn,
				fmt.Sprintf("rabbitmq: ignoring invalid recovery backoff initial=%v max=%v", initial, maxBackoff),
			)

			return
		}

		ensureRecoveryConfig(pub)

		pub.recovery.backoffInitial = initial
		pub.recovery.backoffMax = maxBackoff
	}
}

// WithHealthCallback registers a callback for health state changes.
func WithHealthCallback(fn HealthCallback) ConfirmablePublisherOption {
	return func(pub *ConfirmablePublisher) {
		if fn == nil {
			return
		}

		ensureRecoveryConfig(pub)

		pub.recovery.healthCallback = fn
	}
}

// NewConfirmablePublisher creates a publisher with confirms enabled.
//
// It puts the connection's SHARED channel (ChannelSnapshot) into confirm mode,
// which is irreversible for the life of that channel, so when another producer
// shares this connection use OpenChannelContext plus
// NewConfirmablePublisherFromChannel instead.
func NewConfirmablePublisher(
	conn *RabbitMQConnection,
	opts ...ConfirmablePublisherOption,
) (*ConfirmablePublisher, error) {
	if conn == nil {
		return nil, ErrConnectionRequired
	}

	channel := conn.ChannelSnapshot()

	if channel == nil {
		return nil, ErrChannelRequired
	}

	return NewConfirmablePublisherFromChannel(channel, opts...)
}

// NewConfirmablePublisherFromChannel creates a publisher from an existing channel.
func NewConfirmablePublisherFromChannel(
	ch ConfirmableChannel,
	opts ...ConfirmablePublisherOption,
) (*ConfirmablePublisher, error) {
	if nilcheck.Interface(ch) {
		return nil, ErrChannelRequired
	}

	notifier, err := returnNotifierFor(ch)
	if err != nil {
		return nil, err
	}

	if err := ch.Confirm(false); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfirmModeUnavailable, err)
	}

	confirms := make(chan amqp.Confirmation, confirmChannelBuffer)
	ch.NotifyPublish(confirms)

	returns := registerReturnListener(notifier)

	closeNotify := ch.NotifyClose(make(chan *amqp.Error, 1))

	publisher := &ConfirmablePublisher{
		ch:             ch,
		confirms:       confirms,
		returns:        returns,
		closedCh:       make(chan struct{}),
		closeOnce:      &sync.Once{},
		done:           make(chan struct{}),
		logger:         obs.Nop(),
		confirmTimeout: DefaultConfirmTimeout,
		health:         HealthStateConnected,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(publisher)
		}
	}

	publisher.logDeferredOptionWarnings()

	publisher.startCloseMonitor(closeNotify)

	return publisher, nil
}

// startCloseMonitor launches a goroutine that watches channel close events.
func (pub *ConfirmablePublisher) startCloseMonitor(closeNotify chan *amqp.Error) {
	monitorDone := pub.done
	monitorLogger := pub.logger

	runtime.SafeGo(monitorLogger, "confirmable-publisher-close-monitor", runtime.KeepRunning, func() {
		select {
		case amqpErr := <-closeNotify:
			pub.handleMonitoredClose(amqpErr)
		case <-monitorDone:
			return
		}
	})
}

func (pub *ConfirmablePublisher) handleMonitoredClose(amqpErr *amqp.Error) {
	pub.mu.Lock()
	pub.ensureCloseSignalsLocked()
	monitorCloseOnce := pub.closeOnce
	monitorClosedCh := pub.closedCh
	hasRecovery := pub.recovery != nil && pub.recovery.provider != nil
	pub.closed = true
	pub.mu.Unlock()

	monitorCloseOnce.Do(func() { close(monitorClosedCh) })

	if hasRecovery {
		pub.attemptAutoRecovery(amqpErr)

		return
	}

	pub.emitHealthState(HealthStateDisconnected)
}

func (pub *ConfirmablePublisher) attemptAutoRecovery(amqpErr *amqp.Error) {
	pub.mu.RLock()
	recovery := pub.recovery
	logger := pub.logger
	pub.mu.RUnlock()

	if recovery == nil || recovery.provider == nil {
		return
	}

	pub.emitHealthState(HealthStateReconnecting)
	pub.logChannelClosed(logger, amqpErr, recovery.maxAttempts)

	if !pub.prepareForRecovery() {
		logIfConfigured(logger, obs.LevelInfo, "rabbitmq: recovery aborted, publisher is shutting down")
		pub.emitHealthState(HealthStateDisconnected)

		return
	}

	pub.mu.RLock()
	recoveryStop := pub.done
	pub.mu.RUnlock()

	for attempt := range recovery.maxAttempts {
		result := pub.executeRecoveryAttempt(recovery, logger, recoveryStop, attempt)
		if result == recoveryAttemptSuccess || result == recoveryAttemptAborted {
			return
		}
	}

	logIfConfigured(
		logger,
		obs.LevelError,
		fmt.Sprintf("rabbitmq: auto-recovery failed after %d attempts, publisher is disconnected", recovery.maxAttempts),
	)

	pub.mu.Lock()
	pub.recoveryExhausted = true
	pub.mu.Unlock()

	pub.emitHealthState(HealthStateDisconnected)
}

func (pub *ConfirmablePublisher) logChannelClosed(logger obs.Logger, amqpErr *amqp.Error, maxAttempts int) {
	if nilcheck.Interface(logger) {
		return
	}

	errMsg := "unknown"
	if amqpErr != nil {
		errMsg = sanitizeAMQPErr(amqpErr, "")
	}

	logger.Log(context.Background(), obs.LevelWarn,
		fmt.Sprintf("rabbitmq: channel closed (%s), starting auto-recovery (max %d attempts)", errMsg, maxAttempts))
}

func (pub *ConfirmablePublisher) executeRecoveryAttempt(
	recovery *recoveryConfig,
	logger obs.Logger,
	recoveryStop <-chan struct{},
	attempt int,
) recoveryAttemptResult {
	select {
	case <-recoveryStop:
		logIfConfigured(logger, obs.LevelInfo, "rabbitmq: recovery aborted (publisher closed externally)")
		pub.emitHealthState(HealthStateDisconnected)

		return recoveryAttemptAborted
	default:
	}

	if aborted := pub.waitRecoveryBackoff(recovery, logger, recoveryStop, attempt); aborted {
		return recoveryAttemptAborted
	}

	return pub.tryReconnectChannel(recovery, logger, attempt)
}

func (pub *ConfirmablePublisher) waitRecoveryBackoff(
	recovery *recoveryConfig,
	logger obs.Logger,
	recoveryStop <-chan struct{},
	attempt int,
) bool {
	delay := backoff.ExponentialWithJitter(recovery.backoffInitial, attempt)
	if delay > recovery.backoffMax {
		delay = backoff.FullJitter(recovery.backoffMax)
	}

	logIfConfigured(
		logger,
		obs.LevelInfo,
		fmt.Sprintf("rabbitmq: recovery attempt %d/%d, backoff %v", attempt+1, recovery.maxAttempts, delay),
	)

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return false
	case <-recoveryStop:
		logIfConfigured(logger, obs.LevelInfo, "rabbitmq: recovery aborted during backoff (publisher closed)")
		pub.emitHealthState(HealthStateDisconnected)

		return true
	}
}

func (pub *ConfirmablePublisher) tryReconnectChannel(
	recovery *recoveryConfig,
	logger obs.Logger,
	attempt int,
) recoveryAttemptResult {
	newCh, err := recovery.provider()
	if err != nil {
		sanitizedErr := sanitizeAMQPErr(err, "")
		logIfConfigured(
			logger,
			obs.LevelWarn,
			fmt.Sprintf("rabbitmq: recovery attempt %d/%d failed: %s", attempt+1, recovery.maxAttempts, sanitizedErr),
		)

		return recoveryAttemptRetry
	}

	if err := pub.Reconnect(newCh); err != nil {
		sanitizedErr := sanitizeAMQPErr(err, "")
		logIfConfigured(
			logger,
			obs.LevelWarn,
			fmt.Sprintf("rabbitmq: recovery attempt %d/%d reconnect failed: %s", attempt+1, recovery.maxAttempts, sanitizedErr),
		)

		if !nilcheck.Interface(newCh) {
			_ = newCh.Close()
		}

		return recoveryAttemptRetry
	}

	logIfConfigured(
		logger,
		obs.LevelInfo,
		fmt.Sprintf("rabbitmq: auto-recovery succeeded on attempt %d/%d", attempt+1, recovery.maxAttempts),
	)

	pub.emitHealthState(HealthStateConnected)

	return recoveryAttemptSuccess
}

func (pub *ConfirmablePublisher) prepareForRecovery() bool {
	pub.publishMu.Lock()
	defer pub.publishMu.Unlock()

	pub.mu.Lock()
	if pub.shutdown {
		pub.mu.Unlock()

		return false
	}

	currentCh := pub.ch
	confirms := pub.confirms
	confirmTimeout := pub.confirmTimeout
	pub.ensureCloseSignalsLocked()

	pub.closed = true
	pub.recoveryExhausted = false
	pub.ch = nil
	safeCloseSignal(pub.done)
	pub.closeOnce.Do(func() { close(pub.closedCh) })
	pub.mu.Unlock()

	if !nilcheck.Interface(currentCh) {
		_ = currentCh.Close()
	}

	drainConfirms(confirms, confirmTimeout)

	pub.mu.Lock()
	pub.done = make(chan struct{})
	pub.mu.Unlock()

	return true
}

func (pub *ConfirmablePublisher) emitHealthState(state HealthState) {
	pub.mu.Lock()
	pub.health = state
	recovery := pub.recovery
	pub.mu.Unlock()

	if recovery == nil || recovery.healthCallback == nil {
		return
	}

	recovery.healthCallback(state)
}

// Publish sends a message and waits for broker confirmation.
//
// This method is intentionally serialized per publisher instance: only one
// publish+confirm flow is in-flight at a time. For explicit naming, prefer
// PublishAndWaitConfirm. For higher throughput, shard publishing across
// multiple publisher instances.
// Publish sends a message and waits for the broker to confirm it was both
// accepted and routed.
//
// The mandatory argument is ignored: every publish is mandatory. See
// PublishAndWaitConfirm for why routability cannot be opted out of.
func (pub *ConfirmablePublisher) Publish(
	ctx context.Context,
	exchange, routingKey string,
	_, immediate bool,
	msg amqp.Publishing,
) error {
	if pub == nil {
		return ErrPublisherRequired
	}

	return pub.PublishAndWaitConfirm(ctx, exchange, routingKey, true, immediate, msg)
}

// PublishAndWaitConfirm sends a message and synchronously waits for the broker
// to confirm it was accepted AND routed to at least one queue.
//
// Calls are serialized per publisher instance to preserve confirm ordering
// without delivery-tag correlation state.
//
// Every publish is mandatory and the mandatory argument is ignored. A broker
// ACKs a message it discarded because no queue was bound to the routing key,
// so waiting on the confirmation alone reports success for a message nobody
// received. Such a message now fails with ErrPublishReturned. There is no
// option to restore the silent behaviour: a caller that cannot tolerate the
// error is a caller publishing into a void.
//
// A returned error means the message is not safely delivered; the caller must
// retry it or persist it, never mark it done.
func (pub *ConfirmablePublisher) PublishAndWaitConfirm(
	ctx context.Context,
	exchange, routingKey string,
	_, immediate bool,
	msg amqp.Publishing,
) error {
	if pub == nil {
		return ErrPublisherRequired
	}

	if ctx == nil {
		ctx = context.Background()
	}

	pub.publishMu.Lock()
	defer pub.publishMu.Unlock()

	pub.mu.RLock()

	if pub.closed {
		recoveryExhausted := pub.recoveryExhausted
		pub.mu.RUnlock()

		if recoveryExhausted {
			return fmt.Errorf("%w: %w", ErrPublisherClosed, ErrRecoveryExhausted)
		}

		return ErrPublisherClosed
	}

	if pub.ch == nil {
		pub.mu.RUnlock()
		return ErrPublisherNotReady
	}

	publishChannel := pub.ch
	confirms := pub.confirms
	returns := pub.returns
	closedCh := pub.closedCh
	confirmTimeout := pub.confirmTimeout
	pub.mu.RUnlock()

	// A return left behind by an aborted publish would otherwise be blamed on
	// this message.
	drainReturns(returns)

	if err := publishChannel.PublishWithContext(ctx, exchange, routingKey, true, immediate, msg); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	err := waitForConfirm(ctx, confirms, closedCh, confirmTimeout)
	if err != nil {
		if isConfirmStreamCorrupted(err) {
			// The pending confirmation will corrupt the next waitForConfirm
			// call. Invalidate the channel so the close monitor triggers
			// auto-recovery after publishMu is released by the deferred
			// unlock above.
			pub.invalidateChannel(publishChannel)
		}

		return err
	}

	// The broker acknowledged the message. It returned it first if no queue
	// was bound to the routing key, which makes the ACK a receipt for a
	// discard rather than for a delivery.
	if ret, ok := takeReturn(returns); ok {
		return fmt.Errorf("%w: exchange=%q routing_key=%q code=%d reason=%q",
			ErrPublishReturned, ret.Exchange, ret.RoutingKey, ret.ReplyCode, ret.ReplyText)
	}

	return nil
}

// isConfirmStreamCorrupted reports whether the error indicates the
// confirmation channel has a stale entry that would desynchronize the
// next waitForConfirm call.
func isConfirmStreamCorrupted(err error) bool {
	return errors.Is(err, ErrConfirmTimeout) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// invalidateChannel marks the publisher as closed and closes the
// underlying AMQP channel. The close event propagates to the close
// monitor goroutine which initiates auto-recovery (if configured)
// after the caller releases publishMu.
//
// The publisher transitions to HealthStateDegraded to signal that the
// confirmation stream is corrupted but recovery may restore it. If
// auto-recovery is not configured, callers should call Reconnect()
// with a fresh channel to restore the publisher.
//
// Must be called while holding publishMu.
func (pub *ConfirmablePublisher) invalidateChannel(ch ConfirmableChannel) {
	pub.mu.Lock()
	pub.ensureCloseSignalsLocked()
	pub.closed = true
	pub.ch = nil
	pub.mu.Unlock()

	pub.emitHealthState(HealthStateDegraded)

	pub.closeOnce.Do(func() { close(pub.closedCh) })

	if !nilcheck.Interface(ch) {
		_ = ch.Close()
	}
}

func waitForConfirm(
	ctx context.Context,
	confirms <-chan amqp.Confirmation,
	closedCh <-chan struct{},
	confirmTimeout time.Duration,
) error {
	timeout := time.NewTimer(confirmTimeout)
	defer timeout.Stop()

	select {
	case confirmed, ok := <-confirms:
		if !ok {
			return ErrPublisherClosed
		}

		if !confirmed.Ack {
			return fmt.Errorf("%w: delivery_tag=%d", ErrPublishNacked, confirmed.DeliveryTag)
		}

		return nil

	case <-closedCh:
		return ErrPublisherClosed

	case <-timeout.C:
		return ErrConfirmTimeout

	case <-ctx.Done():
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	}
}

// Close drains pending confirmations and permanently closes the publisher.
// After Close, Reconnect is rejected and callers should create a new publisher.
func (pub *ConfirmablePublisher) Close() error {
	if pub == nil {
		return ErrPublisherRequired
	}

	pub.publishMu.Lock()
	defer pub.publishMu.Unlock()

	pub.mu.Lock()
	pub.ensureCloseSignalsLocked()

	if pub.shutdown {
		pub.mu.Unlock()

		return nil
	}

	pub.shutdown = true
	pub.closed = true
	pub.recoveryExhausted = false
	currentCh := pub.ch
	safeCloseSignal(pub.done)
	pub.closeOnce.Do(func() { close(pub.closedCh) })
	pub.mu.Unlock()

	if !nilcheck.Interface(currentCh) {
		if err := currentCh.Close(); err != nil {
			return fmt.Errorf("closing publisher channel: %w", err)
		}
	}

	drainConfirms(pub.confirms, pub.confirmTimeout)
	pub.emitHealthState(HealthStateDisconnected)

	return nil
}

// Reconnect replaces the underlying AMQP channel with a fresh one.
//
// Caller contract:
//   - Reconnect is only valid after an operational close (for example, auto-recovery
//     transition) when publisher.closed is true and publisher.shutdown is false.
//   - After explicit Close, the publisher enters terminal shutdown and Reconnect
//     returns ErrReconnectAfterClose.
//
// Reconnect replaces the underlying AMQP channel with a fresh one.
//
// Caller contract:
//   - Reconnect is only valid after an operational close (for example, auto-recovery
//     transition) when publisher.closed is true and publisher.shutdown is false.
//   - After explicit Close, the publisher enters terminal shutdown and Reconnect
//     returns ErrReconnectAfterClose.
//   - On success, the publisher transitions to HealthStateConnected and the
//     health callback is invoked.
func (pub *ConfirmablePublisher) Reconnect(ch ConfirmableChannel) error {
	if pub == nil {
		return ErrPublisherRequired
	}

	if nilcheck.Interface(ch) {
		return ErrChannelRequired
	}

	pub.publishMu.Lock()
	defer pub.publishMu.Unlock()

	var healthCallback HealthCallback

	pub.mu.Lock()

	if !pub.closed {
		pub.mu.Unlock()

		return ErrReconnectWhileOpen
	}

	if pub.shutdown {
		pub.mu.Unlock()

		return ErrReconnectAfterClose
	}

	notifier, err := returnNotifierFor(ch)
	if err != nil {
		pub.mu.Unlock()

		return err
	}

	if err := ch.Confirm(false); err != nil {
		pub.mu.Unlock()

		return fmt.Errorf("%w: %w", ErrConfirmModeUnavailable, err)
	}

	confirms := make(chan amqp.Confirmation, confirmChannelBuffer)
	ch.NotifyPublish(confirms)

	returns := registerReturnListener(notifier)

	closeNotify := ch.NotifyClose(make(chan *amqp.Error, 1))

	pub.ch = ch
	pub.confirms = confirms
	pub.returns = returns
	pub.closedCh = make(chan struct{})

	pub.closeOnce = &sync.Once{}
	if pub.done == nil {
		pub.done = make(chan struct{})
	}

	pub.closed = false
	pub.recoveryExhausted = false
	pub.health = HealthStateConnected

	if pub.recovery != nil {
		healthCallback = pub.recovery.healthCallback
	}

	pub.startCloseMonitor(closeNotify)

	pub.mu.Unlock()

	// Emit health callback outside the lock to avoid deadlock with caller callbacks.
	if healthCallback != nil {
		healthCallback(HealthStateConnected)
	}

	return nil
}

// Channel returns the underlying channel for low-level operations.
//
// The return value can be nil when the publisher is closed, reconnecting,
// or not yet initialized. Call ChannelOrError when callers need explicit
// readiness errors.
func (pub *ConfirmablePublisher) Channel() ConfirmableChannel {
	if pub == nil {
		return nil
	}

	pub.mu.RLock()
	defer pub.mu.RUnlock()

	if pub.closed {
		return nil
	}

	return pub.ch
}

// ChannelOrError returns the underlying channel only when the publisher is ready.
func (pub *ConfirmablePublisher) ChannelOrError() (ConfirmableChannel, error) {
	if pub == nil {
		return nil, ErrPublisherRequired
	}

	pub.mu.RLock()
	defer pub.mu.RUnlock()

	if pub.closed {
		return nil, ErrPublisherClosed
	}

	if pub.ch == nil {
		return nil, ErrPublisherNotReady
	}

	return pub.ch, nil
}

// HealthState returns the latest synchronous health state snapshot.
func (pub *ConfirmablePublisher) HealthState() HealthState {
	if pub == nil {
		return HealthStateDisconnected
	}

	pub.mu.RLock()
	defer pub.mu.RUnlock()

	return pub.health
}

func ensureRecoveryConfig(pub *ConfirmablePublisher) {
	if pub.recovery != nil {
		return
	}

	pub.recovery = &recoveryConfig{
		maxAttempts:    DefaultMaxRecoveryAttempts,
		backoffInitial: DefaultRecoveryBackoffInitial,
		backoffMax:     DefaultRecoveryBackoffMax,
	}
}

func (pub *ConfirmablePublisher) logDeferredOptionWarnings() {
	if !pub.invalidConfirmTimeout.set {
		return
	}

	logIfConfigured(pub.logger, obs.LevelWarn,
		fmt.Sprintf("rabbitmq: ignoring invalid confirm timeout %v, using default", pub.invalidConfirmTimeout.value))
}

func (pub *ConfirmablePublisher) ensureCloseSignalsLocked() {
	if pub.closeOnce == nil {
		pub.closeOnce = &sync.Once{}
	}

	if pub.closedCh == nil {
		pub.closedCh = make(chan struct{})
	}
}

func safeCloseSignal(ch chan struct{}) {
	if ch == nil {
		return
	}

	select {
	case <-ch:
		return
	default:
		close(ch)
	}
}

func drainConfirms(confirms <-chan amqp.Confirmation, timeout time.Duration) {
	if confirms == nil {
		return
	}

	if timeout <= 0 {
		timeout = DefaultConfirmTimeout
	}

	grace := time.NewTimer(timeout)
	defer grace.Stop()

	for {
		select {
		case _, ok := <-confirms:
			if !ok {
				return
			}
		case <-grace.C:
			return
		}
	}
}

func logIfConfigured(logger obs.Logger, level int, message string) {
	if nilcheck.Interface(logger) {
		return
	}

	logger.Log(context.Background(), level, message)
}
