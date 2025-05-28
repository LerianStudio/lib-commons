package eventsourcing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrConcurrentModification is returned when an aggregate has been modified concurrently
	ErrConcurrentModification = errors.New("concurrent modification detected")
	// ErrAggregateNotFound is returned when an aggregate doesn't exist
	ErrAggregateNotFound = errors.New("aggregate not found")
	// ErrInvalidVersion is returned when the version is invalid
	ErrInvalidVersion = errors.New("invalid version")
	// ErrNoEvents is returned when no events are provided
	ErrNoEvents = errors.New("no events provided")
)

// Event represents a domain event
type Event struct {
	ID          string
	AggregateID string
	Type        string
	Data        interface{}
	Version     int
	Timestamp   time.Time
	Metadata    map[string]interface{}
}

// Aggregate represents an event-sourced aggregate
type Aggregate interface {
	// GetID returns the aggregate ID
	GetID() string
	// GetVersion returns the current version
	GetVersion() int
	// Apply applies an event to the aggregate
	Apply(event Event) error
}

// EventStore defines the interface for storing and retrieving events
type EventStore interface {
	// Append appends events to a stream
	Append(ctx context.Context, streamID string, events []Event, expectedVersion int) error
	// Load loads events from a stream
	Load(ctx context.Context, streamID string, fromVersion int) ([]Event, error)
	// LoadSnapshot loads the latest snapshot for a stream
	LoadSnapshot(ctx context.Context, streamID string) (*Snapshot, error)
	// SaveSnapshot saves a snapshot
	SaveSnapshot(ctx context.Context, snapshot Snapshot) error
}

// Snapshot represents an aggregate snapshot
type Snapshot struct {
	AggregateID string
	Data        interface{}
	Version     int
	Timestamp   time.Time
}

// Repository provides methods for loading and saving aggregates
type Repository struct {
	store            EventStore
	aggregateFactory func() Aggregate
	snapshotFreq     int
}

// RepositoryOption configures a Repository
type RepositoryOption func(*Repository)

// WithSnapshotFrequency sets how often to take snapshots
func WithSnapshotFrequency(freq int) RepositoryOption {
	return func(r *Repository) {
		r.snapshotFreq = freq
	}
}

// NewRepository creates a new repository
func NewRepository(store EventStore, factory func() Aggregate, opts ...RepositoryOption) *Repository {
	r := &Repository{
		store:            store,
		aggregateFactory: factory,
		snapshotFreq:     10, // Default snapshot every 10 events
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// Get loads an aggregate by ID
func (r *Repository) Get(ctx context.Context, id string) (Aggregate, error) {
	aggregate := r.aggregateFactory()

	// Try to load from snapshot
	snapshot, err := r.store.LoadSnapshot(ctx, id)
	if err == nil && snapshot != nil {
		// Apply snapshot
		// In a real implementation, you'd deserialize the snapshot data
		// For now, we'll just load all events
	}

	// Load events
	fromVersion := 0
	if snapshot != nil {
		fromVersion = snapshot.Version
	}

	events, err := r.store.Load(ctx, id, fromVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to load events: %w", err)
	}

	if len(events) == 0 && snapshot == nil {
		return nil, ErrAggregateNotFound
	}

	// Apply events to aggregate
	for _, event := range events {
		if err := aggregate.Apply(event); err != nil {
			return nil, fmt.Errorf("failed to apply event: %w", err)
		}
	}

	return aggregate, nil
}

// Save saves an aggregate with its new events
func (r *Repository) Save(ctx context.Context, aggregate Aggregate, events []Event) error {
	if len(events) == 0 {
		return ErrNoEvents
	}

	// Append events
	expectedVersion := aggregate.GetVersion() - len(events)
	if err := r.store.Append(ctx, aggregate.GetID(), events, expectedVersion); err != nil {
		return fmt.Errorf("failed to append events: %w", err)
	}

	// Check if we should take a snapshot
	if r.snapshotFreq > 0 && aggregate.GetVersion()%r.snapshotFreq == 0 {
		snapshot := Snapshot{
			AggregateID: aggregate.GetID(),
			Data:        aggregate,
			Version:     aggregate.GetVersion(),
			Timestamp:   time.Now(),
		}
		if err := r.store.SaveSnapshot(ctx, snapshot); err != nil {
			// Log error but don't fail the save
			// In production, you'd use proper logging
		}
	}

	return nil
}

// EventListener is a function that handles events
type EventListener func(event Event)

// EventHandler defines the interface for handling events
type EventHandler interface {
	Handle(ctx context.Context, event Event) error
}

// EventHandlerFunc is a function adapter for EventHandler
type EventHandlerFunc func(ctx context.Context, event Event) error

// Handle implements EventHandler
func (f EventHandlerFunc) Handle(ctx context.Context, event Event) error {
	return f(ctx, event)
}

// ChainHandlers creates a handler that calls multiple handlers in sequence
func ChainHandlers(handlers ...EventHandler) EventHandler {
	return EventHandlerFunc(func(ctx context.Context, event Event) error {
		for _, handler := range handlers {
			if err := handler.Handle(ctx, event); err != nil {
				return err
			}
		}

		return nil
	})
}

// EventBus provides pub/sub functionality for events
type EventBus struct {
	mu         sync.RWMutex
	handlers   map[string][]EventListener
	middleware []EventHandler
}

// NewEventBus creates a new event bus
func NewEventBus() *EventBus {
	return &EventBus{
		handlers: make(map[string][]EventListener),
	}
}

// Subscribe registers a listener for specific event types
func (eb *EventBus) Subscribe(eventType string, listener EventListener) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	eb.handlers[eventType] = append(eb.handlers[eventType], listener)
}

// SubscribeAll registers a listener for all events
func (eb *EventBus) SubscribeAll(listener EventListener) {
	eb.Subscribe("*", listener)
}

// Use adds middleware to the event bus
func (eb *EventBus) Use(middleware EventHandler) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	eb.middleware = append(eb.middleware, middleware)
}

// Publish publishes an event to all subscribers
func (eb *EventBus) Publish(ctx context.Context, event Event) error {
	// Run middleware
	for _, mw := range eb.middleware {
		if err := mw.Handle(ctx, event); err != nil {
			return err
		}
	}

	eb.mu.RLock()
	defer eb.mu.RUnlock()

	// Notify specific handlers
	if handlers, ok := eb.handlers[event.Type]; ok {
		for _, handler := range handlers {
			go handler(event)
		}
	}

	// Notify wildcard handlers
	if handlers, ok := eb.handlers["*"]; ok {
		for _, handler := range handlers {
			go handler(event)
		}
	}

	return nil
}

// Projection defines the interface for projections
type Projection interface {
	// Apply applies an event to update the projection
	Apply(event Event)
	// Reset resets the projection to its initial state
	Reset()
}

// ProjectionBuilder helps build projections from events
type ProjectionBuilder struct {
	projections []Projection
	eventStore  EventStore
}

// NewProjectionBuilder creates a new projection builder
func NewProjectionBuilder(store EventStore) *ProjectionBuilder {
	return &ProjectionBuilder{
		eventStore: store,
	}
}

// Add adds a projection to be built
func (pb *ProjectionBuilder) Add(projection Projection) *ProjectionBuilder {
	pb.projections = append(pb.projections, projection)
	return pb
}

// Build builds all projections from the event store
func (pb *ProjectionBuilder) Build(ctx context.Context, streamIDs []string) error {
	// Reset all projections
	for _, proj := range pb.projections {
		proj.Reset()
	}

	// Load and apply events from each stream
	for _, streamID := range streamIDs {
		events, err := pb.eventStore.Load(ctx, streamID, 0)
		if err != nil {
			return fmt.Errorf("failed to load events for stream %s: %w", streamID, err)
		}

		for _, event := range events {
			for _, proj := range pb.projections {
				proj.Apply(event)
			}
		}
	}

	return nil
}

// Rebuild rebuilds a specific projection
func (pb *ProjectionBuilder) Rebuild(ctx context.Context, projection Projection, streamIDs []string) error {
	projection.Reset()

	for _, streamID := range streamIDs {
		events, err := pb.eventStore.Load(ctx, streamID, 0)
		if err != nil {
			return fmt.Errorf("failed to load events for stream %s: %w", streamID, err)
		}

		for _, event := range events {
			projection.Apply(event)
		}
	}

	return nil
}

// EventProcessor processes events in order
type EventProcessor struct {
	handlers map[string]EventHandler
	mu       sync.RWMutex
}

// NewEventProcessor creates a new event processor
func NewEventProcessor() *EventProcessor {
	return &EventProcessor{
		handlers: make(map[string]EventHandler),
	}
}

// Register registers a handler for an event type
func (ep *EventProcessor) Register(eventType string, handler EventHandler) {
	ep.mu.Lock()
	defer ep.mu.Unlock()

	ep.handlers[eventType] = handler
}

// Process processes an event
func (ep *EventProcessor) Process(ctx context.Context, event Event) error {
	ep.mu.RLock()
	handler, ok := ep.handlers[event.Type]
	ep.mu.RUnlock()

	if !ok {
		return nil // No handler for this event type
	}

	return handler.Handle(ctx, event)
}

// ProcessBatch processes multiple events in order
func (ep *EventProcessor) ProcessBatch(ctx context.Context, events []Event) error {
	for _, event := range events {
		if err := ep.Process(ctx, event); err != nil {
			return fmt.Errorf("failed to process event %s: %w", event.ID, err)
		}
	}

	return nil
}
