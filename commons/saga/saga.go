// Package saga provides distributed saga pattern implementation for coordinating transactions across services.
// It includes support for step execution, compensation, timeouts, retries, and event publishing.
package saga

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrSagaNotFound is returned when a saga is not found
	ErrSagaNotFound = errors.New("saga not found")
	// ErrStepFailed is returned when a step execution fails
	ErrStepFailed = errors.New("step execution failed")
	// ErrCompensationFailed is returned when compensation fails
	ErrCompensationFailed = errors.New("compensation failed")
	// ErrSagaTimedOut is returned when saga execution times out
	ErrSagaTimedOut = errors.New("saga timed out")
)

// SagaStatus represents the status of a saga execution.
// The type name intentionally matches the package name for clarity in external usage.
type SagaStatus string

const (
	// SagaStatusPending indicates the saga has not started
	SagaStatusPending SagaStatus = "pending"
	// SagaStatusRunning indicates the saga is executing
	SagaStatusRunning SagaStatus = "running"
	// SagaStatusCompleted indicates the saga completed successfully
	SagaStatusCompleted SagaStatus = "completed"
	// SagaStatusFailed indicates the saga failed
	SagaStatusFailed SagaStatus = "failed"
	// SagaStatusCompensating indicates the saga is compensating
	SagaStatusCompensating SagaStatus = "compensating"
	// SagaStatusCompensated indicates the saga has been compensated
	SagaStatusCompensated SagaStatus = "compensated"
)

// Step defines the interface for a saga step
type Step interface {
	// Name returns the step name
	Name() string
	// Execute performs the step's forward action
	Execute(ctx context.Context, data any) error
	// Compensate performs the step's compensating action
	Compensate(ctx context.Context, data any) error
}

// Saga represents a saga transaction
type Saga struct {
	name       string
	steps      []Step
	timeout    time.Duration
	retries    int
	retryDelay time.Duration
}

// NewSaga creates a new saga
func NewSaga(name string) *Saga {
	return &Saga{
		name:    name,
		steps:   make([]Step, 0),
		timeout: 30 * time.Second, // Default timeout
		retries: 0,
	}
}

// AddStep adds a step to the saga
func (s *Saga) AddStep(step Step) *Saga {
	s.steps = append(s.steps, step)
	return s
}

// WithTimeout sets the saga timeout
func (s *Saga) WithTimeout(timeout time.Duration) *Saga {
	s.timeout = timeout
	return s
}

// WithRetry sets retry configuration
func (s *Saga) WithRetry(retries int, delay time.Duration) *Saga {
	s.retries = retries
	s.retryDelay = delay

	return s
}

// Execute runs the saga
func (s *Saga) Execute(ctx context.Context, data any) error {
	// Apply timeout
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}

	// Execute steps
	executedSteps := make([]Step, 0, len(s.steps))

	for _, step := range s.steps {
		// Execute with retry
		err := s.executeStep(ctx, step, data)
		if err != nil {
			// Compensate in reverse order
			compensationErr := s.compensate(ctx, executedSteps, data)
			if compensationErr != nil {
				return fmt.Errorf(
					"%w: %v, compensation error: %v",
					ErrStepFailed,
					err,
					compensationErr,
				)
			}
			// If the error is a context error, return it directly
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}

			return fmt.Errorf("%w: %v", ErrStepFailed, err)
		}

		executedSteps = append(executedSteps, step)
	}

	return nil
}

// executeStep executes a single step with retry logic
func (s *Saga) executeStep(ctx context.Context, step Step, data any) error {
	var lastErr error

	attempts := s.retries + 1

	for i := 0; i < attempts; i++ {
		if i > 0 {
			// Wait before retry
			select {
			case <-time.After(s.retryDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		err := step.Execute(ctx, data)
		if err == nil {
			return nil
		}

		lastErr = err
	}

	return lastErr
}

// compensate runs compensation for executed steps in reverse order
func (s *Saga) compensate(ctx context.Context, executedSteps []Step, data any) error {
	var compensationErrors []error

	// Compensate in reverse order
	for i := len(executedSteps) - 1; i >= 0; i-- {
		step := executedSteps[i]
		if err := step.Compensate(ctx, data); err != nil {
			compensationErrors = append(compensationErrors,
				fmt.Errorf("failed to compensate step %s: %w", step.Name(), err))
		}
	}

	if len(compensationErrors) > 0 {
		return fmt.Errorf("%w: %v", ErrCompensationFailed, compensationErrors)
	}

	return nil
}

// SagaExecution represents a saga execution instance.
// The type name intentionally matches the package name for clarity in external usage.
type SagaExecution struct {
	ID        string
	Name      string
	Status    SagaStatus
	StartTime time.Time
	EndTime   *time.Time
	Error     error
	Data      any
}

// Coordinator manages saga executions
type Coordinator struct {
	mu         sync.RWMutex
	sagas      map[string]*Saga
	executions map[string]*SagaExecution
}

// NewCoordinator creates a new saga coordinator
func NewCoordinator() *Coordinator {
	return &Coordinator{
		sagas:      make(map[string]*Saga),
		executions: make(map[string]*SagaExecution),
	}
}

// Register registers a saga with the coordinator
func (c *Coordinator) Register(saga *Saga) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sagas[saga.name] = saga
}

// Start starts a saga execution
func (c *Coordinator) Start(ctx context.Context, sagaName string, data any) (string, error) {
	c.mu.RLock()
	saga, exists := c.sagas[sagaName]
	c.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("%w: %s", ErrSagaNotFound, sagaName)
	}

	// Create execution
	execution := &SagaExecution{
		ID:        uuid.New().String(),
		Name:      sagaName,
		Status:    SagaStatusRunning,
		StartTime: time.Now(),
		Data:      data,
	}

	// Store execution
	c.mu.Lock()
	c.executions[execution.ID] = execution
	c.mu.Unlock()

	// Execute saga asynchronously
	go func() {
		err := saga.Execute(ctx, data)

		c.mu.Lock()
		defer c.mu.Unlock()

		now := time.Now()
		execution.EndTime = &now

		if err != nil {
			execution.Status = SagaStatusFailed
			execution.Error = err
		} else {
			execution.Status = SagaStatusCompleted
		}
	}()

	return execution.ID, nil
}

// GetStatus returns the status of a saga execution
func (c *Coordinator) GetStatus(executionID string) *SagaExecution {
	c.mu.RLock()
	defer c.mu.RUnlock()

	execution, exists := c.executions[executionID]
	if !exists {
		return nil
	}

	// Return a copy to avoid race conditions
	executionCopy := *execution

	return &executionCopy
}

// Event types for distributed sagas
const (
	EventTypeSagaStarted      = "SagaStarted"
	EventTypeSagaCompleted    = "SagaCompleted"
	EventTypeSagaFailed       = "SagaFailed"
	EventTypeStepStarted      = "StepStarted"
	EventTypeStepCompleted    = "StepCompleted"
	EventTypeStepFailed       = "StepFailed"
	EventTypeStepCompensating = "StepCompensating"
	EventTypeStepCompensated  = "StepCompensated"
)

// Event represents a saga event
type Event struct {
	ID        string
	Type      string
	SagaID    string
	StepName  string
	Data      any
	Error     string
	Timestamp time.Time
}

// EventStore defines the interface for publishing saga events
type EventStore interface {
	Publish(ctx context.Context, event Event) error
}

// DistributedStep represents a step in a distributed saga
type DistributedStep struct {
	name       string
	execute    func(ctx context.Context, data any) error
	compensate func(ctx context.Context, data any) error
}

// NewDistributedStep creates a new distributed step
func NewDistributedStep(
	name string,
	execute, compensate func(ctx context.Context, data any) error,
) *DistributedStep {
	return &DistributedStep{
		name:       name,
		execute:    execute,
		compensate: compensate,
	}
}

// Name returns the step name
func (s *DistributedStep) Name() string {
	return s.name
}

// Execute performs the step's forward action
func (s *DistributedStep) Execute(ctx context.Context, data any) error {
	if s.execute != nil {
		return s.execute(ctx, data)
	}

	return nil
}

// Compensate performs the step's compensating action
func (s *DistributedStep) Compensate(ctx context.Context, data any) error {
	if s.compensate != nil {
		return s.compensate(ctx, data)
	}

	return nil
}

// DistributedSaga represents a distributed saga with event publishing
type DistributedSaga struct {
	*Saga
	eventStore EventStore
	sagaID     string
}

// NewDistributedSaga creates a new distributed saga
func NewDistributedSaga(name string, eventStore EventStore) *DistributedSaga {
	return &DistributedSaga{
		Saga:       NewSaga(name),
		eventStore: eventStore,
		sagaID:     uuid.New().String(),
	}
}

// AddStep adds a step to the distributed saga
func (ds *DistributedSaga) AddStep(step Step) *DistributedSaga {
	ds.Saga.AddStep(step)
	return ds
}

// Execute runs the distributed saga with event publishing
func (ds *DistributedSaga) Execute(ctx context.Context, data any) error {
	// Publish saga started event
	ds.publishEvent(ctx, EventTypeSagaStarted, "", data, nil)

	// Execute steps with event publishing
	executedSteps := make([]Step, 0, len(ds.steps))

	for _, step := range ds.steps {
		// Publish step started
		ds.publishEvent(ctx, EventTypeStepStarted, step.Name(), data, nil)

		// Execute step
		err := step.Execute(ctx, data)
		if err != nil {
			// Publish step failed
			ds.publishEvent(ctx, EventTypeStepFailed, step.Name(), data, err)

			// Compensate executed steps
			for i := len(executedSteps) - 1; i >= 0; i-- {
				compensatingStep := executedSteps[i]
				ds.publishEvent(ctx, EventTypeStepCompensating, compensatingStep.Name(), data, nil)

				compensateErr := compensatingStep.Compensate(ctx, data)
				if compensateErr != nil {
					ds.publishEvent(
						ctx,
						EventTypeStepFailed,
						compensatingStep.Name(),
						data,
						compensateErr,
					)
				} else {
					ds.publishEvent(ctx, EventTypeStepCompensated, compensatingStep.Name(), data, nil)
				}
			}

			// Publish saga failed
			ds.publishEvent(ctx, EventTypeSagaFailed, "", data, err)

			return err
		}

		// Publish step completed
		ds.publishEvent(ctx, EventTypeStepCompleted, step.Name(), data, nil)
		executedSteps = append(executedSteps, step)
	}

	// Publish saga completed event
	ds.publishEvent(ctx, EventTypeSagaCompleted, "", data, nil)

	return nil
}

// publishEvent publishes a saga event
func (ds *DistributedSaga) publishEvent(
	ctx context.Context,
	eventType, stepName string,
	data any,
	err error,
) {
	event := Event{
		ID:        uuid.New().String(),
		Type:      eventType,
		SagaID:    ds.sagaID,
		StepName:  stepName,
		Data:      data,
		Timestamp: time.Now(),
	}

	if err != nil {
		event.Error = err.Error()
	}

	// Ignore publish errors for simplicity
	_ = ds.eventStore.Publish(ctx, event)
}

// SagaBuilder helps build sagas fluently.
// The type name intentionally matches the package name for clarity in external usage.
type SagaBuilder struct {
	name       string
	steps      []Step
	timeout    time.Duration
	retries    int
	retryDelay time.Duration
}

// NewSagaBuilder creates a new saga builder
func NewSagaBuilder(name string) *SagaBuilder {
	return &SagaBuilder{
		name:       name,
		steps:      make([]Step, 0),
		timeout:    30 * time.Second,
		retries:    0,
		retryDelay: time.Second,
	}
}

// WithTimeout sets the saga timeout
func (sb *SagaBuilder) WithTimeout(timeout time.Duration) *SagaBuilder {
	sb.timeout = timeout
	return sb
}

// WithRetry sets retry configuration
func (sb *SagaBuilder) WithRetry(retries int, delay time.Duration) *SagaBuilder {
	sb.retries = retries
	sb.retryDelay = delay

	return sb
}

// WithStep adds a step to the saga
func (sb *SagaBuilder) WithStep(
	name string,
	execute, compensate func(ctx context.Context, data any) error,
) *SagaBuilder {
	step := &DistributedStep{
		name:       name,
		execute:    execute,
		compensate: compensate,
	}
	sb.steps = append(sb.steps, step)

	return sb
}

// Build builds the saga
func (sb *SagaBuilder) Build() *Saga {
	saga := NewSaga(sb.name)
	saga.timeout = sb.timeout
	saga.retries = sb.retries
	saga.retryDelay = sb.retryDelay

	for _, step := range sb.steps {
		saga.AddStep(step)
	}

	return saga
}
