//go:build unit

package systemplane

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyTenantTestStore is a minimal TestStore implementation that returns
// zero values from every method. Tenant registration tests do not exercise
// any store path beyond Start's hydration (which reads an empty List), so a
// silent store is sufficient. Real tenant semantics land in Task 7's suite.
type emptyTenantTestStore struct{}

func (emptyTenantTestStore) List(_ context.Context) ([]TestEntry, error) { return nil, nil }
func (emptyTenantTestStore) Get(_ context.Context, _, _ string) (TestEntry, bool, error) {
	return TestEntry{}, false, nil
}
func (emptyTenantTestStore) Set(_ context.Context, _ TestEntry) error { return nil }
func (emptyTenantTestStore) Subscribe(ctx context.Context, _ func(TestEvent)) error {
	<-ctx.Done()

	return nil
}
func (emptyTenantTestStore) Close() error { return nil }
func (emptyTenantTestStore) GetTenantValue(_ context.Context, _, _, _ string) (TestEntry, bool, error) {
	return TestEntry{}, false, nil
}

func (emptyTenantTestStore) SetTenantValue(_ context.Context, _ string, _ TestEntry) error {
	return nil
}

func (emptyTenantTestStore) DeleteTenantValue(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (emptyTenantTestStore) ListTenantOverrides(_ context.Context, _, _, _ string, _ int) ([]TestEntry, error) {
	return nil, nil
}

func (emptyTenantTestStore) ListTenantsForKey(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

func newClientForTest(t *testing.T, opts ...Option) *Client {
	t.Helper()

	c, err := NewForTesting(emptyTenantTestStore{}, opts...)
	require.NoError(t, err, "NewForTesting")
	t.Cleanup(func() { _ = c.Close() })

	return c
}

func TestRegisterTenantScoped_BeforeStart_Succeeds(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t)

	err := c.RegisterTenantScoped("global", "fees.fail_closed_default", false)
	require.NoError(t, err, "RegisterTenantScoped before Start should succeed")

	// Registry should contain the key AND mark it tenant-scoped.
	c.registryMu.RLock()
	_, inRegistry := c.registry[nskey{Namespace: "global", Key: "fees.fail_closed_default"}]
	_, inTenantRegistry := c.tenantScopedRegistry[nskey{Namespace: "global", Key: "fees.fail_closed_default"}]
	c.registryMu.RUnlock()

	assert.True(t, inRegistry, "key should be present in registry")
	assert.True(t, inTenantRegistry, "key should be marked tenant-scoped")

	// Cache should be seeded with the default value so a pre-Start Get works.
	v, ok := c.Get("global", "fees.fail_closed_default")
	assert.True(t, ok, "Get should find the tenant-scoped key")
	assert.Equal(t, false, v, "Get should return the registered default")
}

func TestRegisterTenantScoped_AfterStart_ReturnsErrRegisterAfterStart(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NoError(t, c.Start(ctx), "Start")

	err := c.RegisterTenantScoped("global", "late.key", "x")
	require.Error(t, err, "RegisterTenantScoped after Start should error")
	assert.ErrorIs(t, err, ErrRegisterAfterStart, "error should wrap ErrRegisterAfterStart")
}

func TestRegisterTenantScoped_DuplicateReturnsErrDuplicateKey(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t)

	require.NoError(t, c.RegisterTenantScoped("ns", "k", "v1"), "first Register")

	err := c.RegisterTenantScoped("ns", "k", "v2")
	require.Error(t, err, "duplicate RegisterTenantScoped should error")
	assert.ErrorIs(t, err, ErrDuplicateKey, "error should wrap ErrDuplicateKey")
}

// TestRegisterTenantScoped_DuplicateCrossKindReturnsErrDuplicateKey verifies
// that Register and RegisterTenantScoped share a single (ns,key) namespace —
// attempting both on the same key fails with ErrDuplicateKey regardless of
// order. This is a behavior pin supporting TRD §2.2's "single registry under
// registryMu" claim.
func TestRegisterTenantScoped_DuplicateCrossKindReturnsErrDuplicateKey(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t)

	require.NoError(t, c.Register("ns", "k", "v1"), "Register first")

	err := c.RegisterTenantScoped("ns", "k", "v2")
	require.Error(t, err, "RegisterTenantScoped after Register on same key should error")
	assert.ErrorIs(t, err, ErrDuplicateKey, "error should wrap ErrDuplicateKey")
}

func TestRegisterTenantScoped_InvalidArgsReturnsValidationError(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t)

	err := c.RegisterTenantScoped("", "k", "v")
	require.Error(t, err, "empty namespace should error")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")

	err = c.RegisterTenantScoped("ns", "", "v")
	require.Error(t, err, "empty key should error")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")
}

// TestRegister_ReservedKeyTenantsRejected locks the admin-routing preemption:
// registering a key literally named "tenants" would shadow the admin surface
// at /<prefix>/:namespace/:key/tenants. Both Register and RegisterTenantScoped
// must reject it so the collision cannot be introduced through either path.
func TestRegister_ReservedKeyTenantsRejected(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t)

	err := c.Register("global", "tenants", "v")
	require.Error(t, err, "Register should reject the reserved 'tenants' key")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")

	err = c.RegisterTenantScoped("global", "tenants", "v")
	require.Error(t, err, "RegisterTenantScoped should reject the reserved 'tenants' key")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")
}

// TestRegister_UnitSeparatorRejected locks the singleflight composite-key
// guard: namespaces or keys containing U+001F could collide on the same
// slot because singleflightKey concatenates via that delimiter. Both
// Register and RegisterTenantScoped must reject.
func TestRegister_UnitSeparatorRejected(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t)

	err := c.Register("global\x1fevil", "k", "v")
	require.Error(t, err, "Register should reject U+001F in namespace")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")

	err = c.Register("global", "k\x1fevil", "v")
	require.Error(t, err, "Register should reject U+001F in key")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")

	err = c.RegisterTenantScoped("global\x1fevil", "k", "v")
	require.Error(t, err, "RegisterTenantScoped should reject U+001F in namespace")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")

	err = c.RegisterTenantScoped("global", "k\x1fevil", "v")
	require.Error(t, err, "RegisterTenantScoped should reject U+001F in key")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")
}

func TestRegisterTenantScoped_ValidatorRejectsDefault(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t)

	rejecting := func(_ any) error { return errors.New("always reject") }

	err := c.RegisterTenantScoped("ns", "k", "v", WithValidator(rejecting))
	require.Error(t, err, "validator that rejects default should error at registration")
	assert.ErrorIs(t, err, ErrValidation, "error should wrap ErrValidation")

	// Registry should NOT have been populated when validation fails.
	c.registryMu.RLock()
	_, inRegistry := c.registry[nskey{Namespace: "ns", Key: "k"}]
	c.registryMu.RUnlock()
	assert.False(t, inRegistry, "registry should remain empty on validator rejection")
}

// Nil-receiver coverage for RegisterTenantScoped lives in the consolidated
// TestNilClient_TenantMethods table in tenant_scoped_test.go.

func TestWithLazyTenantLoad_ConfiguresLRU(t *testing.T) {
	t.Parallel()

	c := newClientForTest(t, WithLazyTenantLoad(5))

	assert.Equal(t, tenantLoadLazy, c.tenantLoadMode, "client should report lazy mode")
	require.NotNil(t, c.tenantCache, "tenantCache should be initialized")
	assert.Equal(t, tenantLoadLazy, c.tenantCache.mode(), "tenantCache.mode() should report lazy")

	// Concrete type assertion confirms the LRU path is wired, not eager-fallback.
	_, ok := c.tenantCache.(*tenantCacheLRU)
	assert.True(t, ok, "tenantCache should be the LRU implementation")
}

func TestWithLazyTenantLoad_NonPositiveFallsBackToEager(t *testing.T) {
	t.Parallel()

	// A zero or negative bound is treated as "disabled" per WithLazyTenantLoad's
	// doc comment — eager mode remains active.
	for _, max := range []int{0, -1, -100} {
		max := max
		t.Run("", func(t *testing.T) {
			t.Parallel()

			c := newClientForTest(t, WithLazyTenantLoad(max))
			assert.Equal(t, tenantLoadEager, c.tenantLoadMode, "non-positive bound should keep eager mode, got max=%d", max)
		})
	}
}

func TestTenantCache_LRUEvictsOldest(t *testing.T) {
	t.Parallel()

	const max = 3
	lru := newTenantCacheLRU(max, nil)

	ns := "ns"
	nks := []nskey{
		{Namespace: ns, Key: "k0"},
		{Namespace: ns, Key: "k1"},
		{Namespace: ns, Key: "k2"},
		{Namespace: ns, Key: "k3"},
	}

	// Insert 3 — all fit.
	lru.set("t", nks[0], "v0")
	lru.set("t", nks[1], "v1")
	lru.set("t", nks[2], "v2")

	for i, nk := range nks[:3] {
		v, ok := lru.get("t", nk)
		assert.Truef(t, ok, "entry %d should be present", i)
		assert.Equalf(t, "v"+string(rune('0'+i)), v, "entry %d value mismatch", i)
	}

	// Insert a 4th — oldest (k0) must be evicted. We just read k0/k1/k2 in
	// order above, so LRU order (oldest-first) is k0 → k1 → k2.
	lru.set("t", nks[3], "v3")

	_, ok := lru.get("t", nks[0])
	assert.False(t, ok, "k0 should have been evicted as LRU")

	// k1, k2, k3 should all be present.
	for _, nk := range nks[1:] {
		_, ok := lru.get("t", nk)
		assert.Truef(t, ok, "%s should still be resident", nk.Key)
	}
}

func TestTenantCache_LRUPromoteOnGet(t *testing.T) {
	t.Parallel()

	const max = 3
	lru := newTenantCacheLRU(max, nil)

	ns := "ns"
	nks := []nskey{
		{Namespace: ns, Key: "k0"},
		{Namespace: ns, Key: "k1"},
		{Namespace: ns, Key: "k2"},
		{Namespace: ns, Key: "k3"},
	}

	// Insert 3. Initial LRU order (oldest → newest): k0, k1, k2.
	lru.set("t", nks[0], "v0")
	lru.set("t", nks[1], "v1")
	lru.set("t", nks[2], "v2")

	// get(k0) promotes k0 to MRU. New order (oldest → newest): k1, k2, k0.
	v, ok := lru.get("t", nks[0])
	require.True(t, ok)
	assert.Equal(t, "v0", v)

	// Now insert k3. Eviction target should be k1 (new oldest), NOT k0.
	lru.set("t", nks[3], "v3")

	_, ok = lru.get("t", nks[1])
	assert.False(t, ok, "k1 should have been evicted after k0 was promoted")

	_, ok = lru.get("t", nks[0])
	assert.True(t, ok, "k0 should be resident (was promoted before eviction)")

	_, ok = lru.get("t", nks[2])
	assert.True(t, ok, "k2 should be resident")

	_, ok = lru.get("t", nks[3])
	assert.True(t, ok, "k3 should be resident")
}

// TestTenantCache_LRUUpdateInPlace verifies that re-setting an existing key
// updates the value and promotes it (no eviction, no capacity growth).
func TestTenantCache_LRUUpdateInPlace(t *testing.T) {
	t.Parallel()

	const max = 2
	lru := newTenantCacheLRU(max, nil)

	nk0 := nskey{Namespace: "ns", Key: "k0"}
	nk1 := nskey{Namespace: "ns", Key: "k1"}

	lru.set("t", nk0, "v0")
	lru.set("t", nk1, "v1")

	// Update k0. Capacity is 2; both entries must still be resident.
	lru.set("t", nk0, "v0-new")

	v, ok := lru.get("t", nk0)
	require.True(t, ok, "k0 should still be resident after update")
	assert.Equal(t, "v0-new", v, "k0 value should be updated")

	v, ok = lru.get("t", nk1)
	require.True(t, ok, "k1 should still be resident (no eviction on in-place update)")
	assert.Equal(t, "v1", v)
}

// TestTenantCache_EagerBasicOps is a smoke test for the eager cache — a trivial
// get/set/delete round-trip to lock the interface contract that the
// eager implementation honors. The LRU suite above carries the subtle behavior.
func TestTenantCache_EagerBasicOps(t *testing.T) {
	t.Parallel()

	c := newTenantCacheEager()
	assert.Equal(t, tenantLoadEager, c.mode(), "eager cache should report eager mode")

	nk := nskey{Namespace: "ns", Key: "k"}

	_, ok := c.get("tenant-A", nk)
	assert.False(t, ok, "empty cache should miss")

	c.set("tenant-A", nk, 42)

	v, ok := c.get("tenant-A", nk)
	require.True(t, ok, "set then get should hit")
	assert.Equal(t, 42, v)

	// Different tenant should miss (isolation).
	_, ok = c.get("tenant-B", nk)
	assert.False(t, ok, "different tenant should not share the value")

	// Delete + re-get should miss.
	c.delete("tenant-A", nk)

	_, ok = c.get("tenant-A", nk)
	assert.False(t, ok, "deleted entry should miss")
}
