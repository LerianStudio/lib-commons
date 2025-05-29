package saga

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Test step implementations
type testStep struct {
	name           string
	executeFunc    func(ctx context.Context, data interface{}) error
	compensateFunc func(ctx context.Context, data interface{}) error
	executed       bool
	compensated    bool
	mu             sync.Mutex
}

func (s *testStep) Name() string {
	return s.name
}

func (s *testStep) Execute(ctx context.Context, data interface{}) error {
	s.mu.Lock()
	s.executed = true
	s.mu.Unlock()

	if s.executeFunc != nil {
		return s.executeFunc(ctx, data)
	}
	return nil
}

func (s *testStep) Compensate(ctx context.Context, data interface{}) error {
	s.mu.Lock()
	s.compensated = true
	s.mu.Unlock()

	if s.compensateFunc != nil {
		return s.compensateFunc(ctx, data)
	}
	return nil
}

func (s *testStep) IsExecuted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executed
}

func (s *testStep) IsCompensated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compensated
}

func TestSaga(t *testing.T) {
	t.Run("successful saga execution", func(t *testing.T) {
		// Create steps
		step1 := &testStep{name: "step1"}
		step2 := &testStep{name: "step2"}
		step3 := &testStep{name: "step3"}

		// Create saga
		saga := NewSaga("test-saga").
			AddStep(step1).
			AddStep(step2).
			AddStep(step3)

		// Execute
		ctx := context.Background()
		data := map[string]string{"key": "value"}

		err := saga.Execute(ctx, data)
		assert.NoError(t, err)

		// Verify all steps executed
		assert.True(t, step1.IsExecuted())
		assert.True(t, step2.IsExecuted())
		assert.True(t, step3.IsExecuted())

		// Verify no compensations
		assert.False(t, step1.IsCompensated())
		assert.False(t, step2.IsCompensated())
		assert.False(t, step3.IsCompensated())
	})

	t.Run("saga with failure and compensation", func(t *testing.T) {
		// Create steps
		step1 := &testStep{name: "step1"}
		step2 := &testStep{name: "step2"}
		step3 := &testStep{
			name: "step3",
			executeFunc: func(ctx context.Context, data interface{}) error {
				return errors.New("step3 failed")
			},
		}

		// Create saga
		saga := NewSaga("test-saga").
			AddStep(step1).
			AddStep(step2).
			AddStep(step3)

		// Execute
		ctx := context.Background()
		data := map[string]string{"key": "value"}

		err := saga.Execute(ctx, data)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "step3 failed")

		// Verify execution stopped at step3
		assert.True(t, step1.IsExecuted())
		assert.True(t, step2.IsExecuted())
		assert.True(t, step3.IsExecuted())

		// Verify compensations ran in reverse order
		assert.True(t, step1.IsCompensated())
		assert.True(t, step2.IsCompensated())
		assert.False(t, step3.IsCompensated()) // Failed step doesn't compensate
	})

	t.Run("saga with context cancellation", func(t *testing.T) {
		// Create steps with delays
		step1 := &testStep{name: "step1"}
		step2 := &testStep{
			name: "step2",
			executeFunc: func(ctx context.Context, data interface{}) error {
				select {
				case <-time.After(100 * time.Millisecond):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		}
		step3 := &testStep{name: "step3"}

		// Create saga
		saga := NewSaga("test-saga").
			AddStep(step1).
			AddStep(step2).
			AddStep(step3)

		// Execute with cancellation
		ctx, cancel := context.WithCancel(context.Background())
		data := map[string]string{"key": "value"}

		// Cancel after a short delay
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		err := saga.Execute(ctx, data)
		assert.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)

		// Verify compensation
		assert.True(t, step1.IsCompensated())
		assert.False(t, step3.IsExecuted())
	})

	t.Run("saga with compensation failure", func(t *testing.T) {
		// Create steps
		step1 := &testStep{
			name: "step1",
			compensateFunc: func(ctx context.Context, data interface{}) error {
				return errors.New("compensation failed")
			},
		}
		step2 := &testStep{name: "step2"}
		step3 := &testStep{
			name: "step3",
			executeFunc: func(ctx context.Context, data interface{}) error {
				return errors.New("step3 failed")
			},
		}

		// Create saga
		saga := NewSaga("test-saga").
			AddStep(step1).
			AddStep(step2).
			AddStep(step3)

		// Execute
		ctx := context.Background()
		data := map[string]string{"key": "value"}

		err := saga.Execute(ctx, data)
		assert.Error(t, err)

		// Should contain both original error and compensation error
		assert.Contains(t, err.Error(), "step3 failed")
		assert.Contains(t, err.Error(), "compensation failed")
	})
}

func TestSagaCoordinator(t *testing.T) {
	t.Run("coordinate multiple sagas", func(t *testing.T) {
		coordinator := NewCoordinator()
		ctx := context.Background()

		// Create sagas
		saga1 := NewSaga("saga1").
			AddStep(&testStep{name: "saga1-step1"}).
			AddStep(&testStep{name: "saga1-step2"})

		saga2 := NewSaga("saga2").
			AddStep(&testStep{name: "saga2-step1"}).
			AddStep(&testStep{name: "saga2-step2"})

		// Register sagas
		coordinator.Register(saga1)
		coordinator.Register(saga2)

		// Execute saga1
		data1 := map[string]string{"saga": "1"}
		id1, err := coordinator.Start(ctx, "saga1", data1)
		assert.NoError(t, err)
		assert.NotEmpty(t, id1)

		// Execute saga2
		data2 := map[string]string{"saga": "2"}
		id2, err := coordinator.Start(ctx, "saga2", data2)
		assert.NoError(t, err)
		assert.NotEmpty(t, id2)

		// Wait for sagas to complete
		time.Sleep(50 * time.Millisecond)

		// Check status
		status1 := coordinator.GetStatus(id1)
		assert.Equal(t, SagaStatusCompleted, status1.Status)
		assert.Equal(t, "saga1", status1.Name)

		status2 := coordinator.GetStatus(id2)
		assert.Equal(t, SagaStatusCompleted, status2.Status)
		assert.Equal(t, "saga2", status2.Name)
	})

	t.Run("saga not found", func(t *testing.T) {
		coordinator := NewCoordinator()
		ctx := context.Background()

		_, err := coordinator.Start(ctx, "nonexistent", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestDistributedSaga(t *testing.T) {
	t.Run("distributed saga with events", func(t *testing.T) {
		// Create event store
		events := make([]Event, 0)
		var mu sync.Mutex

		store := &mockEventStore{
			publishFunc: func(ctx context.Context, event Event) error {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
				return nil
			},
		}

		// Create distributed saga
		saga := NewDistributedSaga("order-saga", store)

		// Add steps
		step1 := NewDistributedStep("reserve-inventory",
			func(ctx context.Context, data interface{}) error {
				// Simulate inventory reservation
				return nil
			},
			func(ctx context.Context, data interface{}) error {
				// Simulate inventory release
				return nil
			},
		)

		step2 := NewDistributedStep("charge-payment",
			func(ctx context.Context, data interface{}) error {
				// Simulate payment charge
				return nil
			},
			func(ctx context.Context, data interface{}) error {
				// Simulate payment refund
				return nil
			},
		)

		saga.AddStep(step1)
		saga.AddStep(step2)

		// Execute
		ctx := context.Background()
		data := map[string]interface{}{
			"orderId": "order-123",
			"amount":  100.0,
		}

		err := saga.Execute(ctx, data)
		assert.NoError(t, err)

		// Verify events
		mu.Lock()
		defer mu.Unlock()

		assert.Greater(t, len(events), 0)

		// Should have saga started event
		hasStarted := false
		for _, e := range events {
			if e.Type == EventTypeSagaStarted {
				hasStarted = true
				break
			}
		}
		assert.True(t, hasStarted)

		// Should have saga completed event
		hasCompleted := false
		for _, e := range events {
			if e.Type == EventTypeSagaCompleted {
				hasCompleted = true
				break
			}
		}
		assert.True(t, hasCompleted)
	})

	t.Run("distributed saga with failure", func(t *testing.T) {
		// Create event store
		events := make([]Event, 0)
		var mu sync.Mutex

		store := &mockEventStore{
			publishFunc: func(ctx context.Context, event Event) error {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
				return nil
			},
		}

		// Create distributed saga
		saga := NewDistributedSaga("order-saga", store)

		// Add steps
		step1 := NewDistributedStep("reserve-inventory",
			func(ctx context.Context, data interface{}) error {
				return nil
			},
			func(ctx context.Context, data interface{}) error {
				return nil
			},
		)

		step2 := NewDistributedStep("charge-payment",
			func(ctx context.Context, data interface{}) error {
				return errors.New("payment failed")
			},
			func(ctx context.Context, data interface{}) error {
				return nil
			},
		)

		saga.AddStep(step1)
		saga.AddStep(step2)

		// Execute
		ctx := context.Background()
		data := map[string]interface{}{
			"orderId": "order-123",
		}

		err := saga.Execute(ctx, data)
		assert.Error(t, err)

		// Verify compensation events
		mu.Lock()
		defer mu.Unlock()

		// Should have saga failed event
		hasFailed := false
		for _, e := range events {
			if e.Type == EventTypeSagaFailed {
				hasFailed = true
				break
			}
		}
		assert.True(t, hasFailed)

		// Should have compensation events
		hasCompensation := false
		for _, e := range events {
			if e.Type == EventTypeStepCompensated {
				hasCompensation = true
				break
			}
		}
		assert.True(t, hasCompensation)
	})
}

// Mock event store
type mockEventStore struct {
	publishFunc func(ctx context.Context, event Event) error
}

func (m *mockEventStore) Publish(ctx context.Context, event Event) error {
	if m.publishFunc != nil {
		return m.publishFunc(ctx, event)
	}
	return nil
}

func TestSagaBuilder(t *testing.T) {
	t.Run("build saga with options", func(t *testing.T) {
		builder := NewSagaBuilder("test-saga").
			WithTimeout(5*time.Second).
			WithRetry(3, 100*time.Millisecond).
			WithStep("step1",
				func(ctx context.Context, data interface{}) error {
					return nil
				},
				func(ctx context.Context, data interface{}) error {
					return nil
				},
			).
			WithStep("step2",
				func(ctx context.Context, data interface{}) error {
					return nil
				},
				func(ctx context.Context, data interface{}) error {
					return nil
				},
			)

		saga := builder.Build()
		assert.NotNil(t, saga)

		// Execute
		ctx := context.Background()
		err := saga.Execute(ctx, nil)
		assert.NoError(t, err)
	})

	t.Run("build saga with retry", func(t *testing.T) {
		attempts := 0
		builder := NewSagaBuilder("test-saga").
			WithRetry(3, 10*time.Millisecond).
			WithStep("flaky-step",
				func(ctx context.Context, data interface{}) error {
					attempts++
					if attempts < 3 {
						return errors.New("temporary error")
					}
					return nil
				},
				func(ctx context.Context, data interface{}) error {
					return nil
				},
			)

		saga := builder.Build()
		ctx := context.Background()
		err := saga.Execute(ctx, nil)

		assert.NoError(t, err)
		assert.Equal(t, 3, attempts)
	})
}

func BenchmarkSaga(b *testing.B) {
	// Create a simple saga
	saga := NewSaga("bench-saga")
	for i := 0; i < 5; i++ {
		saga.AddStep(&testStep{
			name: "step" + string(rune('0'+i)),
			executeFunc: func(ctx context.Context, data interface{}) error {
				// Simulate some work
				time.Sleep(time.Microsecond)
				return nil
			},
		})
	}

	ctx := context.Background()
	data := map[string]string{"test": "data"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		saga.Execute(ctx, data)
	}
}
