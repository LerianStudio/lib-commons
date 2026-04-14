// Package store defines the internal storage abstraction for systemplane backends.
//
// It is the only interface kept between the public Client and the concrete
// Postgres / MongoDB implementations. The interface is intentionally small so
// that each backend can be implemented in ~300-350 LOC.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNilBackend is returned when a Store constructor receives a nil database handle.
var ErrNilBackend = errors.New("systemplane/store: nil backend handle")

// ErrClosed is returned when a method is called on a nil or closed Store.
var ErrClosed = errors.New("systemplane/store: store is closed or nil")

// Entry is the persisted shape of a single configuration key.
type Entry struct {
	Namespace string
	Key       string
	Value     []byte // JSON-encoded
	UpdatedAt time.Time
	UpdatedBy string
}

// Event is what a changefeed delivers when a key is modified.
type Event struct {
	Namespace string
	Key       string
}

// Store is implemented by internal/postgres and internal/mongodb.
type Store interface {
	// List returns all entries. Used to hydrate the Client at Start().
	List(ctx context.Context) ([]Entry, error)

	// Get returns a single entry by namespace and key.
	Get(ctx context.Context, namespace, key string) (Entry, bool, error)

	// Set persists an entry using last-write-wins semantics.
	// The backend is responsible for notifying subscribers via its changefeed.
	Set(ctx context.Context, e Entry) error

	// Subscribe blocks until ctx is cancelled, invoking handler for each
	// change event received from the backend's changefeed mechanism.
	Subscribe(ctx context.Context, handler func(Event)) error

	// Close releases backend resources. Idempotent.
	Close() error
}
