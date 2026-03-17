package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

type fakeAcknowledger struct {
	ackCalls  int
	nackCalls int
	requeue   bool
}

func (f *fakeAcknowledger) Ack(uint64, bool) error {
	f.ackCalls++
	return nil
}

func (f *fakeAcknowledger) Nack(uint64, bool, bool) error {
	f.nackCalls++
	f.requeue = true
	return nil
}

func (f *fakeAcknowledger) Reject(uint64, bool) error { return nil }

func TestMultiTenantConsumer_HandleMessage_AcksSuccessfulMessages(t *testing.T) {
	t.Parallel()

	consumer := &MultiTenantConsumer{}
	ack := &fakeAcknowledger{}
	logger := logcompat.New(testutil.NewMockLogger())

	var seenTenantID string
	msg := amqp.Delivery{Acknowledger: ack, DeliveryTag: 1, Headers: amqp.Table{}}

	consumer.handleMessage(context.Background(), "tenant-ack", "queue-a", func(ctx context.Context, delivery amqp.Delivery) error {
		seenTenantID = core.GetTenantIDFromContext(ctx)
		return nil
	}, msg, logger)

	assert.Equal(t, "tenant-ack", seenTenantID)
	assert.Equal(t, 1, ack.ackCalls)
	assert.Equal(t, 0, ack.nackCalls)
}

func TestMultiTenantConsumer_HandleMessage_NacksFailedMessages(t *testing.T) {
	t.Parallel()

	consumer := &MultiTenantConsumer{}
	ack := &fakeAcknowledger{}
	logger := logcompat.New(testutil.NewMockLogger())

	msg := amqp.Delivery{Acknowledger: ack, DeliveryTag: 2, Headers: amqp.Table{}}

	consumer.handleMessage(context.Background(), "tenant-nack", "queue-b", func(context.Context, amqp.Delivery) error {
		return errors.New("boom")
	}, msg, logger)

	assert.Equal(t, 0, ack.ackCalls)
	assert.Equal(t, 1, ack.nackCalls)
	assert.True(t, ack.requeue)
}

func TestMultiTenantConsumer_ProcessMessages_ReturnsOnChannelClose(t *testing.T) {
	t.Parallel()

	consumer := &MultiTenantConsumer{}
	logger := logcompat.New(testutil.NewMockLogger())
	msgs := make(chan amqp.Delivery)
	notifyClose := make(chan *amqp.Error, 1)
	done := make(chan struct{})

	go func() {
		consumer.processMessages(context.Background(), "tenant-close", "queue-c", func(context.Context, amqp.Delivery) error {
			return nil
		}, msgs, notifyClose, logger)
		close(done)
	}()

	notifyClose <- &amqp.Error{Reason: "channel closed"}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("processMessages did not return after channel close notification")
	}
}
