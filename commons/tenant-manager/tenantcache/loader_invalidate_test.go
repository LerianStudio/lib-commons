//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package tenantcache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/cache"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spyConfigCache is a cache.ConfigCache test double that records Del calls.
type spyConfigCache struct {
	mu      sync.Mutex
	store   map[string]string
	delKeys []string
}

func newSpyConfigCache() *spyConfigCache {
	return &spyConfigCache{store: make(map[string]string)}
}

func (s *spyConfigCache) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.store[key]
	if !ok {
		return "", cache.ErrCacheMiss
	}

	return v, nil
}

func (s *spyConfigCache) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.store[key] = value

	return nil
}

func (s *spyConfigCache) Del(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.delKeys = append(s.delKeys, key)
	delete(s.store, key)

	return nil
}

func (s *spyConfigCache) delCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0

	for _, k := range s.delKeys {
		if k == key {
			n++
		}
	}

	return n
}

func TestTenantLoader_InvalidateClientCache_DelegatesToClient(t *testing.T) {
	t.Parallel()

	spy := newSpyConfigCache()

	pmClient, err := client.NewClient(
		"http://localhost:8080",
		log.NewNop(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey(testServiceAPIKey),
		client.WithCache(spy),
	)
	require.NoError(t, err, "client should be created")

	t.Cleanup(func() { _ = pmClient.Close() })

	cacheStore := NewTenantCache()
	loader := NewTenantLoader(pmClient, cacheStore, testServiceName, DefaultTenantCacheTTL, log.NewNop())

	tenantID := "tenant-invalidate-001"
	// Seed tier-2 directly so the Del has something to remove.
	require.NoError(t, spy.Set(context.Background(),
		"tenant-connections:"+tenantID+":"+testServiceName, "{}", time.Hour))

	err = loader.InvalidateClientCache(context.Background(), tenantID, testServiceName)
	require.NoError(t, err, "InvalidateClientCache should succeed")

	assert.Equal(t, 1, spy.delCount("tenant-connections:"+tenantID+":"+testServiceName),
		"InvalidateClientCache should delegate to client and Del the tier-2 key exactly once")
}

func TestTenantLoader_InvalidateClientCache_NilClient_NoPanic(t *testing.T) {
	t.Parallel()

	loader := &TenantLoader{}

	err := loader.InvalidateClientCache(context.Background(), "tenant-x", testServiceName)
	require.NoError(t, err, "nil pmClient must return nil without panic")
}

func TestTenantLoader_InvalidateClientCache_NilReceiver_NoPanic(t *testing.T) {
	t.Parallel()

	var l *TenantLoader

	err := l.InvalidateClientCache(context.Background(), "tenant-x", "svc")
	require.NoError(t, err, "nil receiver must return nil without panic")
}

// errDelConfigCache returns an error from Del to exercise the error-wrap path.
type errDelConfigCache struct {
	*spyConfigCache
	delErr error
}

func (e *errDelConfigCache) Del(_ context.Context, _ string) error {
	return e.delErr
}

func TestTenantLoader_InvalidateClientCache_PropagatesDelError(t *testing.T) {
	t.Parallel()

	errCache := &errDelConfigCache{spyConfigCache: newSpyConfigCache(), delErr: assert.AnError}

	pmClient, err := client.NewClient(
		"http://localhost:8080",
		log.NewNop(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey(testServiceAPIKey),
		client.WithCache(errCache),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = pmClient.Close() })

	loader := NewTenantLoader(pmClient, NewTenantCache(), testServiceName, DefaultTenantCacheTTL, log.NewNop())

	err = loader.InvalidateClientCache(context.Background(), "tenant-delerr", testServiceName)
	require.Error(t, err, "Del error must propagate")
	assert.ErrorIs(t, err, assert.AnError, "wrapped error should preserve the underlying cause")
}
