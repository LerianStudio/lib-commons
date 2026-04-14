package eviction

import (
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindLRUEvictionCandidate_EmptyMap(t *testing.T) {
	t.Parallel()

	id, ok := FindLRUEvictionCandidate(
		5,                      // connectionCount
		5,                      // maxConnections (at capacity)
		map[string]time.Time{}, // empty lastAccessed
		time.Minute,            // idleTimeout
		testutil.NewMockLogger(),
	)

	assert.Empty(t, id)
	assert.False(t, ok)
}

func TestFindLRUEvictionCandidate_SingleEntry(t *testing.T) {
	t.Parallel()

	t.Run("active entry is not evicted", func(t *testing.T) {
		t.Parallel()

		lastAccessed := map[string]time.Time{
			"tenant-1": time.Now().Add(-10 * time.Second), // recently accessed
		}

		id, ok := FindLRUEvictionCandidate(
			1, // connectionCount
			1, // maxConnections (at capacity)
			lastAccessed,
			time.Minute, // idleTimeout = 1 min, entry only 10s old
			testutil.NewMockLogger(),
		)

		assert.Empty(t, id)
		assert.False(t, ok)
	})

	t.Run("idle entry is evicted", func(t *testing.T) {
		t.Parallel()

		lastAccessed := map[string]time.Time{
			"tenant-1": time.Now().Add(-10 * time.Minute), // idle for 10 minutes
		}

		id, ok := FindLRUEvictionCandidate(
			1, // connectionCount
			1, // maxConnections (at capacity)
			lastAccessed,
			time.Minute, // idleTimeout = 1 min
			testutil.NewMockLogger(),
		)

		assert.Equal(t, "tenant-1", id)
		assert.True(t, ok)
	})
}

func TestFindLRUEvictionCandidate_MultipleEntries(t *testing.T) {
	t.Parallel()

	t.Run("one idle among active entries", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		lastAccessed := map[string]time.Time{
			"tenant-active-1": now.Add(-10 * time.Second), // active
			"tenant-idle":     now.Add(-10 * time.Minute), // idle
			"tenant-active-2": now.Add(-30 * time.Second), // active
		}

		id, ok := FindLRUEvictionCandidate(
			3,
			3,
			lastAccessed,
			time.Minute,
			testutil.NewMockLogger(),
		)

		assert.Equal(t, "tenant-idle", id)
		assert.True(t, ok)
	})

	t.Run("all idle returns the oldest", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		lastAccessed := map[string]time.Time{
			"tenant-recent-idle": now.Add(-5 * time.Minute),  // idle 5 min
			"tenant-oldest-idle": now.Add(-30 * time.Minute), // idle 30 min (LRU)
			"tenant-medium-idle": now.Add(-15 * time.Minute), // idle 15 min
		}

		id, ok := FindLRUEvictionCandidate(
			3,
			3,
			lastAccessed,
			time.Minute,
			testutil.NewMockLogger(),
		)

		require.True(t, ok)
		assert.Equal(t, "tenant-oldest-idle", id)
	})

	t.Run("none idle allows pool to grow beyond limit", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		lastAccessed := map[string]time.Time{
			"tenant-1": now.Add(-10 * time.Second),
			"tenant-2": now.Add(-20 * time.Second),
			"tenant-3": now.Add(-30 * time.Second),
			"tenant-4": now.Add(-40 * time.Second),
		}

		// connectionCount (4) > maxConnections (3), but nothing is idle
		id, ok := FindLRUEvictionCandidate(
			4,
			3,
			lastAccessed,
			time.Minute,
			testutil.NewMockLogger(),
		)

		assert.Empty(t, id)
		assert.False(t, ok)
	})
}

func TestFindLRUEvictionCandidate_MaxConnectionsZero(t *testing.T) {
	t.Parallel()

	// maxConnections <= 0 disables eviction entirely (unlimited pool).
	// Even idle entries should NOT be evicted.
	lastAccessed := map[string]time.Time{
		"tenant-1": time.Now().Add(-1 * time.Hour), // very idle
	}

	id, ok := FindLRUEvictionCandidate(
		10,
		0, // unlimited: no eviction
		lastAccessed,
		time.Minute,
		testutil.NewMockLogger(),
	)

	assert.Empty(t, id)
	assert.False(t, ok)
}

func TestFindLRUEvictionCandidate_MaxConnectionsNegative(t *testing.T) {
	t.Parallel()

	// Negative maxConnections is treated the same as zero (unlimited).
	lastAccessed := map[string]time.Time{
		"tenant-1": time.Now().Add(-1 * time.Hour),
	}

	id, ok := FindLRUEvictionCandidate(
		5,
		-1,
		lastAccessed,
		time.Minute,
		testutil.NewMockLogger(),
	)

	assert.Empty(t, id)
	assert.False(t, ok)
}

func TestFindLRUEvictionCandidate_BelowCapacity(t *testing.T) {
	t.Parallel()

	// When connectionCount < maxConnections the pool has room -- no eviction.
	lastAccessed := map[string]time.Time{
		"tenant-1": time.Now().Add(-1 * time.Hour), // very idle, but pool has room
	}

	id, ok := FindLRUEvictionCandidate(
		1,  // connectionCount
		10, // maxConnections (plenty of room)
		lastAccessed,
		time.Minute,
		testutil.NewMockLogger(),
	)

	assert.Empty(t, id)
	assert.False(t, ok)
}

func TestFindLRUEvictionCandidate_DefaultIdleTimeout(t *testing.T) {
	t.Parallel()

	// When idleTimeout is 0, the function defaults to DefaultIdleTimeout (5 min).
	now := time.Now()
	lastAccessed := map[string]time.Time{
		"tenant-within-default": now.Add(-3 * time.Minute),  // 3 min < 5 min default
		"tenant-beyond-default": now.Add(-10 * time.Minute), // 10 min > 5 min default
	}

	id, ok := FindLRUEvictionCandidate(
		2,
		2,
		lastAccessed,
		0, // triggers default idle timeout
		testutil.NewMockLogger(),
	)

	require.True(t, ok)
	assert.Equal(t, "tenant-beyond-default", id)
}

func TestFindLRUEvictionCandidate_NilLogger(t *testing.T) {
	t.Parallel()

	t.Run("eviction found with nil logger", func(t *testing.T) {
		t.Parallel()

		lastAccessed := map[string]time.Time{
			"tenant-1": time.Now().Add(-10 * time.Minute),
		}

		id, ok := FindLRUEvictionCandidate(
			1,
			1,
			lastAccessed,
			time.Minute,
			nil, // nil logger -- must not panic
		)

		assert.Equal(t, "tenant-1", id)
		assert.True(t, ok)
	})

	t.Run("no eviction candidate with nil logger", func(t *testing.T) {
		t.Parallel()

		lastAccessed := map[string]time.Time{
			"tenant-1": time.Now().Add(-10 * time.Second), // active
		}

		id, ok := FindLRUEvictionCandidate(
			1,
			1,
			lastAccessed,
			time.Minute,
			nil, // nil logger -- must not panic on warn log path
		)

		assert.Empty(t, id)
		assert.False(t, ok)
	})
}

func TestFindLRUEvictionCandidate_LogMessages(t *testing.T) {
	t.Parallel()

	t.Run("logs warning when at capacity but nothing to evict", func(t *testing.T) {
		t.Parallel()

		logger := testutil.NewCapturingLogger()
		lastAccessed := map[string]time.Time{
			"tenant-1": time.Now().Add(-5 * time.Second), // active
		}

		id, ok := FindLRUEvictionCandidate(
			1,
			1,
			lastAccessed,
			time.Minute,
			logger,
		)

		assert.Empty(t, id)
		assert.False(t, ok)
		assert.True(t, logger.ContainsSubstring("no idle connections to evict"),
			"expected warning about no idle connections, got: %v", logger.GetMessages())
	})

	t.Run("logs info when evicting", func(t *testing.T) {
		t.Parallel()

		logger := testutil.NewCapturingLogger()
		lastAccessed := map[string]time.Time{
			"tenant-evicted": time.Now().Add(-10 * time.Minute),
		}

		id, ok := FindLRUEvictionCandidate(
			1,
			1,
			lastAccessed,
			time.Minute,
			logger,
		)

		require.True(t, ok)
		assert.Equal(t, "tenant-evicted", id)
		assert.True(t, logger.ContainsSubstring("evicting idle tenant connection"),
			"expected eviction info log, got: %v", logger.GetMessages())
		assert.True(t, logger.ContainsSubstring("tenant-evicted"),
			"expected tenant ID in log, got: %v", logger.GetMessages())
	})
}

func TestFindLRUEvictionCandidate_TableDriven(t *testing.T) {
	t.Parallel()

	now := time.Now()
	idleTimeout := time.Minute

	tests := []struct {
		name            string
		connectionCount int
		maxConnections  int
		lastAccessed    map[string]time.Time
		idleTimeout     time.Duration
		expectedID      string
		expectedOK      bool
	}{
		{
			name:            "empty map at capacity",
			connectionCount: 5,
			maxConnections:  5,
			lastAccessed:    map[string]time.Time{},
			idleTimeout:     idleTimeout,
			expectedID:      "",
			expectedOK:      false,
		},
		{
			name:            "nil map at capacity",
			connectionCount: 5,
			maxConnections:  5,
			lastAccessed:    nil,
			idleTimeout:     idleTimeout,
			expectedID:      "",
			expectedOK:      false,
		},
		{
			name:            "below capacity with idle entries",
			connectionCount: 2,
			maxConnections:  5,
			lastAccessed: map[string]time.Time{
				"t1": now.Add(-10 * time.Minute),
			},
			idleTimeout: idleTimeout,
			expectedID:  "",
			expectedOK:  false,
		},
		{
			name:            "at capacity single idle",
			connectionCount: 1,
			maxConnections:  1,
			lastAccessed: map[string]time.Time{
				"t1": now.Add(-5 * time.Minute),
			},
			idleTimeout: idleTimeout,
			expectedID:  "t1",
			expectedOK:  true,
		},
		{
			name:            "above capacity selects oldest idle",
			connectionCount: 5,
			maxConnections:  3,
			lastAccessed: map[string]time.Time{
				"recent":  now.Add(-2 * time.Minute),
				"oldest":  now.Add(-20 * time.Minute),
				"middle":  now.Add(-10 * time.Minute),
				"active1": now.Add(-10 * time.Second),
				"active2": now.Add(-30 * time.Second),
			},
			idleTimeout: idleTimeout,
			expectedID:  "oldest",
			expectedOK:  true,
		},
		{
			name:            "maxConnections zero disables eviction",
			connectionCount: 100,
			maxConnections:  0,
			lastAccessed: map[string]time.Time{
				"t1": now.Add(-1 * time.Hour),
			},
			idleTimeout: idleTimeout,
			expectedID:  "",
			expectedOK:  false,
		},
		{
			name:            "boundary: idle duration well under timeout is not evicted",
			connectionCount: 1,
			maxConnections:  1,
			lastAccessed: map[string]time.Time{
				// The eviction check uses `idleDuration < idleTimeout` (strictly
				// less-than), so entries whose idle time equals the timeout ARE
				// eligible. We place the entry comfortably under the threshold
				// (30 second buffer) to avoid clock drift between the test's
				// `now` and FindLRUEvictionCandidate's internal time.Now().
				"t1": now.Add(-idleTimeout + 30*time.Second),
			},
			idleTimeout: idleTimeout,
			expectedID:  "",
			expectedOK:  false,
		},
		{
			name:            "boundary: idle duration just past timeout is evicted",
			connectionCount: 1,
			maxConnections:  1,
			lastAccessed: map[string]time.Time{
				"t1": now.Add(-idleTimeout - 30*time.Second),
			},
			idleTimeout: idleTimeout,
			expectedID:  "t1",
			expectedOK:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id, ok := FindLRUEvictionCandidate(
				tt.connectionCount,
				tt.maxConnections,
				tt.lastAccessed,
				tt.idleTimeout,
				testutil.NewMockLogger(),
			)

			assert.Equal(t, tt.expectedOK, ok, "eviction decision mismatch")
			assert.Equal(t, tt.expectedID, id, "evicted tenant ID mismatch")
		})
	}
}

func TestDefaultIdleTimeout(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 5*time.Minute, DefaultIdleTimeout,
		"DefaultIdleTimeout should be 5 minutes")
}
