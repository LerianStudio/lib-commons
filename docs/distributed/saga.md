# Saga Pattern

The saga pattern provides a way to manage distributed transactions across multiple services, ensuring data consistency through a sequence of local transactions with compensating actions.

## Overview

Features:
- Step-based transaction orchestration
- Automatic compensation on failure
- Support for both orchestration and choreography patterns
- Distributed saga with event publishing
- Retry and timeout support

## Basic Usage

```go
import (
    "github.com/LerianStudio/lib-commons/commons/saga"
)

// Create a saga
orderSaga := saga.NewSaga("create-order")

// Add steps
orderSaga.
    AddStep(&CreateOrderStep{}).
    AddStep(&ReserveInventoryStep{}).
    AddStep(&ChargePaymentStep{}).
    AddStep(&SendNotificationStep{})

// Execute
ctx := context.Background()
err := orderSaga.Execute(ctx, orderData)
if err != nil {
    // Saga failed and was compensated
    log.Error("Order creation failed", "error", err)
}
```

## Implementing Saga Steps

### Step Interface

```go
type Step interface {
    Name() string
    Execute(ctx context.Context, data interface{}) error
    Compensate(ctx context.Context, data interface{}) error
}
```

### Example Step Implementation

```go
type ReserveInventoryStep struct {
    inventoryService InventoryService
}

func (s *ReserveInventoryStep) Name() string {
    return "reserve-inventory"
}

func (s *ReserveInventoryStep) Execute(ctx context.Context, data interface{}) error {
    order := data.(*OrderData)
    
    // Reserve inventory
    reservation, err := s.inventoryService.Reserve(ctx, order.Items)
    if err != nil {
        return fmt.Errorf("failed to reserve inventory: %w", err)
    }
    
    // Store reservation ID for compensation
    order.ReservationID = reservation.ID
    return nil
}

func (s *ReserveInventoryStep) Compensate(ctx context.Context, data interface{}) error {
    order := data.(*OrderData)
    
    if order.ReservationID == "" {
        // Nothing to compensate
        return nil
    }
    
    // Release reservation
    err := s.inventoryService.CancelReservation(ctx, order.ReservationID)
    if err != nil {
        // Log but don't fail compensation
        log.Error("Failed to cancel reservation", 
            "reservation_id", order.ReservationID,
            "error", err,
        )
    }
    
    return nil
}
```

## Saga Configuration

### Timeout

```go
saga := saga.NewSaga("process-payment").
    WithTimeout(5 * time.Minute).
    AddStep(&ValidatePaymentStep{}).
    AddStep(&ProcessPaymentStep{})
```

### Retry

```go
saga := saga.NewSaga("create-account").
    WithRetry(3, 1*time.Second).
    AddStep(&CreateUserStep{}).
    AddStep(&SendWelcomeEmailStep{})
```

## Saga Coordinator

Manage multiple saga executions:

```go
// Create coordinator
coordinator := saga.NewCoordinator()

// Register sagas
coordinator.Register(createOrderSaga)
coordinator.Register(cancelOrderSaga)
coordinator.Register(refundOrderSaga)

// Start a saga execution
executionID, err := coordinator.Start(ctx, "create-order", orderData)
if err != nil {
    return err
}

// Check status
status := coordinator.GetStatus(executionID)
fmt.Printf("Saga %s status: %s\n", executionID, status.Status)
```

## Distributed Saga

For microservices architectures with event-driven communication:

```go
// Create event store
eventStore := NewEventStore() // Your event store implementation

// Create distributed saga
saga := saga.NewDistributedSaga("order-processing", eventStore)

// Add steps that publish events
saga.AddStep(saga.NewDistributedStep(
    "create-order",
    func(ctx context.Context, data interface{}) error {
        // Create order and publish OrderCreated event
        order := createOrder(data)
        return publishEvent("OrderCreated", order)
    },
    func(ctx context.Context, data interface{}) error {
        // Compensate by publishing OrderCancelled event
        return publishEvent("OrderCancelled", data)
    },
))
```

## Saga Builder Pattern

For complex sagas with many steps:

```go
saga := saga.NewSagaBuilder("order-fulfillment").
    WithTimeout(10 * time.Minute).
    WithRetry(3, 2 * time.Second).
    WithStep("validate-order",
        func(ctx context.Context, data interface{}) error {
            return validateOrder(data.(*Order))
        },
        func(ctx context.Context, data interface{}) error {
            // No compensation needed for validation
            return nil
        },
    ).
    WithStep("reserve-inventory",
        func(ctx context.Context, data interface{}) error {
            return reserveInventory(data.(*Order))
        },
        func(ctx context.Context, data interface{}) error {
            return releaseInventory(data.(*Order))
        },
    ).
    WithStep("charge-payment",
        func(ctx context.Context, data interface{}) error {
            return chargePayment(data.(*Order))
        },
        func(ctx context.Context, data interface{}) error {
            return refundPayment(data.(*Order))
        },
    ).
    Build()
```

## Error Handling

### Compensation Failures

```go
type CompensationLogger struct {
    logger log.Logger
}

func (c *CompensationLogger) Execute(ctx context.Context, data interface{}) error {
    // Forward action
    return nil
}

func (c *CompensationLogger) Compensate(ctx context.Context, data interface{}) error {
    err := performCompensation(data)
    if err != nil {
        // Log compensation failure but don't propagate
        c.logger.Error("Compensation failed",
            "step", c.Name(),
            "error", err,
            "data", data,
        )
        
        // Optionally send to dead letter queue
        sendToDeadLetterQueue(data, err)
    }
    return nil // Always return nil to continue compensation
}
```

### Partial Failures

```go
type PartialSuccessStep struct{}

func (s *PartialSuccessStep) Execute(ctx context.Context, data interface{}) error {
    order := data.(*Order)
    
    var succeeded []string
    var failed []string
    
    for _, item := range order.Items {
        if err := processItem(item); err != nil {
            failed = append(failed, item.ID)
        } else {
            succeeded = append(succeeded, item.ID)
        }
    }
    
    // Store for compensation
    order.SucceededItems = succeeded
    order.FailedItems = failed
    
    if len(failed) > 0 {
        return fmt.Errorf("partial failure: %d items failed", len(failed))
    }
    
    return nil
}

func (s *PartialSuccessStep) Compensate(ctx context.Context, data interface{}) error {
    order := data.(*Order)
    
    // Only compensate succeeded items
    for _, itemID := range order.SucceededItems {
        if err := revertItem(itemID); err != nil {
            log.Error("Failed to revert item", "item_id", itemID, "error", err)
        }
    }
    
    return nil
}
```

## Monitoring and Observability

### Saga Metrics

```go
type SagaMetrics struct {
    Started   int64
    Completed int64
    Failed    int64
    Compensated int64
    Duration  map[string]time.Duration
}

func (m *SagaMetrics) RecordExecution(saga string, duration time.Duration, err error) {
    if err == nil {
        atomic.AddInt64(&m.Completed, 1)
    } else {
        atomic.AddInt64(&m.Failed, 1)
    }
    m.Duration[saga] = duration
}
```

### Distributed Tracing

```go
type TracedStep struct {
    step   saga.Step
    tracer trace.Tracer
}

func (t *TracedStep) Execute(ctx context.Context, data interface{}) error {
    ctx, span := t.tracer.Start(ctx, fmt.Sprintf("saga.step.%s.execute", t.Name()))
    defer span.End()
    
    err := t.step.Execute(ctx, data)
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
    }
    
    return err
}
```

## Patterns and Best Practices

### 1. Idempotent Steps

```go
func (s *PaymentStep) Execute(ctx context.Context, data interface{}) error {
    order := data.(*Order)
    
    // Check if already processed
    if order.PaymentID != "" {
        payment, err := s.paymentService.Get(ctx, order.PaymentID)
        if err == nil && payment.Status == "completed" {
            // Already processed, skip
            return nil
        }
    }
    
    // Process payment
    payment, err := s.paymentService.Charge(ctx, order.Amount)
    if err != nil {
        return err
    }
    
    order.PaymentID = payment.ID
    return nil
}
```

### 2. Saga State Persistence

```go
type PersistentSaga struct {
    saga       *saga.Saga
    repository SagaRepository
}

func (p *PersistentSaga) Execute(ctx context.Context, data interface{}) error {
    // Create saga instance
    instance := &SagaInstance{
        ID:     uuid.New().String(),
        Type:   p.saga.Name(),
        Data:   data,
        Status: "running",
    }
    
    // Save initial state
    if err := p.repository.Save(ctx, instance); err != nil {
        return err
    }
    
    // Execute with updates
    err := p.saga.Execute(ctx, data)
    
    // Update final state
    if err != nil {
        instance.Status = "failed"
        instance.Error = err.Error()
    } else {
        instance.Status = "completed"
    }
    
    p.repository.Update(ctx, instance)
    return err
}
```

### 3. Compensation Strategies

```go
// Immediate compensation
type ImmediateCompensation struct{}

func (i *ImmediateCompensation) Compensate(ctx context.Context, data interface{}) error {
    // Compensate immediately
    return revertAction(data)
}

// Scheduled compensation
type ScheduledCompensation struct {
    scheduler Scheduler
}

func (s *ScheduledCompensation) Compensate(ctx context.Context, data interface{}) error {
    // Schedule compensation for later
    return s.scheduler.Schedule(func() error {
        return revertAction(data)
    }, 5*time.Minute)
}

// Manual compensation
type ManualCompensation struct {
    notifier Notifier
}

func (m *ManualCompensation) Compensate(ctx context.Context, data interface{}) error {
    // Notify for manual intervention
    return m.notifier.NotifyOperations("Manual compensation required", data)
}
```

## Testing

```go
func TestSagaExecution(t *testing.T) {
    // Create mock steps
    step1 := &MockStep{
        name: "step1",
        executeFunc: func(ctx context.Context, data interface{}) error {
            return nil
        },
    }
    
    step2 := &MockStep{
        name: "step2",
        executeFunc: func(ctx context.Context, data interface{}) error {
            return errors.New("step2 failed")
        },
    }
    
    // Create saga
    testSaga := saga.NewSaga("test-saga").
        AddStep(step1).
        AddStep(step2)
    
    // Execute
    err := testSaga.Execute(context.Background(), nil)
    
    // Verify
    assert.Error(t, err)
    assert.True(t, step1.executed)
    assert.True(t, step2.executed)
    assert.True(t, step1.compensated)
    assert.False(t, step2.compensated) // Failed step doesn't compensate
}
```