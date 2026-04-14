// NewForTesting constructor for out-of-package tests.
package systemplane

import (
	"context"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
)

// TestStore is the public mirror of the internal store.Store interface, exposed
// solely for [NewForTesting]. Production code uses [NewPostgres] or [NewMongoDB]
// which wire the internal interface directly.
//
// DO NOT USE IN PRODUCTION. This interface is intentionally undocumented in
// README/API docs. Its API stability is not promised.
type TestStore interface {
	List(ctx context.Context) ([]TestEntry, error)
	Get(ctx context.Context, namespace, key string) (TestEntry, bool, error)
	Set(ctx context.Context, e TestEntry) error
	Subscribe(ctx context.Context, handler func(TestEvent)) error
	Close() error
}

// TestEntry is the public mirror of internal store.Entry.
type TestEntry struct {
	Namespace string
	Key       string
	Value     []byte // JSON-encoded
	UpdatedAt time.Time
	UpdatedBy string
}

// TestEvent is the public mirror of internal store.Event.
type TestEvent struct {
	Namespace string
	Key       string
}

// testStoreAdapter wraps a TestStore to satisfy the internal store.Store interface.
type testStoreAdapter struct {
	ts TestStore
}

func (a *testStoreAdapter) List(ctx context.Context) ([]store.Entry, error) {
	entries, err := a.ts.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]store.Entry, len(entries))
	for i, e := range entries {
		out[i] = store.Entry{
			Namespace: e.Namespace,
			Key:       e.Key,
			Value:     e.Value,
			UpdatedAt: e.UpdatedAt,
			UpdatedBy: e.UpdatedBy,
		}
	}

	return out, nil
}

func (a *testStoreAdapter) Get(ctx context.Context, namespace, key string) (store.Entry, bool, error) {
	te, found, err := a.ts.Get(ctx, namespace, key)
	if err != nil || !found {
		return store.Entry{}, found, err
	}

	return store.Entry{
		Namespace: te.Namespace,
		Key:       te.Key,
		Value:     te.Value,
		UpdatedAt: te.UpdatedAt,
		UpdatedBy: te.UpdatedBy,
	}, true, nil
}

func (a *testStoreAdapter) Set(ctx context.Context, e store.Entry) error {
	return a.ts.Set(ctx, TestEntry{
		Namespace: e.Namespace,
		Key:       e.Key,
		Value:     e.Value,
		UpdatedAt: e.UpdatedAt,
		UpdatedBy: e.UpdatedBy,
	})
}

func (a *testStoreAdapter) Subscribe(ctx context.Context, handler func(store.Event)) error {
	return a.ts.Subscribe(ctx, func(te TestEvent) {
		handler(store.Event{
			Namespace: te.Namespace,
			Key:       te.Key,
		})
	})
}

func (a *testStoreAdapter) Close() error {
	return a.ts.Close()
}

// NewForTesting wires a Client from an explicit [TestStore] implementation. It
// is intended exclusively for out-of-package tests (e.g. admin_test.go) that
// need a Client backed by a controlled in-memory store without a live database.
//
// Debouncing is disabled by default for test determinism. Callers can override
// via [WithDebounce] if needed.
//
// DO NOT USE IN PRODUCTION. This constructor is intentionally undocumented in
// README/API docs. Its API stability is not promised.
func NewForTesting(s TestStore, opts ...Option) (*Client, error) {
	if s == nil {
		return nil, store.ErrNilBackend
	}

	cfg := defaultClientConfig()

	// Disable debouncing for test determinism by default.
	// Applied before opts so callers can override via WithDebounce.
	cfg.debounce = 0

	for _, o := range opts {
		o(&cfg)
	}

	return newClient(&testStoreAdapter{ts: s}, cfg), nil
}
