package idempotency

import (
	"context"
	"time"
)

//go:generate mockgen -source=store.go -destination=store_mock_test.go -package=idempotency -build_constraint=unit

type cachedResponse struct {
	StatusCode  int                 `json:"status_code"`
	ContentType string              `json:"content_type"`
	Body        []byte              `json:"body"`
	Headers     map[string][]string `json:"headers,omitempty"`
}

type storeRecord struct {
	State       string `json:"state"`
	Fingerprint string `json:"fingerprint"`
	Owner       string `json:"owner"`
	Response    []byte `json:"response,omitempty"`
}

// Store provides exactly the three atomic byte operations required by the
// canonical middleware state machine. It is backend-neutral: implementations
// can use Redis, Valkey, or another store without exposing driver-specific
// types or reimplementing fingerprint, in-progress, replay, or key-reuse rules.
//
// Acquire atomically stores candidate with ttl only when key is absent or
// expired. Exactly one concurrent caller may receive acquired=true. When the
// key already exists it returns the current opaque value and acquired=false.
//
// Complete atomically replaces expected with completed and resets expiration to
// ttl only while the stored bytes still equal expected. Release atomically
// deletes only while the stored bytes still equal expected. Both return false,
// without mutation, when the key expired or its value changed.
// Acquire and Complete must reject non-positive TTL values without mutation.
//
// Implementations should run idempotencytest.Run from the
// commons/net/http/idempotency/idempotencytest package.
type Store interface {
	Acquire(ctx context.Context, key string, candidate []byte, ttl time.Duration) (current []byte, acquired bool, err error)
	Complete(ctx context.Context, key string, expected, completed []byte, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key string, expected []byte) (bool, error)
}
