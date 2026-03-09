package rabbitmq

import (
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	defaultDLXExchangeName = "events.dlx"
	defaultDLQName         = "events.dlq"
	defaultExchangeType    = "topic"
	defaultBindingKey      = "#"

	// DefaultDLQMessageTTL is the default TTL for dead-letter queue messages (7 days).
	// Messages older than this are automatically discarded by the broker.
	DefaultDLQMessageTTL = 7 * 24 * time.Hour

	// DefaultDLQMaxLength is the default maximum number of messages retained in
	// the dead-letter queue. When exceeded, the oldest messages are dropped.
	DefaultDLQMaxLength int64 = 10000
)

// AMQPChannel defines the AMQP channel operations required for DLQ setup.
type AMQPChannel interface {
	ExchangeDeclare(
		name, kind string,
		durable, autoDelete, internal, noWait bool,
		args amqp.Table,
	) error
	QueueDeclare(
		name string,
		durable, autoDelete, exclusive, noWait bool,
		args amqp.Table,
	) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
}

// DLQTopologyConfig defines exchange/queue names for DLQ topology.
type DLQTopologyConfig struct {
	DLXExchangeName string
	DLQName         string
	ExchangeType    string
	BindingKey      string
	QueueMessageTTL time.Duration
	QueueMaxLength  int64
}

// DLQOption configures DLQ topology declaration.
type DLQOption func(*DLQTopologyConfig)

// WithDLXExchangeName overrides the dead-letter exchange name.
func WithDLXExchangeName(name string) DLQOption {
	return func(cfg *DLQTopologyConfig) {
		if name != "" {
			cfg.DLXExchangeName = name
		}
	}
}

// WithDLQName overrides the dead-letter queue name.
func WithDLQName(name string) DLQOption {
	return func(cfg *DLQTopologyConfig) {
		if name != "" {
			cfg.DLQName = name
		}
	}
}

// WithDLQExchangeType overrides the dead-letter exchange type.
func WithDLQExchangeType(exchangeType string) DLQOption {
	return func(cfg *DLQTopologyConfig) {
		if exchangeType != "" {
			cfg.ExchangeType = exchangeType
		}
	}
}

// WithDLQBindingKey overrides the queue binding key to the DLX.
func WithDLQBindingKey(bindingKey string) DLQOption {
	return func(cfg *DLQTopologyConfig) {
		if bindingKey != "" {
			cfg.BindingKey = bindingKey
		}
	}
}

// WithDLQMessageTTL sets x-message-ttl for the DLQ queue.
func WithDLQMessageTTL(ttl time.Duration) DLQOption {
	return func(cfg *DLQTopologyConfig) {
		if ttl > 0 {
			cfg.QueueMessageTTL = ttl
		}
	}
}

// WithDLQMaxLength sets x-max-length for the DLQ queue.
func WithDLQMaxLength(maxLength int64) DLQOption {
	return func(cfg *DLQTopologyConfig) {
		if maxLength > 0 {
			cfg.QueueMaxLength = maxLength
		}
	}
}

func defaultDLQConfig() DLQTopologyConfig {
	return DLQTopologyConfig{
		DLXExchangeName: defaultDLXExchangeName,
		DLQName:         defaultDLQName,
		ExchangeType:    defaultExchangeType,
		BindingKey:      defaultBindingKey,
		QueueMessageTTL: DefaultDLQMessageTTL,
		QueueMaxLength:  DefaultDLQMaxLength,
	}
}

func (cfg DLQTopologyConfig) queueDeclareArgs() amqp.Table {
	args := make(amqp.Table)

	if cfg.QueueMessageTTL > 0 {
		ttlMillis := cfg.QueueMessageTTL.Milliseconds()
		if ttlMillis <= 0 {
			ttlMillis = 1
		}

		args["x-message-ttl"] = ttlMillis
	}

	if cfg.QueueMaxLength > 0 {
		args["x-max-length"] = cfg.QueueMaxLength
	}

	if len(args) == 0 {
		return nil
	}

	return args
}

// DeclareDLQTopology declares dead-letter exchange and queue.
func DeclareDLQTopology(ch AMQPChannel, opts ...DLQOption) error {
	if nilcheck.Interface(ch) {
		return fmt.Errorf("declare dlq topology: %w", ErrChannelRequired)
	}

	cfg := defaultDLQConfig()

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if err := ch.ExchangeDeclare(
		cfg.DLXExchangeName,
		cfg.ExchangeType,
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare dlx exchange: %w", err)
	}

	if _, err := ch.QueueDeclare(
		cfg.DLQName,
		true,
		false,
		false,
		false,
		cfg.queueDeclareArgs(),
	); err != nil {
		return fmt.Errorf("declare dlq queue: %w", err)
	}

	if err := ch.QueueBind(
		cfg.DLQName,
		cfg.BindingKey,
		cfg.DLXExchangeName,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind dlq to dlx: %w", err)
	}

	return nil
}

// GetDLXArgs returns queue declaration args for dead-lettering.
func GetDLXArgs(dlxExchangeName string) amqp.Table {
	if dlxExchangeName == "" {
		dlxExchangeName = defaultDLXExchangeName
	}

	return amqp.Table{
		"x-dead-letter-exchange": dlxExchangeName,
	}
}
