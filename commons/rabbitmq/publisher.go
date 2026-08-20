package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/backoff"
	"github.com/LerianStudio/lib-commons/v6/commons/internal/nilcheck"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
	"github.com/LerianStudio/lib-observability/v2/runtime"
	v3messagingobs "github.com/LerianStudio/lib-observability/v3/messagingobs"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
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
	ErrConnectionRequired     = ErrNilConnection
	ErrPublisherRequired      = errors.New("confirmable publisher is required")
	ErrChannelRequired        = errors.New("rabbitmq channel is required")
	ErrPublisherNotReady      = errors.New("confirmable publisher not initialized")
	ErrConfirmModeUnavailable = errors.New("channel does not support confirm mode")
	ErrPublishNacked          = errors.New("message was nacked by broker")
	ErrConfirmTimeout         = errors.New("confirmation timed out")
	ErrPublisherClosed        = errors.New("publisher is closed")
	ErrReconnectAfterClose    = errors.New("cannot reconnect: publisher was explicitly closed")
	ErrReconnectWhileOpen     = errors.New("cannot reconnect: publisher is still open, call Close first")
	ErrRecoveryExhausted      = errors.New("automatic recovery exhausted all attempts")
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

	// produceOperationName is the messaging.operation.name reported for every
	// publish. It is a constant on purpose: the label set of
	// messaging.client.operation.duration must stay bounded, so nothing derived
	// from the message may reach it.
	produceOperationName = "publish"

	// producerLibraryName is the instrumentation scope reported on the producer
	// span and duration metric, so the series is attributable to this package.
	producerLibraryName = "lib-commons/rabbitmq"
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

// ConfirmablePublisher wraps an AMQP channel with publisher confirms enabled.
type ConfirmablePublisher struct {
	ch                    ConfirmableChannel
	confirms              chan amqp.Confirmation
	closedCh              chan struct{}
	closeOnce             *sync.Once
	done                  chan struct{}
	logger                libLog.Logger
	confirmTimeout        time.Duration
	invalidConfirmTimeout struct {
		set   bool
		value time.Duration
	}
	recovery          *recoveryConfig
	producer          *v3messagingobs.Publisher
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
func WithLogger(logger libLog.Logger) ConfirmablePublisherOption {
	return func(pub *ConfirmablePublisher) {
		if nilcheck.Interface(logger) {
			return
		}

		pub.logger = logger
	}
}

// WithTelemetryProviders binds explicit OpenTelemetry providers to this
// publisher's producer instrumentation, overriding the global providers used by
// default.
//
// It is NOT required. The publisher is instrumented unconditionally against the
// OTel globals, so a service that installed its providers at bootstrap
// (lib-observability Telemetry.ApplyGlobals) starts emitting the producer span,
// the trace-context headers and messaging.client.operation.duration by bumping
// lib-commons alone, with no change here. This option exists for the service
// that deliberately keeps its providers off the globals.
//
// The parameters are the OpenTelemetry core interfaces, not lib-observability
// types, so a consumer on either lib-observability major can pass its own
// providers. Nil values are ignored and leave the global default in place.
//
// The instrument is built once, here at construction, and never reassigned
// afterwards — not even by Reconnect, since it binds to the providers and not to
// the AMQP channel. That immutability is what allows PublishAndWaitConfirm to
// read it without holding a lock.
func WithTelemetryProviders(mp metric.MeterProvider, tp trace.TracerProvider) ConfirmablePublisherOption {
	return func(pub *ConfirmablePublisher) {
		if mp == nil && tp == nil {
			return
		}

		pub.producer = newProducerInstrument(
			v3messagingobs.WithMeterProvider(mp),
			v3messagingobs.WithTracerProvider(tp),
		)
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
				libLog.LevelWarn,
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

	if err := ch.Confirm(false); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfirmModeUnavailable, err)
	}

	confirms := make(chan amqp.Confirmation, confirmChannelBuffer)
	ch.NotifyPublish(confirms)

	closeNotify := ch.NotifyClose(make(chan *amqp.Error, 1))

	publisher := &ConfirmablePublisher{
		ch:             ch,
		confirms:       confirms,
		closedCh:       make(chan struct{}),
		closeOnce:      &sync.Once{},
		done:           make(chan struct{}),
		logger:         libLog.NewNop(),
		confirmTimeout: DefaultConfirmTimeout,
		health:         HealthStateConnected,
		producer:       newProducerInstrument(),
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
		logIfConfigured(logger, libLog.LevelInfo, "rabbitmq: recovery aborted, publisher is shutting down")
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
		libLog.LevelError,
		fmt.Sprintf("rabbitmq: auto-recovery failed after %d attempts, publisher is disconnected", recovery.maxAttempts),
	)

	pub.mu.Lock()
	pub.recoveryExhausted = true
	pub.mu.Unlock()

	pub.emitHealthState(HealthStateDisconnected)
}

func (pub *ConfirmablePublisher) logChannelClosed(logger libLog.Logger, amqpErr *amqp.Error, maxAttempts int) {
	if nilcheck.Interface(logger) {
		return
	}

	errMsg := "unknown"
	if amqpErr != nil {
		errMsg = sanitizeAMQPErr(amqpErr, "")
	}

	logger.Log(context.Background(), libLog.LevelWarn,
		fmt.Sprintf("rabbitmq: channel closed (%s), starting auto-recovery (max %d attempts)", errMsg, maxAttempts))
}

func (pub *ConfirmablePublisher) executeRecoveryAttempt(
	recovery *recoveryConfig,
	logger libLog.Logger,
	recoveryStop <-chan struct{},
	attempt int,
) recoveryAttemptResult {
	select {
	case <-recoveryStop:
		logIfConfigured(logger, libLog.LevelInfo, "rabbitmq: recovery aborted (publisher closed externally)")
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
	logger libLog.Logger,
	recoveryStop <-chan struct{},
	attempt int,
) bool {
	delay := backoff.ExponentialWithJitter(recovery.backoffInitial, attempt)
	if delay > recovery.backoffMax {
		delay = backoff.FullJitter(recovery.backoffMax)
	}

	logIfConfigured(
		logger,
		libLog.LevelInfo,
		fmt.Sprintf("rabbitmq: recovery attempt %d/%d, backoff %v", attempt+1, recovery.maxAttempts, delay),
	)

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return false
	case <-recoveryStop:
		logIfConfigured(logger, libLog.LevelInfo, "rabbitmq: recovery aborted during backoff (publisher closed)")
		pub.emitHealthState(HealthStateDisconnected)

		return true
	}
}

func (pub *ConfirmablePublisher) tryReconnectChannel(
	recovery *recoveryConfig,
	logger libLog.Logger,
	attempt int,
) recoveryAttemptResult {
	newCh, err := recovery.provider()
	if err != nil {
		sanitizedErr := sanitizeAMQPErr(err, "")
		logIfConfigured(
			logger,
			libLog.LevelWarn,
			fmt.Sprintf("rabbitmq: recovery attempt %d/%d failed: %s", attempt+1, recovery.maxAttempts, sanitizedErr),
		)

		return recoveryAttemptRetry
	}

	if err := pub.Reconnect(newCh); err != nil {
		sanitizedErr := sanitizeAMQPErr(err, "")
		logIfConfigured(
			logger,
			libLog.LevelWarn,
			fmt.Sprintf("rabbitmq: recovery attempt %d/%d reconnect failed: %s", attempt+1, recovery.maxAttempts, sanitizedErr),
		)

		if !nilcheck.Interface(newCh) {
			_ = newCh.Close()
		}

		return recoveryAttemptRetry
	}

	logIfConfigured(
		logger,
		libLog.LevelInfo,
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
func (pub *ConfirmablePublisher) Publish(
	ctx context.Context,
	exchange, routingKey string,
	mandatory, immediate bool,
	msg amqp.Publishing,
) error {
	if pub == nil {
		return ErrPublisherRequired
	}

	return pub.PublishAndWaitConfirm(ctx, exchange, routingKey, mandatory, immediate, msg)
}

// PublishAndWaitConfirm sends a message and synchronously waits for broker confirmation.
//
// Calls are serialized per publisher instance to preserve confirm ordering
// without delivery-tag correlation state.
func (pub *ConfirmablePublisher) PublishAndWaitConfirm(
	ctx context.Context,
	exchange, routingKey string,
	mandatory, immediate bool,
	msg amqp.Publishing,
) (err error) {
	if pub == nil {
		return ErrPublisherRequired
	}

	if ctx == nil {
		ctx = context.Background()
	}

	// Telemetry opens before the serialization lock so the recorded duration is
	// the latency the caller actually observes, queueing behind other publishers
	// included. finish is deferred first and therefore runs LAST (defers are
	// LIFO), after publishMu is released, and it runs on every return path —
	// including the closed/not-ready early returns below, which are real publish
	// failures and must be counted as such.
	ctx, finish := pub.startProduce(ctx, exchange, routingKey, &msg)
	defer func() { finish(err) }()

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
	closedCh := pub.closedCh
	confirmTimeout := pub.confirmTimeout
	pub.mu.RUnlock()

	if err := publishChannel.PublishWithContext(ctx, exchange, routingKey, mandatory, immediate, msg); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	err = waitForConfirm(ctx, confirms, closedCh, confirmTimeout)
	if err != nil && isConfirmStreamCorrupted(err) {
		// The pending confirmation will corrupt the next waitForConfirm call.
		// Invalidate the channel so the close monitor triggers auto-recovery
		// after publishMu is released by the deferred unlock above.
		pub.invalidateChannel(publishChannel)
	}

	return err
}

// startProduce opens producer telemetry for a single publish and stamps the
// outgoing message with the trace context, returning the span-bearing context
// and the FinishFunc that closes the operation.
//
// The producer is always present, bound to the OTel globals unless
// WithTelemetryProviders overrode them. Until the service installs its
// providers those globals are no-op implementations: no span is recorded, the
// injected header map comes back empty and msg is therefore left untouched, so
// the publish path stays what it was before this instrumentation existed.
//
// Reading pub.producer without a lock is safe: it is written once, in the
// constructor and while the options are applied, and never reassigned.
//
// Cardinality guardrail: the destination template is the EXCHANGE, which is a
// bounded set fixed by the service's topology. The routing key is unbounded
// (it routinely carries ids or tenants), so it is passed in the field
// messagingobs deliberately never emits as a label, purely for the caller's own
// span/log use. Nothing else is added here.
func (pub *ConfirmablePublisher) startProduce(
	ctx context.Context,
	exchange, routingKey string,
	msg *amqp.Publishing,
) (context.Context, v3messagingobs.FinishFunc) {
	ctx, headers, finish := pub.producer.Produce(ctx, v3messagingobs.ProduceParams{
		DestinationTemplate: exchange,
		OperationName:       produceOperationName,
		RoutingKey:          routingKey,
		MessageID:           msg.MessageId,
	})

	// Leave the caller's map alone when the propagator produced nothing, so the
	// uninstrumented path allocates no table.
	if len(headers) > 0 {
		msg.Headers = mergeTraceHeaders(msg.Headers, headers)
	}

	return ctx, finish
}

// newProducerInstrument builds the messagingobs producer for this package's
// instrumentation scope. Options resolve over the OTel globals, which is what
// makes the instrumentation arrive on a lib-commons bump alone.
func newProducerInstrument(opts ...v3messagingobs.Option) *v3messagingobs.Publisher {
	return v3messagingobs.NewPublisherWithOptions(
		append([]v3messagingobs.Option{v3messagingobs.WithLibraryName(producerLibraryName)}, opts...)...,
	)
}

// mergeTraceHeaders returns the publishing headers carrying the injected trace
// context on top of whatever the caller already set.
//
// It copies instead of writing into existing because that map belongs to the
// caller's amqp.Publishing: amqp.Publishing is passed by value but its Headers
// are a reference, so writing in place would mutate the caller's message (and
// race with a caller reusing one Publishing across goroutines). Injected keys
// win on collision — a traceparent left over from an earlier hop is stale by
// definition and must not survive into this publish.
//
// The collision check is case-INSENSITIVE. The W3C propagator writes through an
// http.Header carrier, which canonicalizes "traceparent" into "Traceparent",
// while a caller (or an upstream hop that copied a Delivery's headers) may carry
// the lowercase spelling. Copying both would ship two conflicting trace contexts
// and let the consumer join the stale one.
func mergeTraceHeaders(existing amqp.Table, injected map[string]any) amqp.Table {
	if len(injected) == 0 {
		return existing
	}

	merged := make(amqp.Table, len(existing)+len(injected))

	for name, value := range existing {
		if !collidesWithInjected(name, injected) {
			merged[name] = value
		}
	}

	maps.Copy(merged, injected)

	return merged
}

// collidesWithInjected reports whether a caller header name matches an injected
// one under case folding.
func collidesWithInjected(name string, injected map[string]any) bool {
	for injectedName := range injected {
		if strings.EqualFold(name, injectedName) {
			return true
		}
	}

	return false
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

	if err := ch.Confirm(false); err != nil {
		pub.mu.Unlock()

		return fmt.Errorf("%w: %w", ErrConfirmModeUnavailable, err)
	}

	confirms := make(chan amqp.Confirmation, confirmChannelBuffer)
	ch.NotifyPublish(confirms)

	closeNotify := ch.NotifyClose(make(chan *amqp.Error, 1))

	pub.ch = ch
	pub.confirms = confirms
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

	logIfConfigured(pub.logger, libLog.LevelWarn,
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

func logIfConfigured(logger libLog.Logger, level libLog.Level, message string) {
	if nilcheck.Interface(logger) {
		return
	}

	logger.Log(context.Background(), level, message)
}
