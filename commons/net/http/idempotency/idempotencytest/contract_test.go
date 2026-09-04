//go:build unit

package idempotencytest

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/net/http/idempotency"
)

var errNonPositiveTTL = errors.New("TTL must be positive")

func TestRun_MemoryStoreSatisfiesContract(t *testing.T) {
	Run(t, func(_ *testing.T) idempotency.Store {
		return &memoryStore{records: make(map[string]memoryRecord)}
	})
}

type memoryRecord struct {
	value     []byte
	expiresAt time.Time
}

type memoryStore struct {
	mu      sync.Mutex
	records map[string]memoryRecord
}

func (s *memoryStore) Acquire(
	_ context.Context,
	key string,
	candidate []byte,
	ttl time.Duration,
) ([]byte, bool, error) {
	if ttl <= 0 {
		return nil, false, errNonPositiveTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, found := s.records[key]
	if found && time.Now().After(record.expiresAt) {
		delete(s.records, key)
		found = false
	}
	if found {
		return append([]byte(nil), record.value...), false, nil
	}

	s.records[key] = memoryRecord{value: append([]byte(nil), candidate...), expiresAt: time.Now().Add(ttl)}

	return nil, true, nil
}

func (s *memoryStore) Complete(
	_ context.Context,
	key string,
	expected, completed []byte,
	ttl time.Duration,
) (bool, error) {
	if ttl <= 0 {
		return false, errNonPositiveTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, found := s.records[key]
	if !found || !bytes.Equal(record.value, expected) {
		return false, nil
	}

	s.records[key] = memoryRecord{value: append([]byte(nil), completed...), expiresAt: time.Now().Add(ttl)}

	return true, nil
}

func (s *memoryStore) Release(_ context.Context, key string, expected []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, found := s.records[key]
	if !found || !bytes.Equal(record.value, expected) {
		return false, nil
	}

	delete(s.records, key)

	return true, nil
}
