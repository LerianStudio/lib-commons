package eventsourcing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Test event types
type AccountCreated struct {
	AccountID string
	Owner     string
	Balance   float64
	Timestamp time.Time
}

type AmountDeposited struct {
	AccountID string
	Amount    float64
	Timestamp time.Time
}

type AmountWithdrawn struct {
	AccountID string
	Amount    float64
	Timestamp time.Time
}

// Test aggregate
type BankAccount struct {
	ID      string
	Owner   string
	Balance float64
	Version int
}

func (a *BankAccount) GetID() string {
	return a.ID
}

func (a *BankAccount) GetVersion() int {
	return a.Version
}

func (a *BankAccount) Apply(event Event) error {
	switch e := event.Data.(type) {
	case AccountCreated:
		a.ID = e.AccountID
		a.Owner = e.Owner
		a.Balance = e.Balance
	case AmountDeposited:
		a.Balance += e.Amount
	case AmountWithdrawn:
		if a.Balance < e.Amount {
			return errors.New("insufficient funds")
		}
		a.Balance -= e.Amount
	default:
		return errors.New("unknown event type")
	}
	a.Version = event.Version
	return nil
}

func (a *BankAccount) HandleCommand(cmd any) ([]Event, error) {
	switch c := cmd.(type) {
	case CreateAccountCommand:
		if a.ID != "" {
			return nil, errors.New("account already exists")
		}
		return []Event{
			{
				AggregateID: c.AccountID,
				Type:        "AccountCreated",
				Data: AccountCreated{
					AccountID: c.AccountID,
					Owner:     c.Owner,
					Balance:   c.InitialBalance,
					Timestamp: time.Now(),
				},
				Version:   1,
				Timestamp: time.Now(),
			},
		}, nil
	case DepositCommand:
		if a.ID == "" {
			return nil, errors.New("account not found")
		}
		return []Event{
			{
				AggregateID: a.ID,
				Type:        "AmountDeposited",
				Data: AmountDeposited{
					AccountID: a.ID,
					Amount:    c.Amount,
					Timestamp: time.Now(),
				},
				Version:   a.Version + 1,
				Timestamp: time.Now(),
			},
		}, nil
	case WithdrawCommand:
		if a.ID == "" {
			return nil, errors.New("account not found")
		}
		if a.Balance < c.Amount {
			return nil, errors.New("insufficient funds")
		}
		return []Event{
			{
				AggregateID: a.ID,
				Type:        "AmountWithdrawn",
				Data: AmountWithdrawn{
					AccountID: a.ID,
					Amount:    c.Amount,
					Timestamp: time.Now(),
				},
				Version:   a.Version + 1,
				Timestamp: time.Now(),
			},
		}, nil
	default:
		return nil, errors.New("unknown command")
	}
}

// Test commands
type CreateAccountCommand struct {
	AccountID      string
	Owner          string
	InitialBalance float64
}

type DepositCommand struct {
	AccountID string
	Amount    float64
}

type WithdrawCommand struct {
	AccountID string
	Amount    float64
}

// Mock event store
type mockEventStore struct {
	mu        sync.RWMutex
	events    map[string][]Event
	streams   map[string][]Event
	appendErr error
	loadErr   error
	listeners []EventListener
}

func newMockEventStore() *mockEventStore {
	return &mockEventStore{
		events:  make(map[string][]Event),
		streams: make(map[string][]Event),
	}
}

func (m *mockEventStore) Append(
	_ context.Context,
	streamID string,
	events []Event,
	expectedVersion int,
) error {
	if m.appendErr != nil {
		return m.appendErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check version
	existing := m.events[streamID]
	if len(existing) > 0 && existing[len(existing)-1].Version != expectedVersion {
		return ErrConcurrentModification
	}

	m.events[streamID] = append(m.events[streamID], events...)

	// Notify listeners
	for _, listener := range m.listeners {
		for _, event := range events {
			go listener(event)
		}
	}

	return nil
}

func (m *mockEventStore) Load(
	_ context.Context,
	streamID string,
	fromVersion int,
) ([]Event, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	events := m.events[streamID]
	if fromVersion > 0 {
		var filtered []Event
		for _, e := range events {
			if e.Version >= fromVersion {
				filtered = append(filtered, e)
			}
		}
		return filtered, nil
	}
	return events, nil
}

func (m *mockEventStore) LoadSnapshot(_ context.Context, _ string) (*Snapshot, error) {
	return nil, nil // No snapshots in mock
}

func (m *mockEventStore) SaveSnapshot(_ context.Context, _ Snapshot) error {
	return nil // No snapshots in mock
}

func (m *mockEventStore) Subscribe(listener EventListener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listeners = append(m.listeners, listener)
}

func TestEventStore(t *testing.T) {
	t.Run("append and load events", func(t *testing.T) {
		store := newMockEventStore()
		ctx := context.Background()

		// Append events
		events := []Event{
			{
				ID:          "evt-1",
				AggregateID: "acc-1",
				Type:        "AccountCreated",
				Data:        AccountCreated{AccountID: "acc-1", Owner: "John"},
				Version:     1,
				Timestamp:   time.Now(),
			},
			{
				ID:          "evt-2",
				AggregateID: "acc-1",
				Type:        "AmountDeposited",
				Data:        AmountDeposited{AccountID: "acc-1", Amount: 100},
				Version:     2,
				Timestamp:   time.Now(),
			},
		}

		err := store.Append(ctx, "acc-1", events, 0)
		assert.NoError(t, err)

		// Load events
		loaded, err := store.Load(ctx, "acc-1", 0)
		assert.NoError(t, err)
		assert.Equal(t, 2, len(loaded))
		assert.Equal(t, "AccountCreated", loaded[0].Type)
		assert.Equal(t, "AmountDeposited", loaded[1].Type)
	})

	t.Run("concurrent modification detection", func(t *testing.T) {
		store := newMockEventStore()
		ctx := context.Background()

		// First append
		event1 := []Event{{
			ID:          "evt-1",
			AggregateID: "acc-1",
			Type:        "AccountCreated",
			Version:     1,
			Timestamp:   time.Now(),
		}}
		err := store.Append(ctx, "acc-1", event1, 0)
		assert.NoError(t, err)

		// Concurrent append with wrong version
		event2 := []Event{{
			ID:          "evt-2",
			AggregateID: "acc-1",
			Type:        "AmountDeposited",
			Version:     2,
			Timestamp:   time.Now(),
		}}
		err = store.Append(ctx, "acc-1", event2, 0) // Wrong expected version
		assert.ErrorIs(t, err, ErrConcurrentModification)
	})
}

func TestEventSourcingRepository(t *testing.T) {
	t.Run("save and get aggregate", func(t *testing.T) {
		store := newMockEventStore()
		repo := NewRepository(store, func() Aggregate {
			return &BankAccount{}
		})
		ctx := context.Background()

		// Create new account
		account := &BankAccount{}
		cmd := CreateAccountCommand{
			AccountID:      "acc-1",
			Owner:          "John",
			InitialBalance: 1000,
		}

		events, err := account.HandleCommand(cmd)
		assert.NoError(t, err)

		// Apply events to aggregate
		for _, event := range events {
			err = account.Apply(event)
			assert.NoError(t, err)
		}

		// Save aggregate
		err = repo.Save(ctx, account, events)
		assert.NoError(t, err)

		// Load aggregate
		loaded, err := repo.Get(ctx, "acc-1")
		assert.NoError(t, err)

		loadedAccount := loaded.(*BankAccount)
		assert.Equal(t, "acc-1", loadedAccount.ID)
		assert.Equal(t, "John", loadedAccount.Owner)
		assert.Equal(t, float64(1000), loadedAccount.Balance)
		assert.Equal(t, 1, loadedAccount.Version)
	})

	t.Run("update aggregate", func(t *testing.T) {
		store := newMockEventStore()
		repo := NewRepository(store, func() Aggregate {
			return &BankAccount{}
		})
		ctx := context.Background()

		// Create and save account
		account := &BankAccount{}
		createEvents, _ := account.HandleCommand(CreateAccountCommand{
			AccountID:      "acc-1",
			Owner:          "John",
			InitialBalance: 1000,
		})
		for _, event := range createEvents {
			_ = account.Apply(event)
		}
		_ = repo.Save(ctx, account, createEvents)

		// Load and update
		loaded, _ := repo.Get(ctx, "acc-1")
		account = loaded.(*BankAccount)

		// Deposit
		depositEvents, err := account.HandleCommand(DepositCommand{
			AccountID: "acc-1",
			Amount:    500,
		})
		assert.NoError(t, err)

		for _, event := range depositEvents {
			_ = account.Apply(event)
		}

		err = repo.Save(ctx, account, depositEvents)
		assert.NoError(t, err)

		// Verify
		reloaded, _ := repo.Get(ctx, "acc-1")
		reloadedAccount := reloaded.(*BankAccount)
		assert.Equal(t, float64(1500), reloadedAccount.Balance)
		assert.Equal(t, 2, reloadedAccount.Version)
	})
}

func TestEventBus(t *testing.T) {
	t.Run("publish and subscribe", func(t *testing.T) {
		bus := NewEventBus()
		ctx := context.Background()

		received := make(map[string]bool)
		var mu sync.Mutex
		done := make(chan bool, 2)

		// Subscribe
		bus.Subscribe("AccountCreated", func(event Event) {
			mu.Lock()
			received[event.Type] = true
			mu.Unlock()
			done <- true
		})
		bus.Subscribe("AmountDeposited", func(event Event) {
			mu.Lock()
			received[event.Type] = true
			mu.Unlock()
			done <- true
		})

		// Publish events
		event1 := Event{
			Type: "AccountCreated",
			Data: AccountCreated{AccountID: "acc-1"},
		}
		event2 := Event{
			Type: "AmountDeposited",
			Data: AmountDeposited{Amount: 100},
		}

		_ = bus.Publish(ctx, event1)
		_ = bus.Publish(ctx, event2)

		// Wait for both events
		for i := 0; i < 2; i++ {
			select {
			case <-done:
				// Event received
			case <-time.After(100 * time.Millisecond):
				t.Fatal("timeout waiting for event")
			}
		}

		// Verify both events were received
		mu.Lock()
		assert.True(t, received["AccountCreated"])
		assert.True(t, received["AmountDeposited"])
		mu.Unlock()
	})

	t.Run("multiple subscribers", func(t *testing.T) {
		bus := NewEventBus()
		ctx := context.Background()

		var wg sync.WaitGroup
		wg.Add(3)

		// Multiple subscribers
		for i := 0; i < 3; i++ {
			bus.Subscribe("TestEvent", func(_ Event) {
				wg.Done()
			})
		}

		// Publish one event
		_ = bus.Publish(ctx, Event{Type: "TestEvent"})

		// All subscribers should receive it
		done := make(chan bool)
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Success
		case <-time.After(100 * time.Millisecond):
			t.Fatal("not all subscribers received the event")
		}
	})
}

func TestProjection(t *testing.T) {
	t.Run("build projection from events", func(t *testing.T) {
		// Account balance projection
		projection := &accountBalanceProjection{
			balances: make(map[string]float64),
		}

		events := []Event{
			{
				Type: "AccountCreated",
				Data: AccountCreated{
					AccountID: "acc-1",
					Balance:   1000,
				},
			},
			{
				Type: "AmountDeposited",
				Data: AmountDeposited{
					AccountID: "acc-1",
					Amount:    500,
				},
			},
			{
				Type: "AmountWithdrawn",
				Data: AmountWithdrawn{
					AccountID: "acc-1",
					Amount:    200,
				},
			},
		}

		// Apply events
		for _, event := range events {
			projection.Apply(event)
		}

		// Verify projection
		assert.Equal(t, float64(1300), projection.GetBalance("acc-1"))
	})
}

// Test projection
type accountBalanceProjection struct {
	mu       sync.RWMutex
	balances map[string]float64
}

func (p *accountBalanceProjection) Apply(event Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch e := event.Data.(type) {
	case AccountCreated:
		p.balances[e.AccountID] = e.Balance
	case AmountDeposited:
		p.balances[e.AccountID] += e.Amount
	case AmountWithdrawn:
		p.balances[e.AccountID] -= e.Amount
	}
}

func (p *accountBalanceProjection) GetBalance(accountID string) float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.balances[accountID]
}

func TestEventHandlerChain(t *testing.T) {
	t.Run("chain multiple handlers", func(t *testing.T) {
		var order []string
		var mu sync.Mutex

		handler1 := EventHandlerFunc(func(_ context.Context, _ Event) error {
			mu.Lock()
			order = append(order, "handler1")
			mu.Unlock()
			return nil
		})

		handler2 := EventHandlerFunc(func(_ context.Context, _ Event) error {
			mu.Lock()
			order = append(order, "handler2")
			mu.Unlock()
			return nil
		})

		handler3 := EventHandlerFunc(func(_ context.Context, _ Event) error {
			mu.Lock()
			order = append(order, "handler3")
			mu.Unlock()
			return nil
		})

		chain := ChainHandlers(handler1, handler2, handler3)
		ctx := context.Background()
		event := Event{Type: "TestEvent"}

		err := chain.Handle(ctx, event)
		assert.NoError(t, err)

		mu.Lock()
		assert.Equal(t, []string{"handler1", "handler2", "handler3"}, order)
		mu.Unlock()
	})

	t.Run("stop on error", func(t *testing.T) {
		var called []string

		handler1 := EventHandlerFunc(func(_ context.Context, _ Event) error {
			called = append(called, "handler1")
			return nil
		})

		handler2 := EventHandlerFunc(func(_ context.Context, _ Event) error {
			called = append(called, "handler2")
			return errors.New("handler2 error")
		})

		handler3 := EventHandlerFunc(func(_ context.Context, _ Event) error {
			called = append(called, "handler3")
			return nil
		})

		chain := ChainHandlers(handler1, handler2, handler3)
		ctx := context.Background()
		event := Event{Type: "TestEvent"}

		err := chain.Handle(ctx, event)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "handler2 error")
		assert.Equal(t, []string{"handler1", "handler2"}, called)
	})
}

func BenchmarkEventStore(b *testing.B) {
	store := newMockEventStore()
	ctx := context.Background()

	b.Run("append events", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			events := []Event{{
				ID:          "evt-" + string(rune(i)),
				AggregateID: "agg-1",
				Type:        "TestEvent",
				Version:     i + 1,
				Timestamp:   time.Now(),
			}}
			_ = store.Append(ctx, "agg-1", events, i)
		}
	})

	b.Run("load events", func(b *testing.B) {
		// Prepare events
		for i := 0; i < 100; i++ {
			events := []Event{{
				ID:          "evt-" + string(rune(i)),
				AggregateID: "agg-1",
				Type:        "TestEvent",
				Version:     i + 1,
				Timestamp:   time.Now(),
			}}
			_ = store.Append(ctx, "agg-1", events, i)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = store.Load(ctx, "agg-1", 0)
		}
	})
}
