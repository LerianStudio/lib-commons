# Event Sourcing

The event sourcing package provides a foundation for implementing event-sourced systems with support for aggregates, event stores, projections, and event bus integration.

## Table of Contents

- [Overview](#overview)
- [Core Concepts](#core-concepts)
- [Event Store](#event-store)
- [Aggregates](#aggregates)
- [Event Bus](#event-bus)
- [Projections](#projections)
- [Event Handlers](#event-handlers)
- [Best Practices](#best-practices)

## Overview

Event sourcing is a pattern where state changes are stored as a sequence of events. Instead of storing just the current state, we store all events that led to the current state.

### Benefits

- Complete audit log of all changes
- Ability to replay events for debugging
- Time travel (view state at any point in time)
- Event-driven architecture support
- Eventual consistency patterns
- CQRS (Command Query Responsibility Segregation) support

### Features

- Event store abstraction with multiple implementations
- Aggregate root pattern support
- Event versioning and migration
- Snapshot support for performance
- Event bus for publishing events
- Projection support for read models
- Event handler chaining

## Core Concepts

### Events

Events represent something that happened in the past:

```go
// Define domain events
type AccountCreated struct {
    AccountID   string    `json:"account_id"`
    OwnerID     string    `json:"owner_id"`
    Currency    string    `json:"currency"`
    CreatedAt   time.Time `json:"created_at"`
}

type MoneyDeposited struct {
    AccountID   string    `json:"account_id"`
    Amount      int64     `json:"amount"`
    Reference   string    `json:"reference"`
    DepositedAt time.Time `json:"deposited_at"`
}

type MoneyWithdrawn struct {
    AccountID   string    `json:"account_id"`
    Amount      int64     `json:"amount"`
    Reference   string    `json:"reference"`
    WithdrawnAt time.Time `json:"withdrawn_at"`
}

// Implement Event interface
func (e AccountCreated) EventType() string { return "AccountCreated" }
func (e AccountCreated) EventVersion() int { return 1 }
func (e AccountCreated) EventTime() time.Time { return e.CreatedAt }
func (e AccountCreated) AggregateID() string { return e.AccountID }
func (e AccountCreated) AggregateType() string { return "Account" }
```

### Event Metadata

```go
type EventMetadata struct {
    UserID        string            `json:"user_id"`
    CorrelationID string            `json:"correlation_id"`
    CausationID   string            `json:"causation_id"`
    Metadata      map[string]string `json:"metadata"`
}

// Enrich events with metadata
event := &AccountCreated{
    AccountID: "acc_123",
    OwnerID:   "user_456",
    Currency:  "USD",
    CreatedAt: time.Now(),
}

metadata := EventMetadata{
    UserID:        "user_456",
    CorrelationID: "corr_789",
    CausationID:   "cmd_012",
    Metadata: map[string]string{
        "ip_address": "192.168.1.1",
        "user_agent": "Mozilla/5.0",
    },
}
```

## Event Store

### Basic Usage

```go
import (
    "github.com/yourusername/commons-go/commons/eventsourcing"
)

// Create event store
store := eventsourcing.NewMemoryEventStore()

// Store events
events := []eventsourcing.Event{
    &AccountCreated{
        AccountID: "acc_123",
        OwnerID:   "user_456",
        Currency:  "USD",
        CreatedAt: time.Now(),
    },
    &MoneyDeposited{
        AccountID:   "acc_123",
        Amount:      10000, // $100.00
        Reference:   "DEP001",
        DepositedAt: time.Now(),
    },
}

err := store.SaveEvents(ctx, "acc_123", events, -1)
if err != nil {
    log.Fatal(err)
}

// Load events
loadedEvents, err := store.LoadEvents(ctx, "acc_123", 0)
if err != nil {
    log.Fatal(err)
}
```

### Event Store Implementations

```go
// PostgreSQL event store
pgStore := eventsourcing.NewPostgresEventStore(db, eventsourcing.PostgresConfig{
    EventsTable:    "events",
    SnapshotsTable: "snapshots",
})

// MongoDB event store
mongoStore := eventsourcing.NewMongoEventStore(mongoClient, eventsourcing.MongoConfig{
    Database:           "eventstore",
    EventsCollection:   "events",
    SnapshotsCollection: "snapshots",
})

// Event store with compression
compressedStore := eventsourcing.NewCompressedEventStore(pgStore, eventsourcing.CompressionGzip)
```

## Aggregates

Aggregates are the core building blocks in event sourcing:

```go
type Account struct {
    eventsourcing.AggregateBase
    ID       string
    OwnerID  string
    Balance  int64
    Currency string
    Status   string
}

// Apply events to rebuild state
func (a *Account) Apply(event eventsourcing.Event) error {
    switch e := event.(type) {
    case *AccountCreated:
        a.ID = e.AccountID
        a.OwnerID = e.OwnerID
        a.Currency = e.Currency
        a.Status = "active"
        a.Balance = 0
        
    case *MoneyDeposited:
        a.Balance += e.Amount
        
    case *MoneyWithdrawn:
        a.Balance -= e.Amount
        
    default:
        return fmt.Errorf("unknown event type: %T", e)
    }
    
    return nil
}

// Command handlers that produce events
func (a *Account) CreateAccount(ownerID, currency string) error {
    if a.ID != "" {
        return errors.New("account already exists")
    }
    
    event := &AccountCreated{
        AccountID: uuid.New().String(),
        OwnerID:   ownerID,
        Currency:  currency,
        CreatedAt: time.Now(),
    }
    
    a.AddEvent(event)
    return a.Apply(event)
}

func (a *Account) Deposit(amount int64, reference string) error {
    if a.Status != "active" {
        return errors.New("account is not active")
    }
    
    if amount <= 0 {
        return errors.New("amount must be positive")
    }
    
    event := &MoneyDeposited{
        AccountID:   a.ID,
        Amount:      amount,
        Reference:   reference,
        DepositedAt: time.Now(),
    }
    
    a.AddEvent(event)
    return a.Apply(event)
}

func (a *Account) Withdraw(amount int64, reference string) error {
    if a.Status != "active" {
        return errors.New("account is not active")
    }
    
    if amount <= 0 {
        return errors.New("amount must be positive")
    }
    
    if a.Balance < amount {
        return errors.New("insufficient balance")
    }
    
    event := &MoneyWithdrawn{
        AccountID:   a.ID,
        Amount:      amount,
        Reference:   reference,
        WithdrawnAt: time.Now(),
    }
    
    a.AddEvent(event)
    return a.Apply(event)
}
```

### Aggregate Repository

```go
type AccountRepository struct {
    store eventsourcing.EventStore
    bus   eventsourcing.EventBus
}

func NewAccountRepository(store eventsourcing.EventStore, bus eventsourcing.EventBus) *AccountRepository {
    return &AccountRepository{
        store: store,
        bus:   bus,
    }
}

func (r *AccountRepository) Load(ctx context.Context, id string) (*Account, error) {
    events, err := r.store.LoadEvents(ctx, id, 0)
    if err != nil {
        return nil, err
    }
    
    if len(events) == 0 {
        return nil, errors.New("account not found")
    }
    
    account := &Account{}
    for _, event := range events {
        if err := account.Apply(event); err != nil {
            return nil, err
        }
    }
    
    account.MarkUnchanged()
    return account, nil
}

func (r *AccountRepository) Save(ctx context.Context, account *Account) error {
    uncommittedEvents := account.GetUncommittedEvents()
    if len(uncommittedEvents) == 0 {
        return nil // No changes
    }
    
    // Save events to store
    err := r.store.SaveEvents(ctx, account.ID, uncommittedEvents, account.Version())
    if err != nil {
        return err
    }
    
    // Publish events to bus
    for _, event := range uncommittedEvents {
        if err := r.bus.Publish(ctx, event); err != nil {
            // Log error but don't fail the save
            log.Printf("Failed to publish event: %v", err)
        }
    }
    
    account.MarkUnchanged()
    return nil
}
```

## Event Bus

The event bus enables event-driven communication:

```go
// Create event bus
bus := eventsourcing.NewEventBus()

// Subscribe to events
unsubscribe := bus.Subscribe("AccountCreated", func(ctx context.Context, event eventsourcing.Event) error {
    created := event.(*AccountCreated)
    log.Printf("Account created: %s", created.AccountID)
    
    // Send welcome email
    return emailService.SendWelcome(created.OwnerID)
})
defer unsubscribe()

// Subscribe with pattern
bus.SubscribePattern("Money*", func(ctx context.Context, event eventsourcing.Event) error {
    // Handle all money-related events
    switch e := event.(type) {
    case *MoneyDeposited:
        return notifyDeposit(e)
    case *MoneyWithdrawn:
        return notifyWithdrawal(e)
    }
    return nil
})

// Publish events
err := bus.Publish(ctx, &AccountCreated{
    AccountID: "acc_123",
    OwnerID:   "user_456",
    Currency:  "USD",
    CreatedAt: time.Now(),
})
```

### Distributed Event Bus

```go
// RabbitMQ event bus
rabbitBus := eventsourcing.NewRabbitMQEventBus(amqpConn, eventsourcing.RabbitMQConfig{
    Exchange:     "events",
    ExchangeType: "topic",
    Durable:      true,
})

// Kafka event bus
kafkaBus := eventsourcing.NewKafkaEventBus(kafkaClient, eventsourcing.KafkaConfig{
    Topic:           "domain-events",
    NumPartitions:   10,
    ReplicationFactor: 3,
})

// Event bus with retry
retryBus := eventsourcing.NewRetryEventBus(rabbitBus, eventsourcing.RetryConfig{
    MaxRetries:     3,
    InitialBackoff: 1 * time.Second,
    MaxBackoff:     30 * time.Second,
})
```

## Projections

Projections build read models from events:

```go
type AccountProjection struct {
    ID       string
    OwnerID  string
    Balance  int64
    Currency string
    Status   string
    UpdatedAt time.Time
}

type AccountProjector struct {
    db *sql.DB
}

func (p *AccountProjector) Project(ctx context.Context, event eventsourcing.Event) error {
    switch e := event.(type) {
    case *AccountCreated:
        _, err := p.db.ExecContext(ctx, `
            INSERT INTO account_projections (id, owner_id, balance, currency, status, updated_at)
            VALUES ($1, $2, $3, $4, $5, $6)
        `, e.AccountID, e.OwnerID, 0, e.Currency, "active", e.CreatedAt)
        return err
        
    case *MoneyDeposited:
        _, err := p.db.ExecContext(ctx, `
            UPDATE account_projections 
            SET balance = balance + $1, updated_at = $2
            WHERE id = $3
        `, e.Amount, e.DepositedAt, e.AccountID)
        return err
        
    case *MoneyWithdrawn:
        _, err := p.db.ExecContext(ctx, `
            UPDATE account_projections 
            SET balance = balance - $1, updated_at = $2
            WHERE id = $3
        `, e.Amount, e.WithdrawnAt, e.AccountID)
        return err
    }
    
    return nil
}

// Rebuild projection from events
func (p *AccountProjector) Rebuild(ctx context.Context, store eventsourcing.EventStore) error {
    // Clear existing projections
    _, err := p.db.ExecContext(ctx, "TRUNCATE account_projections")
    if err != nil {
        return err
    }
    
    // Stream all events
    events, err := store.LoadAllEvents(ctx, "Account", 0)
    if err != nil {
        return err
    }
    
    // Project each event
    for _, event := range events {
        if err := p.Project(ctx, event); err != nil {
            return err
        }
    }
    
    return nil
}
```

### Async Projections

```go
type AsyncProjector struct {
    projectors []eventsourcing.Projector
    workers    int
}

func NewAsyncProjector(workers int, projectors ...eventsourcing.Projector) *AsyncProjector {
    return &AsyncProjector{
        projectors: projectors,
        workers:    workers,
    }
}

func (p *AsyncProjector) Start(ctx context.Context, bus eventsourcing.EventBus) {
    eventChan := make(chan eventsourcing.Event, 1000)
    
    // Subscribe to all events
    bus.Subscribe("*", func(ctx context.Context, event eventsourcing.Event) error {
        select {
        case eventChan <- event:
        case <-ctx.Done():
            return ctx.Err()
        }
        return nil
    })
    
    // Start workers
    var wg sync.WaitGroup
    for i := 0; i < p.workers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            p.worker(ctx, eventChan)
        }()
    }
    
    // Wait for context cancellation
    <-ctx.Done()
    close(eventChan)
    wg.Wait()
}

func (p *AsyncProjector) worker(ctx context.Context, events <-chan eventsourcing.Event) {
    for event := range events {
        for _, projector := range p.projectors {
            if err := projector.Project(ctx, event); err != nil {
                log.Printf("Projection error: %v", err)
                // Could implement retry logic here
            }
        }
    }
}
```

## Event Handlers

### Handler Chains

```go
// Create handler chain
chain := eventsourcing.NewHandlerChain()

// Add handlers in order
chain.Add(eventsourcing.HandlerFunc(func(ctx context.Context, event eventsourcing.Event) error {
    // Logging handler
    log.Printf("Processing event: %s", event.EventType())
    return nil
}))

chain.Add(eventsourcing.HandlerFunc(func(ctx context.Context, event eventsourcing.Event) error {
    // Validation handler
    if event.AggregateID() == "" {
        return errors.New("event missing aggregate ID")
    }
    return nil
}))

chain.Add(eventsourcing.HandlerFunc(func(ctx context.Context, event eventsourcing.Event) error {
    // Business logic handler
    return processBusinessLogic(event)
}))

// Use the chain
err := chain.Handle(ctx, event)
```

### Conditional Handlers

```go
type ConditionalHandler struct {
    condition func(eventsourcing.Event) bool
    handler   eventsourcing.EventHandler
}

func (h *ConditionalHandler) Handle(ctx context.Context, event eventsourcing.Event) error {
    if h.condition(event) {
        return h.handler.Handle(ctx, event)
    }
    return nil
}

// Use conditional handler
handler := &ConditionalHandler{
    condition: func(e eventsourcing.Event) bool {
        // Only handle events from last hour
        return time.Since(e.EventTime()) < time.Hour
    },
    handler: eventsourcing.HandlerFunc(processRecentEvents),
}
```

## Best Practices

### 1. Event Design

```go
// Good: Events are immutable and represent facts
type OrderPlaced struct {
    OrderID    string    `json:"order_id"`
    CustomerID string    `json:"customer_id"`
    Items      []Item    `json:"items"`
    Total      int64     `json:"total"`
    PlacedAt   time.Time `json:"placed_at"`
}

// Bad: Events should not contain logic or behavior
type OrderPlacedWithLogic struct {
    // Don't do this
    CalculateDiscount() float64
    ValidateItems() error
}

// Good: Use past tense for event names
type CustomerRegistered struct{} // ✓
type PaymentReceived struct{}   // ✓

// Bad: Don't use present or future tense
type RegisterCustomer struct{} // ✗
type ReceivePayment struct{}   // ✗
```

### 2. Event Versioning

```go
// Version 1
type UserRegisteredV1 struct {
    UserID string `json:"user_id"`
    Email  string `json:"email"`
}

func (e UserRegisteredV1) EventVersion() int { return 1 }

// Version 2 - Added name field
type UserRegisteredV2 struct {
    UserID string `json:"user_id"`
    Email  string `json:"email"`
    Name   string `json:"name"` // New field
}

func (e UserRegisteredV2) EventVersion() int { return 2 }

// Event upgrader
type EventUpgrader struct{}

func (u *EventUpgrader) Upgrade(event eventsourcing.Event) eventsourcing.Event {
    switch e := event.(type) {
    case *UserRegisteredV1:
        // Upgrade V1 to V2
        return &UserRegisteredV2{
            UserID: e.UserID,
            Email:  e.Email,
            Name:   "", // Default value for new field
        }
    }
    return event
}
```

### 3. Snapshots

```go
type SnapshotStore interface {
    SaveSnapshot(ctx context.Context, aggregateID string, snapshot interface{}, version int) error
    LoadSnapshot(ctx context.Context, aggregateID string) (interface{}, int, error)
}

type SnapshotRepository struct {
    eventStore    eventsourcing.EventStore
    snapshotStore SnapshotStore
    snapshotEvery int
}

func (r *SnapshotRepository) Load(ctx context.Context, id string) (*Account, error) {
    // Try to load from snapshot
    snapshot, version, err := r.snapshotStore.LoadSnapshot(ctx, id)
    if err == nil {
        account := snapshot.(*Account)
        
        // Load events after snapshot
        events, err := r.eventStore.LoadEvents(ctx, id, version+1)
        if err != nil {
            return nil, err
        }
        
        // Apply events after snapshot
        for _, event := range events {
            if err := account.Apply(event); err != nil {
                return nil, err
            }
        }
        
        return account, nil
    }
    
    // No snapshot, load all events
    return r.loadFromEvents(ctx, id)
}

func (r *SnapshotRepository) Save(ctx context.Context, account *Account) error {
    // Save events
    if err := r.eventStore.SaveEvents(ctx, account.ID, account.GetUncommittedEvents(), account.Version()); err != nil {
        return err
    }
    
    // Save snapshot if needed
    if account.Version() % r.snapshotEvery == 0 {
        if err := r.snapshotStore.SaveSnapshot(ctx, account.ID, account, account.Version()); err != nil {
            // Log error but don't fail
            log.Printf("Failed to save snapshot: %v", err)
        }
    }
    
    return nil
}
```

### 4. Command and Event Separation

```go
// Commands represent intent
type CreateAccountCommand struct {
    OwnerID  string
    Currency string
}

type DepositMoneyCommand struct {
    AccountID string
    Amount    int64
    Reference string
}

// Command handler produces events
type AccountCommandHandler struct {
    repo *AccountRepository
}

func (h *AccountCommandHandler) Handle(ctx context.Context, cmd interface{}) error {
    switch c := cmd.(type) {
    case CreateAccountCommand:
        account := &Account{}
        if err := account.CreateAccount(c.OwnerID, c.Currency); err != nil {
            return err
        }
        return h.repo.Save(ctx, account)
        
    case DepositMoneyCommand:
        account, err := h.repo.Load(ctx, c.AccountID)
        if err != nil {
            return err
        }
        
        if err := account.Deposit(c.Amount, c.Reference); err != nil {
            return err
        }
        
        return h.repo.Save(ctx, account)
    }
    
    return errors.New("unknown command")
}
```

### 5. Event Sourcing with CQRS

```go
// Write side - Commands and Events
type WriteService struct {
    commandBus eventsourcing.CommandBus
}

func (s *WriteService) CreateAccount(ctx context.Context, ownerID, currency string) error {
    cmd := CreateAccountCommand{
        OwnerID:  ownerID,
        Currency: currency,
    }
    return s.commandBus.Send(ctx, cmd)
}

// Read side - Queries
type ReadService struct {
    db *sql.DB
}

func (s *ReadService) GetAccount(ctx context.Context, accountID string) (*AccountProjection, error) {
    var account AccountProjection
    err := s.db.QueryRowContext(ctx, `
        SELECT id, owner_id, balance, currency, status, updated_at
        FROM account_projections
        WHERE id = $1
    `, accountID).Scan(
        &account.ID,
        &account.OwnerID,
        &account.Balance,
        &account.Currency,
        &account.Status,
        &account.UpdatedAt,
    )
    return &account, err
}

func (s *ReadService) GetAccountsByOwner(ctx context.Context, ownerID string) ([]*AccountProjection, error) {
    rows, err := s.db.QueryContext(ctx, `
        SELECT id, owner_id, balance, currency, status, updated_at
        FROM account_projections
        WHERE owner_id = $1
        ORDER BY updated_at DESC
    `, ownerID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var accounts []*AccountProjection
    for rows.Next() {
        var account AccountProjection
        err := rows.Scan(
            &account.ID,
            &account.OwnerID,
            &account.Balance,
            &account.Currency,
            &account.Status,
            &account.UpdatedAt,
        )
        if err != nil {
            return nil, err
        }
        accounts = append(accounts, &account)
    }
    
    return accounts, rows.Err()
}
```

### 6. Testing Event Sourced Systems

```go
func TestAccountAggregate(t *testing.T) {
    // Given
    account := &Account{}
    
    // When creating account
    err := account.CreateAccount("user_123", "USD")
    assert.NoError(t, err)
    
    // Then
    assert.Equal(t, "user_123", account.OwnerID)
    assert.Equal(t, "USD", account.Currency)
    assert.Equal(t, int64(0), account.Balance)
    assert.Len(t, account.GetUncommittedEvents(), 1)
    
    // When depositing money
    err = account.Deposit(10000, "DEP001")
    assert.NoError(t, err)
    
    // Then
    assert.Equal(t, int64(10000), account.Balance)
    assert.Len(t, account.GetUncommittedEvents(), 2)
    
    // When withdrawing money
    err = account.Withdraw(5000, "WTH001")
    assert.NoError(t, err)
    
    // Then
    assert.Equal(t, int64(5000), account.Balance)
    assert.Len(t, account.GetUncommittedEvents(), 3)
    
    // Verify events
    events := account.GetUncommittedEvents()
    assert.IsType(t, &AccountCreated{}, events[0])
    assert.IsType(t, &MoneyDeposited{}, events[1])
    assert.IsType(t, &MoneyWithdrawn{}, events[2])
}

// Test event replay
func TestEventReplay(t *testing.T) {
    events := []eventsourcing.Event{
        &AccountCreated{
            AccountID: "acc_123",
            OwnerID:   "user_456",
            Currency:  "USD",
            CreatedAt: time.Now(),
        },
        &MoneyDeposited{
            AccountID:   "acc_123",
            Amount:      10000,
            Reference:   "DEP001",
            DepositedAt: time.Now(),
        },
        &MoneyWithdrawn{
            AccountID:   "acc_123",
            Amount:      3000,
            Reference:   "WTH001",
            WithdrawnAt: time.Now(),
        },
    }
    
    // Replay events
    account := &Account{}
    for _, event := range events {
        err := account.Apply(event)
        assert.NoError(t, err)
    }
    
    // Verify final state
    assert.Equal(t, "acc_123", account.ID)
    assert.Equal(t, int64(7000), account.Balance)
}
```

### 7. Production Considerations

```go
// Event store with monitoring
type MonitoredEventStore struct {
    store   eventsourcing.EventStore
    metrics *prometheus.Registry
}

func (s *MonitoredEventStore) SaveEvents(ctx context.Context, aggregateID string, events []eventsourcing.Event, expectedVersion int) error {
    timer := prometheus.NewTimer(s.metrics.NewHistogram(
        prometheus.HistogramOpts{
            Name: "event_store_save_duration_seconds",
        },
    ))
    defer timer.ObserveDuration()
    
    err := s.store.SaveEvents(ctx, aggregateID, events, expectedVersion)
    
    counter := s.metrics.NewCounter(prometheus.CounterOpts{
        Name: "event_store_events_saved_total",
    })
    
    if err != nil {
        counter.WithLabelValues("error").Add(float64(len(events)))
        return err
    }
    
    counter.WithLabelValues("success").Add(float64(len(events)))
    return nil
}

// Event store with caching
type CachedEventStore struct {
    store eventsourcing.EventStore
    cache cache.Cache
}

func (s *CachedEventStore) LoadEvents(ctx context.Context, aggregateID string, afterVersion int) ([]eventsourcing.Event, error) {
    cacheKey := fmt.Sprintf("events:%s:%d", aggregateID, afterVersion)
    
    // Try cache first
    if cached, exists, err := s.cache.Get(ctx, cacheKey); err == nil && exists {
        return cached.([]eventsourcing.Event), nil
    }
    
    // Load from store
    events, err := s.store.LoadEvents(ctx, aggregateID, afterVersion)
    if err != nil {
        return nil, err
    }
    
    // Cache for future requests (only if loading all events)
    if afterVersion == 0 && len(events) > 0 {
        s.cache.Set(ctx, cacheKey, events, 5*time.Minute)
    }
    
    return events, nil
}
```

## Examples

### Complete Example: Order Processing System

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"
    
    "github.com/yourusername/commons-go/commons/eventsourcing"
)

// Domain Events
type OrderPlaced struct {
    OrderID    string    `json:"order_id"`
    CustomerID string    `json:"customer_id"`
    Items      []Item    `json:"items"`
    Total      int64     `json:"total"`
    PlacedAt   time.Time `json:"placed_at"`
}

type OrderPaid struct {
    OrderID      string    `json:"order_id"`
    PaymentID    string    `json:"payment_id"`
    Amount       int64     `json:"amount"`
    PaymentMethod string   `json:"payment_method"`
    PaidAt       time.Time `json:"paid_at"`
}

type OrderShipped struct {
    OrderID        string    `json:"order_id"`
    TrackingNumber string    `json:"tracking_number"`
    Carrier        string    `json:"carrier"`
    ShippedAt      time.Time `json:"shipped_at"`
}

type OrderDelivered struct {
    OrderID      string    `json:"order_id"`
    DeliveredAt  time.Time `json:"delivered_at"`
    SignedBy     string    `json:"signed_by"`
}

type Item struct {
    ProductID string `json:"product_id"`
    Quantity  int    `json:"quantity"`
    Price     int64  `json:"price"`
}

// Implement Event interface for all events
func (e OrderPlaced) EventType() string { return "OrderPlaced" }
func (e OrderPlaced) AggregateID() string { return e.OrderID }
func (e OrderPlaced) AggregateType() string { return "Order" }
func (e OrderPlaced) EventVersion() int { return 1 }
func (e OrderPlaced) EventTime() time.Time { return e.PlacedAt }

// Order Aggregate
type Order struct {
    eventsourcing.AggregateBase
    ID            string
    CustomerID    string
    Items         []Item
    Total         int64
    Status        string
    PaymentID     string
    TrackingNumber string
}

func (o *Order) Apply(event eventsourcing.Event) error {
    switch e := event.(type) {
    case *OrderPlaced:
        o.ID = e.OrderID
        o.CustomerID = e.CustomerID
        o.Items = e.Items
        o.Total = e.Total
        o.Status = "placed"
        
    case *OrderPaid:
        o.PaymentID = e.PaymentID
        o.Status = "paid"
        
    case *OrderShipped:
        o.TrackingNumber = e.TrackingNumber
        o.Status = "shipped"
        
    case *OrderDelivered:
        o.Status = "delivered"
    }
    
    return nil
}

func (o *Order) PlaceOrder(customerID string, items []Item) error {
    if o.ID != "" {
        return fmt.Errorf("order already exists")
    }
    
    var total int64
    for _, item := range items {
        total += item.Price * int64(item.Quantity)
    }
    
    event := &OrderPlaced{
        OrderID:    fmt.Sprintf("ORD-%d", time.Now().Unix()),
        CustomerID: customerID,
        Items:      items,
        Total:      total,
        PlacedAt:   time.Now(),
    }
    
    o.AddEvent(event)
    return o.Apply(event)
}

func (o *Order) RecordPayment(paymentID string, amount int64, method string) error {
    if o.Status != "placed" {
        return fmt.Errorf("order is not in placed status")
    }
    
    if amount != o.Total {
        return fmt.Errorf("payment amount does not match order total")
    }
    
    event := &OrderPaid{
        OrderID:       o.ID,
        PaymentID:     paymentID,
        Amount:        amount,
        PaymentMethod: method,
        PaidAt:        time.Now(),
    }
    
    o.AddEvent(event)
    return o.Apply(event)
}

// Event Handlers
type InventoryService struct{}

func (s *InventoryService) HandleOrderPlaced(ctx context.Context, event eventsourcing.Event) error {
    order := event.(*OrderPlaced)
    
    // Reserve inventory
    for _, item := range order.Items {
        log.Printf("Reserving %d units of product %s for order %s",
            item.Quantity, item.ProductID, order.OrderID)
    }
    
    return nil
}

type EmailService struct{}

func (s *EmailService) HandleOrderEvents(ctx context.Context, event eventsourcing.Event) error {
    switch e := event.(type) {
    case *OrderPlaced:
        log.Printf("Sending order confirmation email for order %s", e.OrderID)
        
    case *OrderShipped:
        log.Printf("Sending shipping notification for order %s with tracking %s",
            e.OrderID, e.TrackingNumber)
        
    case *OrderDelivered:
        log.Printf("Sending delivery confirmation for order %s", e.OrderID)
    }
    
    return nil
}

// Main application
func main() {
    ctx := context.Background()
    
    // Setup event store and bus
    eventStore := eventsourcing.NewMemoryEventStore()
    eventBus := eventsourcing.NewEventBus()
    
    // Setup services
    inventoryService := &InventoryService{}
    emailService := &EmailService{}
    
    // Subscribe to events
    eventBus.Subscribe("OrderPlaced", inventoryService.HandleOrderPlaced)
    eventBus.Subscribe("Order*", emailService.HandleOrderEvents)
    
    // Create repository
    repo := &OrderRepository{
        store: eventStore,
        bus:   eventBus,
    }
    
    // Place an order
    order := &Order{}
    err := order.PlaceOrder("CUST-123", []Item{
        {ProductID: "PROD-001", Quantity: 2, Price: 2999},
        {ProductID: "PROD-002", Quantity: 1, Price: 4999},
    })
    if err != nil {
        log.Fatal(err)
    }
    
    // Save order (triggers event handlers)
    err = repo.Save(ctx, order)
    if err != nil {
        log.Fatal(err)
    }
    
    log.Printf("Order placed: %s", order.ID)
    
    // Simulate payment
    err = order.RecordPayment("PAY-456", order.Total, "credit_card")
    if err != nil {
        log.Fatal(err)
    }
    
    err = repo.Save(ctx, order)
    if err != nil {
        log.Fatal(err)
    }
    
    log.Printf("Order paid: %s", order.ID)
}

type OrderRepository struct {
    store eventsourcing.EventStore
    bus   eventsourcing.EventBus
}

func (r *OrderRepository) Save(ctx context.Context, order *Order) error {
    events := order.GetUncommittedEvents()
    if len(events) == 0 {
        return nil
    }
    
    err := r.store.SaveEvents(ctx, order.ID, events, order.Version())
    if err != nil {
        return err
    }
    
    // Publish events
    for _, event := range events {
        if err := r.bus.Publish(ctx, event); err != nil {
            log.Printf("Failed to publish event: %v", err)
        }
    }
    
    order.MarkUnchanged()
    return nil
}
```