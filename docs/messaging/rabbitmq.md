# RabbitMQ Integration

The RabbitMQ package provides connection management, message publishing, and consumption utilities for working with RabbitMQ message broker.

## Overview

The RabbitMQ integration provides:

- Connection pooling and management
- Message publishing and consumption
- Exchange and queue management
- Dead letter queue support
- Message serialization/deserialization
- Retry mechanisms

## Basic Usage

### Connection Setup

```go
import (
    "github.com/yourusername/commons-go/commons/rabbitmq"
)

// Connect using environment variables
conn, err := rabbitmq.Connect()
if err != nil {
    log.Fatal(err)
}
defer conn.Close()
```

### Publishing Messages

```go
// Publish a message
message := &rabbitmq.Message{
    Exchange:   "orders",
    RoutingKey: "order.created",
    Body:       []byte(`{"order_id": "123", "status": "created"}`),
}

err := conn.Publish(context.Background(), message)
if err != nil {
    log.Printf("Failed to publish message: %v", err)
}
```

### Consuming Messages

```go
// Start consuming messages
err := conn.Consume(
    "order-processing-queue",
    func(ctx context.Context, msg *rabbitmq.Message) error {
        // Process the message
        log.Printf("Received message: %s", string(msg.Body))
        
        // Return nil to acknowledge the message
        // Return an error to reject and requeue
        return processOrder(ctx, msg.Body)
    },
)
```

## Configuration

### Connection Configuration

```go
config := rabbitmq.Config{
    URL:             "amqp://guest:guest@localhost:5672/",
    MaxConnections:  10,
    MaxChannels:     100,
    ReconnectDelay:  5 * time.Second,
    HeartbeatInterval: 30 * time.Second,
}

conn, err := rabbitmq.ConnectWithConfig(config)
```

### Exchange and Queue Setup

```go
// Declare exchange
err := conn.DeclareExchange(rabbitmq.ExchangeConfig{
    Name:       "orders",
    Type:       "topic",
    Durable:    true,
    AutoDelete: false,
})

// Declare queue
err = conn.DeclareQueue(rabbitmq.QueueConfig{
    Name:       "order-processing",
    Durable:    true,
    Exclusive:  false,
    AutoDelete: false,
})

// Bind queue to exchange
err = conn.BindQueue("order-processing", "order.*", "orders")
```

## Best Practices

1. **Use durable exchanges and queues** for important messages
2. **Implement proper error handling** and retry logic
3. **Use dead letter queues** for failed messages
4. **Monitor connection health** and implement reconnection logic
5. **Serialize messages properly** using JSON or Protocol Buffers

## Error Handling

```go
func processMessage(ctx context.Context, msg *rabbitmq.Message) error {
    var order Order
    if err := json.Unmarshal(msg.Body, &order); err != nil {
        // Log parsing error but don't requeue
        log.Printf("Failed to parse message: %v", err)
        return nil // Acknowledge to remove from queue
    }
    
    if err := processOrder(ctx, &order); err != nil {
        // Business logic error - requeue for retry
        return fmt.Errorf("failed to process order: %w", err)
    }
    
    return nil
}
```

## Examples

### Publisher Service

```go
type OrderPublisher struct {
    conn *rabbitmq.Connection
}

func (p *OrderPublisher) PublishOrderCreated(ctx context.Context, order *Order) error {
    data, err := json.Marshal(order)
    if err != nil {
        return err
    }
    
    message := &rabbitmq.Message{
        Exchange:   "orders",
        RoutingKey: "order.created",
        Body:       data,
        Headers: map[string]interface{}{
            "event_type": "order_created",
            "timestamp":  time.Now().Unix(),
        },
    }
    
    return p.conn.Publish(ctx, message)
}
```

### Consumer Service

```go
type OrderConsumer struct {
    conn *rabbitmq.Connection
}

func (c *OrderConsumer) Start(ctx context.Context) error {
    return c.conn.Consume(
        "order-processing-queue",
        c.handleOrderMessage,
    )
}

func (c *OrderConsumer) handleOrderMessage(ctx context.Context, msg *rabbitmq.Message) error {
    var order Order
    if err := json.Unmarshal(msg.Body, &order); err != nil {
        return err
    }
    
    return c.processOrder(ctx, &order)
}
``` 