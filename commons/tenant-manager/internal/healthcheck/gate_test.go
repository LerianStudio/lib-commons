//go:build unit

package healthcheck

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tenant = "tenant-1"

func TestGate_FirstCallerRunsTheCheck(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	action, done := gate.Begin(tenant)

	assert.Equal(t, Run, action)
	assert.Nil(t, done)
}

func TestGate_PassedCheckIsSkippedInsideTheInterval(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	action, _ := gate.Begin(tenant)
	require.Equal(t, Run, action)

	gate.End(tenant, true)

	action, done := gate.Begin(tenant)

	assert.Equal(t, Skip, action)
	assert.Nil(t, done)
}

func TestGate_FailedCheckIsNotRecorded(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	action, _ := gate.Begin(tenant)
	require.Equal(t, Run, action)

	gate.End(tenant, false)

	action, _ = gate.Begin(tenant)

	assert.Equal(t, Run, action, "a failed check must leave the next caller due to check the replacement")
}

func TestGate_ElapsedIntervalRunsAgain(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Nanosecond)

	action, _ := gate.Begin(tenant)
	require.Equal(t, Run, action)
	gate.End(tenant, true)

	action, _ = gate.Begin(tenant)

	assert.Equal(t, Run, action)
}

func TestGate_SecondCallerWaitsForTheCheckInFlight(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	action, _ := gate.Begin(tenant)
	require.Equal(t, Run, action)

	action, done := gate.Begin(tenant)
	require.Equal(t, Wait, action)
	require.NotNil(t, done)

	select {
	case <-done:
		t.Fatal("the verdict was published before the check ended")
	default:
	}

	gate.End(tenant, true)

	require.NoError(t, Await(context.Background(), done))
}

func TestGate_AwaitHonoursContext(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	action, _ := gate.Begin(tenant)
	require.Equal(t, Run, action)

	_, done := gate.Begin(tenant)
	require.NotNil(t, done)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.ErrorIs(t, Await(ctx, done), context.Canceled,
		"a caller whose request is gone must not keep waiting for someone else's check")

	gate.End(tenant, true)
}

func TestGate_DisabledIntervalRunsEveryCaller(t *testing.T) {
	t.Parallel()

	for _, interval := range []time.Duration{0, -5 * time.Second} {
		gate := NewGate(interval)

		action, done := gate.Begin(tenant)
		require.Equal(t, Run, action)
		require.Nil(t, done, "a disabled gate must not make anyone wait")

		gate.End(tenant, true)

		action, _ = gate.Begin(tenant)
		assert.Equal(t, Run, action, "a disabled gate must never skip a check")
	}
}

func TestGate_ForgetMakesTheNextCallerCheck(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	action, _ := gate.Begin(tenant)
	require.Equal(t, Run, action)
	gate.End(tenant, true)

	gate.Forget(tenant)

	action, _ = gate.Begin(tenant)

	assert.Equal(t, Run, action)
}

func TestGate_ForgetAllMakesEveryCallerCheck(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	for _, key := range []string{"tenant-a", "tenant-b"} {
		action, _ := gate.Begin(key)
		require.Equal(t, Run, action)
		gate.End(key, true)
	}

	gate.ForgetAll()

	for _, key := range []string{"tenant-a", "tenant-b"} {
		action, _ := gate.Begin(key)
		assert.Equal(t, Run, action)
	}
}

func TestGate_ForgetLeavesTheCheckInFlightAlone(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	action, _ := gate.Begin(tenant)
	require.Equal(t, Run, action)

	_, done := gate.Begin(tenant)
	require.NotNil(t, done)

	// An eviction elsewhere must not orphan the caller waiting on this check.
	gate.Forget(tenant)
	gate.ForgetAll()

	gate.End(tenant, false)

	require.NoError(t, Await(context.Background(), done))
}

func TestGate_NilGateIsUsable(t *testing.T) {
	t.Parallel()

	var gate *Gate

	action, done := gate.Begin(tenant)

	assert.Equal(t, Run, action)
	assert.Nil(t, done)

	gate.End(tenant, true)
	gate.Forget(tenant)
	gate.ForgetAll()

	assert.NoError(t, Await(context.Background(), nil))
}

func TestGate_KeysAreIndependent(t *testing.T) {
	t.Parallel()

	gate := NewGate(time.Hour)

	action, _ := gate.Begin("tenant-a")
	require.Equal(t, Run, action)

	action, done := gate.Begin("tenant-b")

	assert.Equal(t, Run, action, "a check in flight for one tenant must not gate another")
	assert.Nil(t, done)
}
