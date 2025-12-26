package poolmanager

import (
	"context"
	"fmt"
	"strings"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// TenantIDHeader is the AMQP header key used to store the tenant identifier.
	// This header is automatically injected by TenantRabbitMQPublisher and can be
	// extracted using ExtractTenantFromMessage.
	TenantIDHeader = "X-Tenant-ID"
)

// AMQPChannel defines the interface for AMQP channel operations needed by the publisher.
// This interface allows for testing with mock implementations.
type AMQPChannel interface {
	// PublishWithContext sends a message to an exchange with the given routing key.
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	// IsClosed returns true if the channel is marked as closed.
	IsClosed() bool
	// Close closes the channel.
	Close() error
}

// TenantRabbitMQPublisher defines the interface for a tenant-aware RabbitMQ publisher.
// All published messages are automatically enriched with the X-Tenant-ID header
// containing the tenant identifier.
type TenantRabbitMQPublisher interface {
	// Publish sends a message to the specified exchange with the given routing key.
	// The X-Tenant-ID header is automatically injected into the message.
	// Returns an error if the context is nil or if publishing fails.
	Publish(ctx context.Context, exchange, routingKey string, msg amqp.Publishing) error

	// GetTenantID returns the tenant identifier associated with this publisher.
	GetTenantID() string
}

// tenantRabbitMQPublisher implements TenantRabbitMQPublisher.
type tenantRabbitMQPublisher struct {
	channel  AMQPChannel
	tenantID string
	logger   libLog.Logger
}

// NewTenantRabbitMQPublisher creates a new tenant-aware RabbitMQ publisher.
// Returns nil if channel is nil or tenantID is empty/whitespace.
//
// The publisher wraps RabbitMQ publish operations and automatically injects
// the X-Tenant-ID header into all published messages to ensure tenant context
// is propagated through message queues.
//
// Example:
//
//	publisher := NewTenantRabbitMQPublisher(channel, "org-123")
//	publisher.Publish(ctx, "exchange", "routing.key", amqp.Publishing{
//	    Body: []byte(`{"event": "user.created"}`),
//	})
//	// Message will have header: X-Tenant-ID: org-123
func NewTenantRabbitMQPublisher(channel AMQPChannel, tenantID string) TenantRabbitMQPublisher {
	if channel == nil {
		return nil
	}

	if strings.TrimSpace(tenantID) == "" {
		return nil
	}

	return &tenantRabbitMQPublisher{
		channel:  channel,
		tenantID: tenantID,
	}
}

// NewTenantRabbitMQPublisherWithLogger creates a new tenant-aware RabbitMQ publisher with logging support.
// Returns nil if channel is nil or tenantID is empty/whitespace.
//
// Example:
//
//	publisher := NewTenantRabbitMQPublisherWithLogger(channel, "org-123", logger)
//	publisher.Publish(ctx, "exchange", "routing.key", amqp.Publishing{
//	    Body: []byte(`{"event": "user.created"}`),
//	})
//	// Message will have header: X-Tenant-ID: org-123
func NewTenantRabbitMQPublisherWithLogger(channel AMQPChannel, tenantID string, logger libLog.Logger) TenantRabbitMQPublisher {
	if channel == nil {
		return nil
	}

	if strings.TrimSpace(tenantID) == "" {
		return nil
	}

	return &tenantRabbitMQPublisher{
		channel:  channel,
		tenantID: tenantID,
		logger:   logger,
	}
}

// Publish sends a message to the specified exchange with the given routing key.
// The X-Tenant-ID header is automatically injected into the message headers.
// If the message already has headers, the tenant header is added to the existing map.
// If the message has no headers, a new headers map is created.
// Any existing X-Tenant-ID header is overwritten with the publisher's tenant ID.
func (p *tenantRabbitMQPublisher) Publish(ctx context.Context, exchange, routingKey string, msg amqp.Publishing) error {
	if ctx == nil {
		return fmt.Errorf("context cannot be nil")
	}

	// Ensure headers map exists
	if msg.Headers == nil {
		msg.Headers = make(amqp.Table)
	}

	// Inject tenant ID header (overwrites any existing value)
	msg.Headers[TenantIDHeader] = p.tenantID

	err := p.channel.PublishWithContext(ctx, exchange, routingKey, false, false, msg)
	if err != nil {
		if p.logger != nil {
			p.logger.Errorf("RabbitMQ Publish failed for tenant %s exchange %s routing key %s: %v", p.tenantID, exchange, routingKey, err)
		}
		return err
	}

	if p.logger != nil {
		p.logger.Infof("RabbitMQ Publish for tenant %s exchange %s routing key %s", p.tenantID, exchange, routingKey)
	}

	return nil
}

// GetTenantID returns the tenant identifier associated with this publisher.
func (p *tenantRabbitMQPublisher) GetTenantID() string {
	return p.tenantID
}

// ExtractTenantFromMessage extracts the tenant ID from an AMQP message's headers.
// Returns the tenant ID if the X-Tenant-ID header exists and is a non-empty string.
// Returns an error if:
//   - The message headers are nil
//   - The X-Tenant-ID header is missing
//   - The header value is not a string
//   - The header value is empty or contains only whitespace
//
// Example:
//
//	func handleMessage(msg amqp.Delivery) error {
//	    tenantID, err := ExtractTenantFromMessage(msg)
//	    if err != nil {
//	        return fmt.Errorf("failed to extract tenant: %w", err)
//	    }
//	    // Use tenantID for tenant-scoped operations
//	}
func ExtractTenantFromMessage(msg amqp.Delivery) (string, error) {
	if msg.Headers == nil {
		return "", fmt.Errorf("message headers are nil")
	}

	headerValue, ok := msg.Headers[TenantIDHeader]
	if !ok {
		return "", fmt.Errorf("header %s not found", TenantIDHeader)
	}

	if headerValue == nil {
		return "", fmt.Errorf("header %s is nil", TenantIDHeader)
	}

	tenantID, ok := headerValue.(string)
	if !ok {
		return "", fmt.Errorf("header %s is not a string: got %T", TenantIDHeader, headerValue)
	}

	if strings.TrimSpace(tenantID) == "" {
		return "", fmt.Errorf("tenant ID is empty")
	}

	return tenantID, nil
}
