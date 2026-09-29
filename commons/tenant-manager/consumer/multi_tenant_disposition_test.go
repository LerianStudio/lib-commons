//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v7/commons/rabbitmq"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
)

// fakePublisher records confirmed publishes and answers each with the next
// queued error (nil once the queue is exhausted).
type fakePublisher struct {
	mu        sync.Mutex
	calls     []publishCall
	errs      []error
	closeCall int
}

type publishCall struct {
	exchange   string
	routingKey string
	msg        amqp.Publishing
}

func (p *fakePublisher) PublishAndWaitConfirm(_ context.Context, exchange, routingKey string, _, _ bool, msg amqp.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls = append(p.calls, publishCall{exchange: exchange, routingKey: routingKey, msg: msg})

	if len(p.errs) == 0 {
		return nil
	}

	err := p.errs[0]
	p.errs = p.errs[1:]

	return err
}

func (p *fakePublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closeCall++

	return nil
}

func (p *fakePublisher) published() []publishCall {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]publishCall(nil), p.calls...)
}

// recordingWait records every requested delay and answers with err.
type recordingWait struct {
	mu     sync.Mutex
	delays []time.Duration
	err    error
}

func (w *recordingWait) wait(_ context.Context, d time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.delays = append(w.delays, d)

	return w.err
}

func testPolicy() QueuePolicy {
	return QueuePolicy{
		Retry:      RetryPolicy{MaxAttempts: 3, InitialDelay: 100 * time.Millisecond, MaxDelay: time.Second},
		DeadLetter: DeadLetterRoute{Exchange: "dlx", RoutingKey: "orders.dead"},
	}
}

func newTestRuntime(t *testing.T, policy QueuePolicy, pub *fakePublisher, w *recordingWait) *dispositionRuntime {
	t.Helper()

	normalized, err := normalizeQueuePolicy(policy)
	require.NoError(t, err)

	// A nil publisher stays an untyped nil: attemptConsumeConnection opens one.
	var publisher dispositionPublisher
	if pub != nil {
		publisher = pub
	}

	return newDispositionRuntime("orders", normalized, publisher, w.wait)
}

func failing(err error) HandlerFunc {
	return func(context.Context, amqp.Delivery) error { return err }
}

func deliveryWith(ack *fakeAcknowledger, headers amqp.Table) amqp.Delivery {
	return amqp.Delivery{
		Acknowledger: ack,
		DeliveryTag:  7,
		Exchange:     "orders-x",
		RoutingKey:   "orders.created",
		Headers:      headers,
		Body:         []byte(`{"id":1}`),
		ContentType:  "application/json",
		MessageId:    "m-1",
		UserId:       "producer",
	}
}

func runDisposition(t *testing.T, rt *dispositionRuntime, handler HandlerFunc, msg amqp.Delivery) bool {
	t.Helper()

	c := &MultiTenantConsumer{}

	return c.handleWithDisposition(context.Background(), "tenant-a", handler, rt, msg, logcompat.New(testutil.NewMockLogger())) == nil
}

func TestFakeAcknowledger_RecordsRequeueArgument(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	require.NoError(t, ack.Nack(1, false, false))
	assert.False(t, ack.requeue, "fake must record the requeue argument, not assume true")
}

func TestHandleMessage_LegacyRegisterIgnoresDisposition(t *testing.T) {
	t.Parallel()

	for _, wrap := range []func(error) error{DeadLetter, Ack, RetryLater} {
		ack := &fakeAcknowledger{}
		c := &MultiTenantConsumer{}

		c.handleMessage(context.Background(), "tenant-a", "orders", failing(wrap(errors.New("refused"))),
			deliveryWith(ack, amqp.Table{}), logcompat.New(testutil.NewMockLogger()))

		assert.Equal(t, 1, ack.nackCalls)
		assert.True(t, ack.requeue, "a queue registered with Register keeps Nack(requeue=true)")
		assert.Equal(t, 0, ack.ackCalls)
	}
}

func TestDispositionOf(t *testing.T) {
	t.Parallel()

	base := errors.New("base")

	tests := []struct {
		name string
		err  error
		want Disposition
	}{
		{name: "nil acks", err: nil, want: DispositionAck},
		{name: "plain error retries", err: base, want: DispositionRetry},
		{name: "retry later", err: RetryLater(base), want: DispositionRetry},
		{name: "dead letter", err: DeadLetter(base), want: DispositionDeadLetter},
		{name: "ack", err: Ack(base), want: DispositionAck},
		{name: "wrapped dead letter", err: fmt.Errorf("verify: %w", DeadLetter(base)), want: DispositionDeadLetter},
		{name: "joined dead letter", err: errors.Join(base, DeadLetter(base)), want: DispositionDeadLetter},
		{name: "dead letter of nil", err: DeadLetter(nil), want: DispositionDeadLetter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, DispositionOf(tt.err))
		})
	}

	assert.ErrorIs(t, DeadLetter(base), base, "the wrapper keeps errors.Is")
	assert.ErrorIs(t, fmt.Errorf("x: %w", RetryLater(base)), base)
	assert.Contains(t, DeadLetter(base).Error(), "base")
	assert.NotEmpty(t, RetryLater(nil).Error())
	assert.Equal(t, "dead-letter", DispositionDeadLetter.String())
	assert.Equal(t, "retry", DispositionRetry.String())
	assert.Equal(t, "ack", DispositionAck.String())
	assert.Equal(t, "unknown", Disposition(0).String())
}

func TestDeliveryAttempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  int
	}{
		{name: "absent", value: nil, want: 0},
		{name: "int32", value: int32(2), want: 2},
		{name: "int64", value: int64(3), want: 3},
		{name: "int", value: 4, want: 4},
		{name: "int16", value: int16(5), want: 5},
		{name: "int8", value: int8(6), want: 6},
		{name: "uint8", value: uint8(7), want: 7},
		{name: "uint16", value: uint16(8), want: 8},
		{name: "uint32", value: uint32(9), want: 9},
		{name: "negative is zero", value: int32(-4), want: 0},
		{name: "string is not trusted", value: "3", want: 0},
		{name: "huge is clamped", value: int64(math.MaxInt64), want: math.MaxInt32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			headers := amqp.Table{}
			if tt.value != nil {
				headers[HeaderRetryAttempt] = tt.value
			}

			assert.Equal(t, tt.want, DeliveryAttempt(amqp.Delivery{Headers: headers}))
		})
	}
}

func TestDisposition_RetryRepublishesAndAcksAfterConfirm(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	pub := &fakePublisher{}
	w := &recordingWait{}
	rt := newTestRuntime(t, testPolicy(), pub, w)

	keep := runDisposition(t, rt, failing(errors.New("db down")), deliveryWith(ack, amqp.Table{"trace": "x"}))

	require.True(t, keep)
	require.Len(t, pub.published(), 1)

	call := pub.published()[0]
	assert.Equal(t, "", call.exchange, "retry goes through the default exchange")
	assert.Equal(t, "orders", call.routingKey, "retry routes back to the same queue")
	assert.Equal(t, int32(1), call.msg.Headers[HeaderRetryAttempt])
	assert.Equal(t, "orders-x", call.msg.Headers[HeaderOriginExchange])
	assert.Equal(t, "orders.created", call.msg.Headers[HeaderOriginRoutingKey])
	assert.Equal(t, "x", call.msg.Headers["trace"], "existing headers survive the retry")
	assert.Equal(t, []byte(`{"id":1}`), call.msg.Body)
	assert.Equal(t, "application/json", call.msg.ContentType)
	assert.Equal(t, "m-1", call.msg.MessageId)
	assert.Empty(t, call.msg.UserId, "user-id is dropped: the broker would refuse it from another user")

	require.Len(t, w.delays, 1)
	assert.LessOrEqual(t, w.delays[0], 100*time.Millisecond, "first retry delay is bounded by InitialDelay")

	assert.Equal(t, 1, ack.ackCalls, "the original is acked only after the confirmed republish")
	assert.Equal(t, 0, ack.nackCalls)
}

func TestDisposition_RetryDelayIsCappedByMaxDelay(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	w := &recordingWait{}
	policy := testPolicy()
	policy.Retry = RetryPolicy{MaxAttempts: 50, InitialDelay: time.Second, MaxDelay: 2 * time.Second}
	rt := newTestRuntime(t, policy, &fakePublisher{}, w)

	runDisposition(t, rt, failing(errors.New("db down")), deliveryWith(ack, amqp.Table{HeaderRetryAttempt: int32(20)}))

	require.Len(t, w.delays, 1)
	assert.LessOrEqual(t, w.delays[0], 2*time.Second)
}

func TestDisposition_RetryPublishFailureNacksWithRequeueAfterBackoff(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	pub := &fakePublisher{errs: []error{rabbitmq.ErrPublishNacked}}
	w := &recordingWait{}
	rt := newTestRuntime(t, testPolicy(), pub, w)

	keep := runDisposition(t, rt, failing(errors.New("db down")), deliveryWith(ack, amqp.Table{}))

	assert.True(t, keep, "a usable publisher keeps the consumer on its channel")
	assert.Equal(t, 0, ack.ackCalls)
	assert.Equal(t, 1, ack.nackCalls)
	assert.True(t, ack.requeue, "a failed republish must never lose the message")
	assert.Len(t, w.delays, 2, "retry delay plus a failure backoff before the requeue")
}

func TestDisposition_UnusablePublisherLeavesTheChannel(t *testing.T) {
	t.Parallel()

	for _, pubErr := range []error{rabbitmq.ErrPublisherClosed, rabbitmq.ErrConfirmTimeout, rabbitmq.ErrPublisherNotReady} {
		ack := &fakeAcknowledger{}
		pub := &fakePublisher{errs: []error{fmt.Errorf("publish: %w", pubErr)}}
		w := &recordingWait{}
		rt := newTestRuntime(t, testPolicy(), pub, w)

		keep := runDisposition(t, rt, failing(DeadLetter(errors.New("forged"))), deliveryWith(ack, amqp.Table{}))

		assert.False(t, keep, "an unusable publisher must force a reconnect: %v", pubErr)
		assert.True(t, ack.requeue)
		assert.Empty(t, w.delays, "the reconnect backoff is the wait: %v", pubErr)
		assert.Equal(t, 1, rt.publishFailures, "the failure is counted for the reconnect: %v", pubErr)
	}
}

func TestDisposition_RetryExhaustedDeadLetters(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	pub := &fakePublisher{}
	w := &recordingWait{}
	rt := newTestRuntime(t, testPolicy(), pub, w)

	keep := runDisposition(t, rt, failing(errors.New("db down")), deliveryWith(ack, amqp.Table{HeaderRetryAttempt: int32(2)}))

	require.True(t, keep)
	require.Len(t, pub.published(), 1)

	call := pub.published()[0]
	assert.Equal(t, "dlx", call.exchange)
	assert.Equal(t, "orders.dead", call.routingKey)
	assert.Equal(t, DeadLetterClassRetryExhausted, call.msg.Headers[HeaderDeadLetterClass])
	assert.Equal(t, int32(3), call.msg.Headers[HeaderDeadLetterAttempts])
	assert.Equal(t, "orders", call.msg.Headers[HeaderDeadLetterQueue])
	assert.NotContains(t, call.msg.Headers, HeaderRetryAttempt,
		"a dead-letter drops the retry counter, so a redriven message gets its full retry budget")
	assert.Empty(t, w.delays, "an exhausted message is not delayed")
	assert.Equal(t, 1, ack.ackCalls)
}

func TestDisposition_DeadLetterPublishAcks(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	pub := &fakePublisher{}
	rt := newTestRuntime(t, testPolicy(), pub, &recordingWait{})

	keep := runDisposition(t, rt, failing(DeadLetter(errors.New("forged signature: secret-detail"))), deliveryWith(ack, amqp.Table{}))

	require.True(t, keep)
	require.Len(t, pub.published(), 1)

	call := pub.published()[0]
	assert.Equal(t, DeadLetterClassHandler, call.msg.Headers[HeaderDeadLetterClass])
	assert.Equal(t, int32(1), call.msg.Headers[HeaderDeadLetterAttempts])

	for key, value := range call.msg.Headers {
		if s, ok := value.(string); ok {
			assert.NotContains(t, s, "secret-detail", "error text must never reach header %s", key)
		}
	}

	assert.Equal(t, 1, ack.ackCalls)
	assert.Equal(t, 0, ack.nackCalls)
}

func TestDisposition_DeadLetterReturnedNacksWithRequeue(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	pub := &fakePublisher{errs: []error{fmt.Errorf("%w: exchange=dlx", rabbitmq.ErrPublishReturned)}}
	w := &recordingWait{}
	rt := newTestRuntime(t, testPolicy(), pub, w)

	keep := runDisposition(t, rt, failing(DeadLetter(errors.New("forged"))), deliveryWith(ack, amqp.Table{}))

	assert.True(t, keep)
	assert.Equal(t, 0, ack.ackCalls)
	assert.Equal(t, 1, ack.nackCalls)
	assert.True(t, ack.requeue, "an unroutable dead-letter must not be discarded")
	assert.Len(t, w.delays, 1, "the requeue is backed off so a missing DLX cannot hot-loop")
}

func TestDisposition_ViaQueueDLXNacksWithoutRequeue(t *testing.T) {
	t.Parallel()

	policy := testPolicy()
	policy.DeadLetter = DeadLetterRoute{ViaQueueDLX: true}

	for _, headers := range []amqp.Table{{}, {HeaderRetryAttempt: int32(2)}} {
		ack := &fakeAcknowledger{}
		pub := &fakePublisher{}
		rt := newTestRuntime(t, policy, pub, &recordingWait{})

		handlerErr := error(DeadLetter(errors.New("forged")))
		if len(headers) > 0 {
			handlerErr = errors.New("db down")
		}

		keep := runDisposition(t, rt, failing(handlerErr), deliveryWith(ack, headers))

		assert.True(t, keep)
		assert.Empty(t, pub.published())
		assert.Equal(t, 1, ack.nackCalls)
		assert.False(t, ack.requeue)
	}
}

func TestDisposition_AckDespiteErrorAcks(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	pub := &fakePublisher{}
	rt := newTestRuntime(t, testPolicy(), pub, &recordingWait{})

	keep := runDisposition(t, rt, failing(Ack(errors.New("duplicate"))), deliveryWith(ack, amqp.Table{}))

	assert.True(t, keep)
	assert.Empty(t, pub.published())
	assert.Equal(t, 1, ack.ackCalls)
}

func TestDisposition_SuccessAcks(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	rt := newTestRuntime(t, testPolicy(), &fakePublisher{}, &recordingWait{})

	var tenantID string

	keep := runDisposition(t, rt, func(ctx context.Context, _ amqp.Delivery) error {
		tenantID = core.GetTenantIDContext(ctx)
		return nil
	}, deliveryWith(ack, amqp.Table{}))

	assert.True(t, keep)
	assert.Equal(t, "tenant-a", tenantID)
	assert.Equal(t, 1, ack.ackCalls)
}

func TestDisposition_RestoresOriginalRoutingForHandler(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	rt := newTestRuntime(t, testPolicy(), &fakePublisher{}, &recordingWait{})

	retried := amqp.Delivery{
		Acknowledger: ack,
		Exchange:     "",
		RoutingKey:   "orders",
		Headers: amqp.Table{
			HeaderRetryAttempt:     int32(1),
			HeaderOriginExchange:   "orders-x",
			HeaderOriginRoutingKey: "orders.created",
		},
	}

	var seenExchange, seenKey string

	runDisposition(t, rt, func(_ context.Context, d amqp.Delivery) error {
		seenExchange, seenKey = d.Exchange, d.RoutingKey
		return nil
	}, retried)

	assert.Equal(t, "orders-x", seenExchange)
	assert.Equal(t, "orders.created", seenKey)

	// A message that did not arrive through the library's retry hop keeps its
	// routing even when it carries origin headers.
	direct := deliveryWith(&fakeAcknowledger{}, amqp.Table{HeaderOriginExchange: "spoofed", HeaderOriginRoutingKey: "spoofed"})

	runDisposition(t, rt, func(_ context.Context, d amqp.Delivery) error {
		seenExchange, seenKey = d.Exchange, d.RoutingKey
		return nil
	}, direct)

	assert.Equal(t, "orders-x", seenExchange)
	assert.Equal(t, "orders.created", seenKey)
}

func TestDisposition_ContextCancelledDuringDelayNacksWithRequeue(t *testing.T) {
	t.Parallel()

	ack := &fakeAcknowledger{}
	pub := &fakePublisher{}
	w := &recordingWait{err: context.Canceled}
	rt := newTestRuntime(t, testPolicy(), pub, w)

	keep := runDisposition(t, rt, failing(errors.New("db down")), deliveryWith(ack, amqp.Table{}))

	assert.True(t, keep)
	assert.Empty(t, pub.published(), "nothing is republished once the wait is interrupted")
	assert.Equal(t, 1, ack.nackCalls)
	assert.True(t, ack.requeue)
}

func TestNormalizeQueuePolicy(t *testing.T) {
	t.Parallel()

	route := DeadLetterRoute{RoutingKey: "dead"}

	got, err := normalizeQueuePolicy(QueuePolicy{DeadLetter: route})
	require.NoError(t, err)
	assert.Equal(t, DefaultRetryPolicy(), got.Retry, "a zero Retry means the default policy")
	assert.Equal(t, RetryPolicy{MaxAttempts: 5, InitialDelay: time.Second, MaxDelay: 30 * time.Second}, DefaultRetryPolicy())

	got, err = normalizeQueuePolicy(QueuePolicy{DeadLetter: route, Retry: RetryPolicy{MaxAttempts: 2}})
	require.NoError(t, err)
	assert.Equal(t, 2, got.Retry.MaxAttempts)
	assert.Equal(t, time.Second, got.Retry.InitialDelay, "zero fields take the default")

	invalid := []QueuePolicy{
		{DeadLetter: DeadLetterRoute{}},
		{DeadLetter: DeadLetterRoute{Exchange: "dlx"}},
		{DeadLetter: DeadLetterRoute{ViaQueueDLX: true, RoutingKey: "dead"}},
		{DeadLetter: DeadLetterRoute{ViaQueueDLX: true, Exchange: "dlx"}},
	}

	for _, policy := range invalid {
		_, err := normalizeQueuePolicy(policy)
		assert.ErrorIs(t, err, ErrDeadLetterRouteRequired, "%+v", policy.DeadLetter)
	}

	badRetry := []RetryPolicy{
		{MaxAttempts: -1},
		{InitialDelay: -time.Second},
		{MaxDelay: -time.Second},
		{InitialDelay: 2 * time.Second, MaxDelay: time.Second},
	}

	for _, retry := range badRetry {
		_, err := normalizeQueuePolicy(QueuePolicy{DeadLetter: route, Retry: retry})
		assert.ErrorIs(t, err, ErrInvalidRetryPolicy, "%+v", retry)
	}
}

func TestRegisterQueue(t *testing.T) {
	t.Parallel()

	var nilConsumer *MultiTenantConsumer
	assert.ErrorIs(t, nilConsumer.RegisterQueue("q", failing(nil), testPolicy()), ErrNilConsumer)

	assert.Error(t, (&MultiTenantConsumer{}).RegisterQueue("q", failing(nil), testPolicy()), "RabbitMQ is required")

	c := &MultiTenantConsumer{rabbitmq: dummyRabbitMQManager(), logger: logcompat.New(testutil.NewMockLogger())}

	assert.ErrorIs(t, c.RegisterQueue("q", nil, testPolicy()), core.ErrNilHandlerFunc)
	assert.ErrorIs(t, c.RegisterQueue("q", failing(nil), QueuePolicy{}), ErrDeadLetterRouteRequired)

	require.NoError(t, c.RegisterQueue("q", failing(nil), testPolicy()))
	assert.Contains(t, c.Stats().RegisteredQueues, "q")
	require.NotNil(t, c.policies["q"])
	assert.Equal(t, 3, c.policies["q"].Retry.MaxAttempts)

	require.NoError(t, c.Register("q", failing(nil)))
	assert.Nil(t, c.policies["q"], "re-registering with Register returns the queue to legacy behavior")
}

// fakeConsumeChannel is a scripted consume channel recording call order.
type fakeConsumeChannel struct {
	mu          sync.Mutex
	calls       []string
	qosErr      error
	consumeErr  error
	deliveries  chan amqp.Delivery
	notifyClose chan *amqp.Error
	closed      int
}

func newFakeConsumeChannel() *fakeConsumeChannel {
	return &fakeConsumeChannel{deliveries: make(chan amqp.Delivery, 4)}
}

func (f *fakeConsumeChannel) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)
}

func (f *fakeConsumeChannel) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

func (f *fakeConsumeChannel) ExchangeDeclare(string, string, bool, bool, bool, bool, amqp.Table) error {
	f.record("exchange-declare")
	return nil
}

func (f *fakeConsumeChannel) QueueDeclare(name string, _, _, _, _ bool, _ amqp.Table) (amqp.Queue, error) {
	f.record("queue-declare")
	return amqp.Queue{Name: name}, nil
}

func (f *fakeConsumeChannel) QueueBind(string, string, string, bool, amqp.Table) error {
	f.record("queue-bind")
	return nil
}

func (f *fakeConsumeChannel) Qos(int, int, bool) error {
	f.record("qos")
	return f.qosErr
}

func (f *fakeConsumeChannel) Consume(string, string, bool, bool, bool, bool, amqp.Table) (<-chan amqp.Delivery, error) {
	f.record("consume")
	return f.deliveries, f.consumeErr
}

func (f *fakeConsumeChannel) NotifyClose(c chan *amqp.Error) chan *amqp.Error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.notifyClose = c

	return c
}

func (f *fakeConsumeChannel) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed++

	return nil
}

func (f *fakeConsumeChannel) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closed
}

func seamConsumer(ch *fakeConsumeChannel, pub *fakePublisher, pubErr error) *MultiTenantConsumer {
	c := &MultiTenantConsumer{
		rabbitmq: dummyRabbitMQManager(),
		config:   MultiTenantConfig{PrefetchCount: 1},
		logger:   logcompat.New(testutil.NewMockLogger()),
	}
	c.openConsumeChannelFn = func(context.Context, string) (consumeChannel, error) { return ch, nil }
	c.openPublisherFn = func(context.Context, string) (dispositionPublisher, error) {
		if pubErr != nil {
			return nil, pubErr
		}

		return pub, nil
	}
	c.dispositionWaitFn = (&recordingWait{}).wait

	return c
}

func TestAttemptConsume_TopologyRunsBeforeQosAndConsume(t *testing.T) {
	t.Parallel()

	ch := newFakeConsumeChannel()
	pub := &fakePublisher{}
	c := seamConsumer(ch, pub, nil)

	var topologyTenant, topologyQueue string

	policy := testPolicy()
	policy.Topology = func(_ context.Context, tenantID, queueName string, tch rabbitmq.AMQPChannel) error {
		topologyTenant, topologyQueue = tenantID, queueName
		if err := rabbitmq.DeclareDLQTopology(tch, rabbitmq.WithDLXExchangeName("orders.dlx")); err != nil {
			return err
		}

		_, err := tch.QueueDeclare(queueName, true, false, false, false, rabbitmq.GetDLXArgs("orders.dlx"))

		return err
	}

	rt := newTestRuntime(t, policy, nil, &recordingWait{})

	ack := &fakeAcknowledger{}
	ch.deliveries <- deliveryWith(ack, amqp.Table{})

	done := make(chan bool, 1)

	go func() {
		done <- c.attemptConsumeConnection(context.Background(), "tenant-a", "orders",
			failing(DeadLetter(errors.New("forged"))), rt, logcompat.New(testutil.NewMockLogger()))
	}()

	require.Eventually(t, func() bool { return len(pub.published()) == 1 }, time.Second, 5*time.Millisecond)

	ch.mu.Lock()
	notify := ch.notifyClose
	ch.mu.Unlock()
	notify <- &amqp.Error{Reason: "closed"}

	select {
	case reconnect := <-done:
		assert.True(t, reconnect)
	case <-time.After(time.Second):
		t.Fatal("attemptConsumeConnection did not return after channel close")
	}

	assert.Equal(t, "tenant-a", topologyTenant)
	assert.Equal(t, "orders", topologyQueue)

	calls := ch.callLog()
	require.GreaterOrEqual(t, len(calls), 3)
	assert.Equal(t, []string{"qos", "consume"}, calls[len(calls)-2:], "topology runs before Qos and Consume")
	assert.Equal(t, 1, pub.closeCall, "the per-queue publisher is closed when the channel ends")
	assert.Equal(t, 1, ch.closeCount(), "the consume channel is closed when processing ends")
	assert.Equal(t, 1, ack.ackCalls)
}

func TestAttemptConsume_SetupFailuresCloseAndBackOff(t *testing.T) {
	t.Parallel()

	setupErr := errors.New("setup failed")

	tests := []struct {
		name          string
		configure     func(ch *fakeConsumeChannel, policy *QueuePolicy)
		publisherErr  error
		wantChClosed  int
		wantPubClosed int
	}{
		{
			name: "topology error",
			configure: func(_ *fakeConsumeChannel, policy *QueuePolicy) {
				policy.Topology = func(context.Context, string, string, rabbitmq.AMQPChannel) error { return setupErr }
			},
			wantChClosed: 1,
		},
		{
			name:         "qos error",
			configure:    func(ch *fakeConsumeChannel, _ *QueuePolicy) { ch.qosErr = setupErr },
			wantChClosed: 1,
		},
		{
			name:         "publisher open error",
			configure:    func(*fakeConsumeChannel, *QueuePolicy) {},
			publisherErr: setupErr,
			wantChClosed: 1,
		},
		{
			name:          "consume error",
			configure:     func(ch *fakeConsumeChannel, _ *QueuePolicy) { ch.consumeErr = setupErr },
			wantChClosed:  1,
			wantPubClosed: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := newFakeConsumeChannel()
			pub := &fakePublisher{}
			c := seamConsumer(ch, pub, tt.publisherErr)
			policy := testPolicy()
			tt.configure(ch, &policy)

			rt := newTestRuntime(t, policy, nil, &recordingWait{})

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			reconnect := c.attemptConsumeConnection(ctx, "tenant-a", "orders", failing(nil), rt,
				logcompat.New(testutil.NewMockLogger()))

			assert.False(t, reconnect, "a cancelled context ends the backoff instead of reconnecting")
			assert.Equal(t, tt.wantChClosed, ch.closeCount())
			assert.Equal(t, tt.wantPubClosed, pub.closeCall)

			entry, ok := c.retryState.Load("tenant-a")
			require.True(t, ok)
			assert.Equal(t, 1, entry.(*retryStateEntry).retryCount, "setup failures take the tenant backoff path")
		})
	}
}

func TestSuperviseTenantQueues_PassesPolicyToOptedInQueue(t *testing.T) {
	t.Parallel()

	ch := newFakeConsumeChannel()
	pub := &fakePublisher{}
	c := seamConsumer(ch, pub, nil)
	c.handlers = map[string]HandlerFunc{}
	c.tenants = map[string]context.CancelFunc{}
	c.knownTenants = map[string]bool{}

	require.NoError(t, c.RegisterQueue("orders", failing(DeadLetter(errors.New("forged"))), testPolicy()))

	ack := &fakeAcknowledger{}
	ch.deliveries <- deliveryWith(ack, amqp.Table{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.mu.Lock()
	c.startTenantConsumer(ctx, "tenant-a")
	c.mu.Unlock()

	require.Eventually(t, func() bool { return len(pub.published()) == 1 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, "dlx", pub.published()[0].exchange)
}

func TestStartActiveTenants(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()

		var c *MultiTenantConsumer

		_, err := c.StartActiveTenants(context.Background())
		assert.ErrorIs(t, err, ErrNilConsumer)
	})

	t.Run("zero value consumer is not running", func(t *testing.T) {
		t.Parallel()

		_, err := (&MultiTenantConsumer{}).StartActiveTenants(context.Background())
		assert.ErrorIs(t, err, ErrConsumerNotRunning)
	})

	t.Run("before Run", func(t *testing.T) {
		t.Parallel()

		server := setupTenantManagerAPIServer(t, makeTenantSummaries(1))
		c, err := NewMultiTenantConsumerWithError(newTestConfig(server.URL), testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
		require.NoError(t, err)

		t.Cleanup(func() { _ = c.Close() })

		_, err = c.StartActiveTenants(context.Background())
		assert.ErrorIs(t, err, ErrConsumerNotRunning)
	})

	t.Run("after Close", func(t *testing.T) {
		t.Parallel()

		server := setupTenantManagerAPIServer(t, makeTenantSummaries(1))
		c, err := NewMultiTenantConsumerWithError(newTestConfig(server.URL), testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
		require.NoError(t, err)
		require.NoError(t, c.Run(context.Background()))
		require.NoError(t, c.Close())

		_, err = c.StartActiveTenants(context.Background())
		assert.ErrorIs(t, err, ErrConsumerNotRunning)
	})

	t.Run("http-only mode starts nothing", func(t *testing.T) {
		t.Parallel()

		server := setupTenantManagerAPIServer(t, makeTenantSummaries(2))
		c, err := NewMultiTenantConsumerWithError(newTestConfig(server.URL), testutil.NewMockLogger())
		require.NoError(t, err)

		t.Cleanup(func() { _ = c.Close() })
		require.NoError(t, c.Run(context.Background()))

		started, err := c.StartActiveTenants(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 0, started)
	})

	t.Run("listing error is propagated", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)

		c, err := NewMultiTenantConsumerWithError(newTestConfig(server.URL), testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
		require.NoError(t, err)

		t.Cleanup(func() { _ = c.Close() })
		require.NoError(t, c.Run(context.Background()))

		started, err := c.StartActiveTenants(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
		assert.Equal(t, 0, started)
	})

	t.Run("starts only active tenants", func(t *testing.T) {
		t.Parallel()

		summaries := []*client.TenantSummary{
			{ID: "tenant-1", Status: "active"},
			{ID: "tenant-2", Status: "ACTIVE"},
			{ID: "tenant-3", Status: "inactive"},
			nil,
			{ID: "  ", Status: "active"},
		}

		server := setupTenantManagerAPIServer(t, summaries)
		c, err := NewMultiTenantConsumerWithError(newTestConfig(server.URL), testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
		require.NoError(t, err)

		t.Cleanup(func() { _ = c.Close() })
		require.NoError(t, c.Run(context.Background()))

		started, err := c.StartActiveTenants(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 2, started)

		stats := c.Stats()
		assert.Equal(t, 2, stats.ActiveTenants)
		assert.ElementsMatch(t, []string{"tenant-1", "tenant-2"}, stats.TenantIDs)
	})
}

func TestDisposition_AckAndNackFailuresAreLoggedNotFatal(t *testing.T) {
	t.Parallel()

	brokerErr := errors.New("channel closed")
	viaDLX := testPolicy()
	viaDLX.DeadLetter = DeadLetterRoute{ViaQueueDLX: true}

	cases := []struct {
		name    string
		policy  QueuePolicy
		handler HandlerFunc
		wait    *recordingWait
	}{
		{name: "ack", policy: testPolicy(), handler: failing(nil), wait: &recordingWait{}},
		{name: "nack requeue", policy: testPolicy(), handler: failing(errors.New("db down")), wait: &recordingWait{err: context.Canceled}},
		{name: "nack via dlx", policy: viaDLX, handler: failing(DeadLetter(errors.New("forged"))), wait: &recordingWait{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ack := &fakeAcknowledger{err: brokerErr}
			rt := newTestRuntime(t, tc.policy, &fakePublisher{}, tc.wait)

			assert.True(t, runDisposition(t, rt, tc.handler, deliveryWith(ack, amqp.Table{})))
			assert.Equal(t, 1, ack.ackCalls+ack.nackCalls)
		})
	}
}

func TestProcessMessages_UnusablePublisherEndsTheLoop(t *testing.T) {
	t.Parallel()

	pub := &fakePublisher{errs: []error{rabbitmq.ErrPublisherClosed}}
	rt := newTestRuntime(t, testPolicy(), pub, &recordingWait{})
	msgs := make(chan amqp.Delivery, 1)
	msgs <- deliveryWith(&fakeAcknowledger{}, amqp.Table{})

	done := make(chan error, 1)

	go func() {
		done <- (&MultiTenantConsumer{}).processMessages(context.Background(), "tenant-a", "orders",
			failing(DeadLetter(errors.New("forged"))), rt, msgs, make(chan *amqp.Error), logcompat.New(testutil.NewMockLogger()))
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, rabbitmq.ErrPublisherClosed, "the cause reaches the reconnect backoff")
	case <-time.After(time.Second):
		t.Fatal("processMessages must return so the consumer reconnects with a fresh publisher")
	}
}

func TestDisposition_Helpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int32(math.MaxInt32), headerInt(math.MaxInt64))
	assert.Equal(t, int32(0), headerInt(-1))
	assert.Equal(t, int32(3), headerInt(3))

	rt := newDispositionRuntime("orders", testDefaultQueuePolicy(), &fakePublisher{}, nil)
	require.NotNil(t, rt.wait, "a nil wait falls back to backoff.WaitContext")
	assert.NoError(t, rt.wait(context.Background(), 0))
}

func testDefaultQueuePolicy() QueuePolicy {
	return QueuePolicy{Retry: DefaultRetryPolicy(), DeadLetter: DeadLetterRoute{RoutingKey: "dead"}}
}

func TestDefaultOpeners_PropagateChannelErrors(t *testing.T) {
	t.Parallel()

	c := &MultiTenantConsumer{rabbitmq: dummyRabbitMQManager(), logger: logcompat.New(testutil.NewMockLogger())}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.openConsumeChannel(ctx, "tenant-a")
	assert.Error(t, err)

	_, err = c.openDispositionPublisher(ctx, "tenant-a")
	assert.Error(t, err)
}

func TestStartActiveTenants_WithoutTenantManagerClient(t *testing.T) {
	t.Parallel()

	c := &MultiTenantConsumer{rabbitmq: dummyRabbitMQManager(), parentCtx: context.Background()}

	_, err := c.StartActiveTenants(nil) //nolint:staticcheck // a nil context is normalized
	assert.Error(t, err)
}

func TestRestoreOriginRouting_WithoutOriginHeadersKeepsDelivery(t *testing.T) {
	t.Parallel()

	msg := amqp.Delivery{Exchange: "", RoutingKey: "orders", Headers: amqp.Table{HeaderRetryAttempt: int32(1)}}
	restoreOriginRouting(&msg, "orders")

	assert.Equal(t, "", msg.Exchange)
	assert.Equal(t, "orders", msg.RoutingKey)
}

// reconnectRecorder records each reconnect backoff with the tenant's degraded
// flag at that moment, without sleeping.
type reconnectRecorder struct {
	mu       sync.Mutex
	delays   []time.Duration
	degraded []bool
}

func TestConsumeTenantQueue_UnusablePublisherBacksOffAcrossReconnects(t *testing.T) {
	t.Parallel()

	const failures = 4

	pubErrs := make([]error, failures)
	for i := range pubErrs {
		pubErrs[i] = fmt.Errorf("publish: %w", rabbitmq.ErrPublisherClosed)
	}

	pub := &fakePublisher{errs: pubErrs}
	c := seamConsumer(newFakeConsumeChannel(), pub, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		chMu     sync.Mutex
		connects int
		acks     []*fakeAcknowledger
	)

	// Every connect redelivers the refused message, as the broker does after a
	// requeue. Past a bound the opener blocks, so a regression cannot spin.
	c.openConsumeChannelFn = func(ctx context.Context, _ string) (consumeChannel, error) {
		chMu.Lock()
		defer chMu.Unlock()

		connects++
		if connects > failures+1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		ack := &fakeAcknowledger{}
		acks = append(acks, ack)

		ch := newFakeConsumeChannel()
		ch.deliveries <- deliveryWith(ack, amqp.Table{})

		return ch, nil
	}

	rec := &reconnectRecorder{}
	c.reconnectWaitFn = func(_ context.Context, d time.Duration) error {
		rec.mu.Lock()
		defer rec.mu.Unlock()

		rec.delays = append(rec.delays, d)
		rec.degraded = append(rec.degraded, c.IsDegraded("tenant-a"))

		return nil
	}

	policy, err := normalizeQueuePolicy(testPolicy())
	require.NoError(t, err)

	done := make(chan struct{})

	go func() {
		c.consumeTenantQueue(ctx, "tenant-a", "orders", failing(DeadLetter(errors.New("forged"))), &policy,
			logcompat.New(testutil.NewMockLogger()))
		close(done)
	}()

	// The fifth dead-letter publish is confirmed.
	require.Eventually(t, func() bool { return len(pub.published()) == failures+1 }, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return !c.IsDegraded("tenant-a") }, time.Second, 5*time.Millisecond,
		"a confirmed publish clears the degraded flag")

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeTenantQueue did not stop after cancel")
	}

	rec.mu.Lock()
	delays := append([]time.Duration(nil), rec.delays...)
	degraded := append([]bool(nil), rec.degraded...)
	rec.mu.Unlock()

	require.Len(t, delays, failures, "every unusable-publisher exit backs off before reconnecting")

	for i := 1; i < len(delays); i++ {
		assert.Greater(t, delays[i], delays[i-1], "the reconnect backoff grows across reconnects: %v", delays)
	}

	assert.Equal(t, []bool{false, false, true, true}, degraded,
		"the tenant is marked degraded after consecutive failed publishes")

	chMu.Lock()
	defer chMu.Unlock()

	for i, ack := range acks[:failures] {
		assert.Equal(t, 0, ack.ackCalls, "delivery %d must not be acked", i)
		assert.True(t, ack.requeue, "delivery %d must be requeued, never dropped", i)
	}

	assert.Equal(t, 1, acks[failures].ackCalls, "the confirmed dead-letter acks the original")
}
