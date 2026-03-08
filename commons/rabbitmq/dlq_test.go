//go:build unit

package rabbitmq

import (
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeChannel struct {
	exchangeDeclareCount int
	queueDeclareCount    int
	queueBindCount       int

	lastExchangeName string
	lastExchangeType string
	lastQueueName    string
	lastQueueArgs    amqp.Table
	lastBindQueue    string
	lastBindKey      string
	lastBindExchange string
}

func (f *fakeChannel) ExchangeDeclare(name, kind string, _, _, _, _ bool, _ amqp.Table) error {
	f.exchangeDeclareCount++
	f.lastExchangeName = name
	f.lastExchangeType = kind

	return nil
}

func (f *fakeChannel) QueueDeclare(name string, _, _, _, _ bool, args amqp.Table) (amqp.Queue, error) {
	f.queueDeclareCount++
	f.lastQueueName = name
	f.lastQueueArgs = args

	return amqp.Queue{Name: name}, nil
}

func (f *fakeChannel) QueueBind(name, key, exchange string, _ bool, _ amqp.Table) error {
	f.queueBindCount++
	f.lastBindQueue = name
	f.lastBindKey = key
	f.lastBindExchange = exchange

	return nil
}

func TestDeclareDLQTopology_Success(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	err := DeclareDLQTopology(ch, WithDLXExchangeName("matcher.events.dlx"), WithDLQName("matcher.events.dlq"))

	require.NoError(t, err)
	assert.Equal(t, 1, ch.exchangeDeclareCount)
	assert.Equal(t, 1, ch.queueDeclareCount)
	assert.Equal(t, 1, ch.queueBindCount)

	assert.Equal(t, "matcher.events.dlx", ch.lastExchangeName)
	assert.Equal(t, defaultExchangeType, ch.lastExchangeType)
	assert.Equal(t, "matcher.events.dlq", ch.lastQueueName)
	assert.Equal(t, "matcher.events.dlq", ch.lastBindQueue)
	assert.Equal(t, "#", ch.lastBindKey)
	assert.Equal(t, "matcher.events.dlx", ch.lastBindExchange)

	// Verify default TTL and max-length are applied
	require.NotNil(t, ch.lastQueueArgs)
	assert.Equal(t, DefaultDLQMessageTTL.Milliseconds(), ch.lastQueueArgs["x-message-ttl"])
	assert.Equal(t, DefaultDLQMaxLength, ch.lastQueueArgs["x-max-length"])
}

func TestDeclareDLQTopology_NilChannel(t *testing.T) {
	t.Parallel()

	err := DeclareDLQTopology(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChannelRequired)
}

func TestDeclareDLQTopology_TypedNilChannel(t *testing.T) {
	t.Parallel()

	var nilChannel *fakeChannel
	var ch AMQPChannel = nilChannel

	err := DeclareDLQTopology(ch)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChannelRequired)
}

func TestDeclareDLQTopology_ExchangeError(t *testing.T) {
	t.Parallel()

	ch := &fakeChannelExchangeError{}
	err := DeclareDLQTopology(ch)
	require.Error(t, err)
	require.ErrorIs(t, err, errExchangeFailed)
}

func TestDeclareDLQTopology_QueueDeclareError(t *testing.T) {
	t.Parallel()

	errQueueDeclareFailed := errors.New("queue declare failed")
	ch := &fakeChannelQueueDeclareError{err: errQueueDeclareFailed}

	err := DeclareDLQTopology(ch)
	require.Error(t, err)
	require.ErrorIs(t, err, errQueueDeclareFailed)
}

func TestDeclareDLQTopology_QueueBindError(t *testing.T) {
	t.Parallel()

	errQueueBindFailed := errors.New("queue bind failed")
	ch := &fakeChannelQueueBindError{err: errQueueBindFailed}

	err := DeclareDLQTopology(ch)
	require.Error(t, err)
	require.ErrorIs(t, err, errQueueBindFailed)
}

var errExchangeFailed = errors.New("exchange declare failed")

type fakeChannelExchangeError struct{ fakeChannel }

func (f *fakeChannelExchangeError) ExchangeDeclare(
	_, _ string,
	_, _, _, _ bool,
	_ amqp.Table,
) error {
	return errExchangeFailed
}

func TestGetDLXArgs(t *testing.T) {
	t.Parallel()

	args := GetDLXArgs("my.dlx")
	require.NotNil(t, args)
	assert.Equal(t, "my.dlx", args["x-dead-letter-exchange"])
}

func TestGetDLXArgs_DefaultExchange(t *testing.T) {
	t.Parallel()

	args := GetDLXArgs("")
	require.NotNil(t, args)
	assert.Equal(t, defaultDLXExchangeName, args["x-dead-letter-exchange"])
}

func TestDeclareDLQTopology_CustomExchangeTypeAndBindingKey(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	err := DeclareDLQTopology(
		ch,
		WithDLQExchangeType("direct"),
		WithDLQBindingKey("payments.failed"),
	)

	require.NoError(t, err)
	assert.Equal(t, "direct", ch.lastExchangeType)
	assert.Equal(t, "payments.failed", ch.lastBindKey)
}

func TestDeclareDLQTopology_EmptyExchangeTypeAndBindingKeyKeepDefaults(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	err := DeclareDLQTopology(
		ch,
		WithDLQExchangeType(""),
		WithDLQBindingKey(""),
	)

	require.NoError(t, err)
	assert.Equal(t, defaultExchangeType, ch.lastExchangeType)
	assert.Equal(t, defaultBindingKey, ch.lastBindKey)
}

func TestDeclareDLQTopology_QueueArgsOptions(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	err := DeclareDLQTopology(
		ch,
		WithDLQMessageTTL(45*time.Second),
		WithDLQMaxLength(500),
	)

	require.NoError(t, err)
	require.NotNil(t, ch.lastQueueArgs)
	assert.Equal(t, int64(45000), ch.lastQueueArgs["x-message-ttl"])
	assert.Equal(t, int64(500), ch.lastQueueArgs["x-max-length"])
}

func TestDeclareDLQTopology_ZeroTTLAndMaxLengthKeepDefaults(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	err := DeclareDLQTopology(
		ch,
		WithDLQMessageTTL(0),
		WithDLQMaxLength(0),
	)

	require.NoError(t, err)
	// Zero values in options are ignored, so defaults apply (7 days TTL, 10000 max-length).
	require.NotNil(t, ch.lastQueueArgs)
	assert.Equal(t, DefaultDLQMessageTTL.Milliseconds(), ch.lastQueueArgs["x-message-ttl"])
	assert.Equal(t, DefaultDLQMaxLength, ch.lastQueueArgs["x-max-length"])
}

type fakeChannelQueueDeclareError struct {
	fakeChannel
	err error
}

func (f *fakeChannelQueueDeclareError) QueueDeclare(
	_ string,
	_, _, _, _ bool,
	_ amqp.Table,
) (amqp.Queue, error) {
	return amqp.Queue{}, f.err
}

type fakeChannelQueueBindError struct {
	fakeChannel
	err error
}

func (f *fakeChannelQueueBindError) QueueBind(_ string, _ string, _ string, _ bool, _ amqp.Table) error {
	return f.err
}
