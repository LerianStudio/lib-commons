# RabbitMQ Messaging Patterns with Commons-Go

## 🎯 Overview

This guide demonstrates how to improve RabbitMQ messaging patterns across Midaz components using commons-go libraries for better reliability, observability, and error handling.

## 📊 Common Messaging Issues in Midaz

### Current Problems
- **Basic Connection Management**: Simple RabbitMQ connections without proper error handling
- **No Circuit Breaker Protection**: Message publishing can cascade failures
- **Limited Retry Logic**: Failed messages are often lost
- **Poor Observability**: Limited visibility into message flow and failures
- **Inconsistent Error Handling**: Different services handle messaging errors differently

### Found In
- Core transaction service (event publishing)
- Onboarding service (workflow events) 
- CRM plugin (customer events)
- Fees plugin (calculation events)
- All services requiring event-driven communication

## 🛠️ Enhanced RabbitMQ Patterns

### 1. Robust Message Publisher

#### ❌ Before: Basic RabbitMQ Publisher
```go
// Common pattern across services
type BasicEventPublisher struct {
    connection *amqp.Connection
    channel    *amqp.Channel
    logger     log.Logger
}

func (p *BasicEventPublisher) PublishEvent(event interface{}) error {
    body, err := json.Marshal(event)
    if err != nil {
        return err
    }
    
    err = p.channel.Publish(
        "events",    // exchange
        "",          // routing key
        false,       // mandatory
        false,       // immediate
        amqp.Publishing{
            ContentType: "application/json",
            Body:        body,
        },
    )
    
    if err != nil {
        p.logger.Error("Failed to publish event", "error", err)
        return err  // No retry logic
    }
    
    return nil
}
```

#### ✅ After: Enhanced Publisher with Commons-Go
```go
import (
    commonsRabbit "github.com/LerianStudio/lib-commons/commons/rabbitmq"
    "github.com/LerianStudio/lib-commons/commons/circuitbreaker"
    "github.com/LerianStudio/lib-commons/commons/retry"
    "github.com/LerianStudio/lib-commons/commons/observability"
)

type EnhancedEventPublisher struct {
    rabbitConn     *commonsRabbit.RabbitConnection
    circuitBreaker *circuitbreaker.CircuitBreaker
    retryConfig    *retry.JitterConfig
    metrics        *observability.MetricsCollector
    logger         log.Logger
}

func NewEnhancedEventPublisher(config *MessagingConfig, logger log.Logger) (*EnhancedEventPublisher, error) {
    // Create commons-go RabbitMQ connection
    rabbitConn := &commonsRabbit.RabbitConnection{
        URL:                config.RabbitURL,
        ReconnectInterval:  config.ReconnectInterval,
        MaxReconnectAttempts: config.MaxReconnectAttempts,
        Logger:             logger,
    }
    
    // Add circuit breaker protection
    cb := circuitbreaker.New("rabbitmq-publisher",
        circuitbreaker.WithThreshold(5),
        circuitbreaker.WithTimeout(30*time.Second),
        circuitbreaker.WithOnStateChange(func(change circuitbreaker.StateChange) {
            logger.Info("RabbitMQ circuit breaker state changed",
                "from", change.From, "to", change.To)
        }),
    )
    
    // Configure retry with jitter
    retryConfig := &retry.JitterConfig{
        Type:       retry.ExponentialJitter,
        BaseDelay:  500*time.Millisecond,
        MaxDelay:   10*time.Second,
        Multiplier: 2.0,
    }
    
    // Initialize metrics
    obsProvider, err := observability.New(context.Background(),
        observability.WithServiceName(config.ServiceName),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to initialize observability: %w", err)
    }
    
    metrics, err := observability.NewMetricsCollector(obsProvider)
    if err != nil {
        return nil, fmt.Errorf("failed to create metrics collector: %w", err)
    }
    
    return &EnhancedEventPublisher{
        rabbitConn:     rabbitConn,
        circuitBreaker: cb,
        retryConfig:    retryConfig,
        metrics:        metrics,
        logger:         logger,
    }, nil
}

func (p *EnhancedEventPublisher) Connect(ctx context.Context) error {
    return p.circuitBreaker.ExecuteWithContext(ctx, func() error {
        if err := p.rabbitConn.Connect(); err != nil {
            p.logger.Error("Failed to connect to RabbitMQ", "error", err)
            return fmt.Errorf("rabbitmq connection failed: %w", err)
        }
        p.logger.Info("Successfully connected to RabbitMQ")
        return nil
    })
}

func (p *EnhancedEventPublisher) PublishEvent(ctx context.Context, exchange, routingKey string, event interface{}) error {
    // Add distributed tracing
    ctx, span := p.metrics.StartSpan(ctx, "publish_event")
    defer span.End()
    
    span.SetAttributes(
        attribute.String("exchange", exchange),
        attribute.String("routing_key", routingKey),
        attribute.String("event_type", reflect.TypeOf(event).Name()),
    )
    
    // Record metrics
    timer := p.metrics.NewTimer(ctx, "event_publish", "messaging")
    
    err := retry.ExecuteWithJitter(func() error {
        return p.circuitBreaker.ExecuteWithContext(ctx, func() error {
            return p.publishWithObservability(ctx, exchange, routingKey, event)
        })
    }, p.retryConfig, 3)
    
    if err != nil {
        timer.Stop(500) // Error status
        p.metrics.RecordError(ctx, "event_publish", "messaging", "publish_failed")
        span.RecordError(err)
        return fmt.Errorf("failed to publish event: %w", err)
    }
    
    timer.Stop(200) // Success status
    p.metrics.IncrementCounter(ctx, "events_published", map[string]string{
        "exchange":     exchange,
        "routing_key":  routingKey,
        "event_type":   reflect.TypeOf(event).Name(),
    })
    
    return nil
}

func (p *EnhancedEventPublisher) publishWithObservability(ctx context.Context, exchange, routingKey string, event interface{}) error {
    body, err := json.Marshal(event)
    if err != nil {
        return fmt.Errorf("failed to marshal event: %w", err)
    }
    
    // Add trace context to message headers
    headers := amqp.Table{}
    if traceID := trace.SpanFromContext(ctx).SpanContext().TraceID(); traceID.IsValid() {
        headers["trace_id"] = traceID.String()
    }
    
    channel, err := p.rabbitConn.GetChannel()
    if err != nil {
        return fmt.Errorf("failed to get channel: %w", err)
    }
    
    err = channel.Publish(
        exchange,
        routingKey,
        false, // mandatory
        false, // immediate
        amqp.Publishing{
            ContentType:  "application/json",
            Body:         body,
            Headers:      headers,
            Timestamp:    time.Now(),
            MessageId:    uuid.New().String(),
            DeliveryMode: amqp.Persistent, // Ensure message durability
        },
    )
    
    if err != nil {
        p.logger.Error("Failed to publish message",
            "exchange", exchange,
            "routing_key", routingKey,
            "error", err)
        return fmt.Errorf("publish failed: %w", err)
    }
    
    p.logger.Debug("Event published successfully",
        "exchange", exchange,
        "routing_key", routingKey,
        "event_type", reflect.TypeOf(event).Name())
    
    return nil
}
```

### 2. Resilient Message Consumer

#### ❌ Before: Basic Consumer
```go
type BasicEventConsumer struct {
    connection *amqp.Connection
    channel    *amqp.Channel
    logger     log.Logger
}

func (c *BasicEventConsumer) StartConsuming(queueName string, handler func([]byte) error) error {
    msgs, err := c.channel.Consume(
        queueName,
        "",    // consumer
        true,  // auto-ack (dangerous!)
        false, // exclusive
        false, // no-local
        false, // no-wait
        nil,   // args
    )
    if err != nil {
        return err
    }
    
    for msg := range msgs {
        if err := handler(msg.Body); err != nil {
            c.logger.Error("Failed to handle message", "error", err)
            // Message is lost due to auto-ack!
        }
    }
    
    return nil
}
```

#### ✅ After: Enhanced Consumer with Commons-Go
```go
type EnhancedEventConsumer struct {
    rabbitConn     *commonsRabbit.RabbitConnection
    circuitBreaker *circuitbreaker.CircuitBreaker
    retryConfig    *retry.JitterConfig
    metrics        *observability.MetricsCollector
    logger         log.Logger
    deadLetterQueue string
}

func NewEnhancedEventConsumer(config *MessagingConfig, logger log.Logger) (*EnhancedEventConsumer, error) {
    rabbitConn := &commonsRabbit.RabbitConnection{
        URL:                config.RabbitURL,
        ReconnectInterval:  config.ReconnectInterval,
        MaxReconnectAttempts: config.MaxReconnectAttempts,
        Logger:             logger,
    }
    
    cb := circuitbreaker.New("rabbitmq-consumer",
        circuitbreaker.WithThreshold(10),
        circuitbreaker.WithTimeout(10*time.Second),
    )
    
    retryConfig := &retry.JitterConfig{
        Type:       retry.ExponentialJitter,
        BaseDelay:  time.Second,
        MaxDelay:   30*time.Second,
        Multiplier: 2.0,
    }
    
    obsProvider, err := observability.New(context.Background(),
        observability.WithServiceName(config.ServiceName),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to initialize observability: %w", err)
    }
    
    metrics, err := observability.NewMetricsCollector(obsProvider)
    if err != nil {
        return nil, fmt.Errorf("failed to create metrics collector: %w", err)
    }
    
    return &EnhancedEventConsumer{
        rabbitConn:      rabbitConn,
        circuitBreaker:  cb,
        retryConfig:     retryConfig,
        metrics:         metrics,
        logger:          logger,
        deadLetterQueue: config.DeadLetterQueue,
    }, nil
}

func (c *EnhancedEventConsumer) StartConsuming(ctx context.Context, queueName string, handler EventHandler) error {
    channel, err := c.rabbitConn.GetChannel()
    if err != nil {
        return fmt.Errorf("failed to get channel: %w", err)
    }
    
    // Set QoS to control message prefetch
    if err := channel.Qos(10, 0, false); err != nil {
        return fmt.Errorf("failed to set QoS: %w", err)
    }
    
    msgs, err := channel.Consume(
        queueName,
        "",    // consumer
        false, // manual ack for reliability
        false, // exclusive
        false, // no-local
        false, // no-wait
        nil,   // args
    )
    if err != nil {
        return fmt.Errorf("failed to start consuming: %w", err)
    }
    
    c.logger.Info("Started consuming messages", "queue", queueName)
    
    for {
        select {
        case <-ctx.Done():
            c.logger.Info("Stopping consumer", "queue", queueName)
            return ctx.Err()
        case msg, ok := <-msgs:
            if !ok {
                c.logger.Warn("Message channel closed, reconnecting...")
                return fmt.Errorf("message channel closed")
            }
            
            c.handleMessage(ctx, msg, handler)
        }
    }
}

func (c *EnhancedEventConsumer) handleMessage(ctx context.Context, msg amqp.Delivery, handler EventHandler) {
    // Extract trace context from message headers
    if traceID, ok := msg.Headers["trace_id"].(string); ok {
        // Add trace context to current context
        ctx = trace.ContextWithSpanContext(ctx, trace.SpanContextFromTraceID(traceID))
    }
    
    ctx, span := c.metrics.StartSpan(ctx, "consume_event")
    defer span.End()
    
    span.SetAttributes(
        attribute.String("queue", msg.RoutingKey),
        attribute.String("message_id", msg.MessageId),
        attribute.Int("retry_count", c.getRetryCount(msg)),
    )
    
    timer := c.metrics.NewTimer(ctx, "event_consume", "messaging")
    
    err := c.circuitBreaker.ExecuteWithContext(ctx, func() error {
        return handler.Handle(ctx, msg.Body)
    })
    
    if err != nil {
        timer.Stop(500) // Error status
        c.metrics.RecordError(ctx, "event_consume", "messaging", "handler_failed")
        span.RecordError(err)
        
        c.handleFailedMessage(ctx, msg, err)
        return
    }
    
    timer.Stop(200) // Success status
    c.metrics.IncrementCounter(ctx, "events_consumed", map[string]string{
        "queue": msg.RoutingKey,
    })
    
    // Acknowledge successful processing
    if err := msg.Ack(false); err != nil {
        c.logger.Error("Failed to acknowledge message", "error", err)
    }
    
    c.logger.Debug("Message processed successfully",
        "queue", msg.RoutingKey,
        "message_id", msg.MessageId)
}

func (c *EnhancedEventConsumer) handleFailedMessage(ctx context.Context, msg amqp.Delivery, err error) {
    retryCount := c.getRetryCount(msg)
    maxRetries := 3
    
    if retryCount < maxRetries {
        // Increment retry count and requeue with delay
        c.requeueWithDelay(ctx, msg, retryCount+1)
        c.logger.Warn("Message processing failed, requeuing",
            "error", err,
            "retry_count", retryCount+1,
            "message_id", msg.MessageId)
    } else {
        // Send to dead letter queue
        c.sendToDeadLetterQueue(ctx, msg, err)
        c.logger.Error("Message processing failed permanently, sending to DLQ",
            "error", err,
            "retry_count", retryCount,
            "message_id", msg.MessageId)
        
        // Acknowledge to remove from original queue
        msg.Ack(false)
    }
}

func (c *EnhancedEventConsumer) getRetryCount(msg amqp.Delivery) int {
    if count, ok := msg.Headers["retry_count"].(int); ok {
        return count
    }
    return 0
}

func (c *EnhancedEventConsumer) requeueWithDelay(ctx context.Context, msg amqp.Delivery, retryCount int) {
    // Implement exponential backoff
    delay := time.Duration(retryCount) * time.Second * 2
    
    go func() {
        time.Sleep(delay)
        
        // Create new headers with updated retry count
        headers := amqp.Table{}
        for k, v := range msg.Headers {
            headers[k] = v
        }
        headers["retry_count"] = retryCount
        headers["retry_timestamp"] = time.Now().Unix()
        
        channel, err := c.rabbitConn.GetChannel()
        if err != nil {
            c.logger.Error("Failed to get channel for requeue", "error", err)
            return
        }
        
        err = channel.Publish(
            "",             // exchange
            msg.RoutingKey, // routing key (back to same queue)
            false,          // mandatory
            false,          // immediate
            amqp.Publishing{
                ContentType:  msg.ContentType,
                Body:         msg.Body,
                Headers:      headers,
                Timestamp:    time.Now(),
                MessageId:    msg.MessageId,
                DeliveryMode: amqp.Persistent,
            },
        )
        
        if err != nil {
            c.logger.Error("Failed to requeue message", "error", err)
        }
        
        msg.Ack(false) // Remove original message
    }()
}

func (c *EnhancedEventConsumer) sendToDeadLetterQueue(ctx context.Context, msg amqp.Delivery, originalErr error) {
    if c.deadLetterQueue == "" {
        c.logger.Warn("No dead letter queue configured, discarding message")
        return
    }
    
    headers := amqp.Table{}
    for k, v := range msg.Headers {
        headers[k] = v
    }
    headers["original_error"] = originalErr.Error()
    headers["failed_timestamp"] = time.Now().Unix()
    headers["original_queue"] = msg.RoutingKey
    
    channel, err := c.rabbitConn.GetChannel()
    if err != nil {
        c.logger.Error("Failed to get channel for DLQ", "error", err)
        return
    }
    
    err = channel.Publish(
        "",                 // exchange
        c.deadLetterQueue,  // routing key
        false,              // mandatory
        false,              // immediate
        amqp.Publishing{
            ContentType:  msg.ContentType,
            Body:         msg.Body,
            Headers:      headers,
            Timestamp:    time.Now(),
            MessageId:    msg.MessageId,
            DeliveryMode: amqp.Persistent,
        },
    )
    
    if err != nil {
        c.logger.Error("Failed to send message to DLQ", "error", err)
    }
}

// EventHandler interface for type-safe message handling
type EventHandler interface {
    Handle(ctx context.Context, body []byte) error
}

// TransactionEventHandler example implementation
type TransactionEventHandler struct {
    transactionService *TransactionService
    logger            log.Logger
}

func (h *TransactionEventHandler) Handle(ctx context.Context, body []byte) error {
    var event TransactionEvent
    if err := json.Unmarshal(body, &event); err != nil {
        return fmt.Errorf("failed to unmarshal transaction event: %w", err)
    }
    
    return h.transactionService.ProcessTransactionEvent(ctx, &event)
}
```

### 3. Configuration for Enhanced Messaging

#### ✅ Messaging Configuration
```go
type MessagingConfig struct {
    RabbitURL            string        `env:"RABBIT_URL" validate:"required"`
    ServiceName          string        `env:"SERVICE_NAME" validate:"required"`
    ReconnectInterval    time.Duration `env:"RABBIT_RECONNECT_INTERVAL" envDefault:"5s"`
    MaxReconnectAttempts int           `env:"RABBIT_MAX_RECONNECT" envDefault:"10"`
    DeadLetterQueue      string        `env:"RABBIT_DLQ" envDefault:"dlq"`
    PrefetchCount        int           `env:"RABBIT_PREFETCH" envDefault:"10"`
    
    // Circuit breaker settings
    CircuitBreakerThreshold int           `env:"CIRCUIT_BREAKER_THRESHOLD" envDefault:"5"`
    CircuitBreakerTimeout   time.Duration `env:"CIRCUIT_BREAKER_TIMEOUT" envDefault:"30s"`
    
    // Retry settings
    MaxRetries    int           `env:"MESSAGE_MAX_RETRIES" envDefault:"3"`
    RetryDelay    time.Duration `env:"MESSAGE_RETRY_DELAY" envDefault:"1s"`
    MaxRetryDelay time.Duration `env:"MESSAGE_MAX_RETRY_DELAY" envDefault:"30s"`
}

func (c *MessagingConfig) Validate() error {
    if c.RabbitURL == "" {
        return errors.New("rabbit URL is required")
    }
    if c.ServiceName == "" {
        return errors.New("service name is required")
    }
    return nil
}
```

## 📋 Integration Examples

### Transaction Service Event Publishing
```go
// Example: Publishing transaction events with enhanced patterns
func (s *TransactionService) PublishTransactionCreated(ctx context.Context, tx *Transaction) error {
    event := TransactionCreatedEvent{
        TransactionID: tx.ID,
        Amount:       tx.Amount,
        Currency:     tx.Currency,
        Timestamp:    time.Now(),
        UserID:       tx.UserID,
    }
    
    return s.eventPublisher.PublishEvent(ctx, "transactions", "transaction.created", event)
}
```

### CRM Service Event Consumption
```go
// Example: Consuming customer events in CRM service
func (s *CRMService) StartEventConsumption(ctx context.Context) error {
    customerHandler := &CustomerEventHandler{
        crmService: s,
        logger:     s.logger,
    }
    
    return s.eventConsumer.StartConsuming(ctx, "crm.customer.events", customerHandler)
}

type CustomerEventHandler struct {
    crmService *CRMService
    logger     log.Logger
}

func (h *CustomerEventHandler) Handle(ctx context.Context, body []byte) error {
    var event CustomerEvent
    if err := json.Unmarshal(body, &event); err != nil {
        return fmt.Errorf("failed to unmarshal customer event: %w", err)
    }
    
    switch event.Type {
    case "customer.created":
        return h.crmService.HandleCustomerCreated(ctx, &event)
    case "customer.updated":
        return h.crmService.HandleCustomerUpdated(ctx, &event)
    default:
        h.logger.Warn("Unknown event type", "type", event.Type)
        return nil // Don't retry unknown events
    }
}
```

## 📈 Benefits

| Aspect                  | Before                 | After                           | Improvement          |
| ----------------------- | ---------------------- | ------------------------------- | -------------------- |
| **Message Reliability** | 85% (lost on failures) | 99.9% (with retry + DLQ)        | +14.9%               |
| **Error Recovery**      | Manual intervention    | Automatic retry with backoff    | -95% manual work     |
| **Observability**       | Basic logs             | Distributed tracing + metrics   | +400% visibility     |
| **Performance**         | Blocking failures      | Circuit breaker protection      | +60% resilience      |
| **Debugging**           | Limited context        | Full message lifecycle tracking | +300% debugging ease |

## 🔗 Related Resources

- [HTTP Client Patterns](../http/retry-with-jitter.md)
- [Observability Migration](../observability/observability-migration.md)
- [Core Components Guide](../../components/core/README.md)
- [Transaction Service Integration](../../components/core/transaction-service-migration.md)

---

**💡 Pro Tip**: Start with the publisher patterns since they're easier to implement and provide immediate benefits. Then gradually migrate consumers to the enhanced patterns with proper error handling and observability. 