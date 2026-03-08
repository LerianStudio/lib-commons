//go:build unit

package safe

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCacheLen returns the current number of entries in the regex cache.
// This is a test-only helper to verify cache behavior without exporting
// the function from the production code.
func testCacheLen() int {
	regexMu.RLock()
	defer regexMu.RUnlock()

	return len(regexCache)
}

// TestCompile verifies safe regex compilation and caching behavior.
// t.Parallel() is intentionally omitted because this test mutates the
// package-level regexCache via ClearCache, which would race with other
// cache-dependent tests running concurrently.
func TestCompile(t *testing.T) {
	ClearCache()

	t.Run("valid pattern", func(t *testing.T) {
		re, err := Compile(`^\d{4}-\d{2}-\d{2}$`)

		assert.NoError(t, err)
		assert.NotNil(t, re)
		assert.True(t, re.MatchString("2026-01-27"))
		assert.False(t, re.MatchString("invalid"))
	})

	t.Run("invalid pattern", func(t *testing.T) {
		re, err := Compile(`[invalid(`)

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidRegex)
		assert.Nil(t, re)
	})

	t.Run("caching", func(t *testing.T) {
		ClearCache()

		pattern := `^\d+$`

		re1, err1 := Compile(pattern)
		re2, err2 := Compile(pattern)

		assert.NoError(t, err1)
		assert.NoError(t, err2)
		assert.Same(t, re1, re2)
	})

	t.Run("empty pattern", func(t *testing.T) {
		re, err := Compile("")

		assert.NoError(t, err)
		assert.NotNil(t, re)
		assert.True(t, re.MatchString("anything"))
	})
}

// TestCompilePOSIX verifies POSIX regex compilation and caching.
// t.Parallel() is intentionally omitted because this test mutates the
// package-level regexCache via ClearCache, which would race with other
// cache-dependent tests running concurrently.
func TestCompilePOSIX(t *testing.T) {
	ClearCache()

	t.Run("valid pattern", func(t *testing.T) {
		re, err := CompilePOSIX(`^[0-9]+$`)

		assert.NoError(t, err)
		assert.NotNil(t, re)
		assert.True(t, re.MatchString("12345"))
		assert.False(t, re.MatchString("abc"))
	})

	t.Run("invalid pattern", func(t *testing.T) {
		re, err := CompilePOSIX(`[invalid(`)

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidRegex)
		assert.Nil(t, re)
	})

	t.Run("caching", func(t *testing.T) {
		ClearCache()

		pattern := `^[a-z]+$`

		re1, err1 := CompilePOSIX(pattern)
		re2, err2 := CompilePOSIX(pattern)

		assert.NoError(t, err1)
		assert.NoError(t, err2)
		assert.Same(t, re1, re2)
	})
}

// TestMatchString verifies the convenience MatchString wrapper.
// t.Parallel() is intentionally omitted because this test mutates the
// package-level regexCache via ClearCache, which would race with other
// cache-dependent tests running concurrently.
func TestMatchString(t *testing.T) {
	ClearCache()

	t.Run("valid pattern match", func(t *testing.T) {
		matched, err := MatchString(`^\d{4}-\d{2}-\d{2}$`, "2026-01-27")

		assert.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("valid pattern no match", func(t *testing.T) {
		matched, err := MatchString(`^\d{4}-\d{2}-\d{2}$`, "invalid-date")

		assert.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("invalid pattern", func(t *testing.T) {
		matched, err := MatchString(`[invalid(`, "test")

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidRegex)
		assert.False(t, matched)
	})
}

// TestFindString verifies the convenience FindString wrapper.
// t.Parallel() is intentionally omitted because this test mutates the
// package-level regexCache via ClearCache, which would race with other
// cache-dependent tests running concurrently.
func TestFindString(t *testing.T) {
	ClearCache()

	t.Run("valid pattern match", func(t *testing.T) {
		match, err := FindString(`[a-z]+`, "123abc456")

		assert.NoError(t, err)
		assert.Equal(t, "abc", match)
	})

	t.Run("valid pattern no match", func(t *testing.T) {
		match, err := FindString(`[a-z]+`, "123456")

		assert.NoError(t, err)
		assert.Empty(t, match)
	})

	t.Run("invalid pattern", func(t *testing.T) {
		match, err := FindString(`[invalid(`, "test")

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidRegex)
		assert.Empty(t, match)
	})
}

// TestClearCache verifies that ClearCache removes all cached entries and
// subsequent compilations produce new regex instances.
// t.Parallel() is intentionally omitted because this test mutates the
// package-level regexCache via ClearCache, which would race with other
// cache-dependent tests running concurrently.
func TestClearCache(t *testing.T) {
	pattern := `^test$`

	re1, _ := Compile(pattern)
	ClearCache()

	re2, _ := Compile(pattern)

	assert.NotSame(t, re1, re2)
}

// TestCacheBoundedSize verifies that the regex cache does not grow beyond
// maxCacheSize entries. When the cache is full, storing a new entry evicts
// approximately 25% of entries to reclaim space while preserving hot entries.
// t.Parallel() is intentionally omitted because this test mutates the
// package-level regexCache via ClearCache, which would race with other
// cache-dependent tests running concurrently.
func TestCacheBoundedSize(t *testing.T) {
	ClearCache()

	// Fill the cache to maxCacheSize.
	for i := range maxCacheSize {
		pattern := fmt.Sprintf(`^pattern_%d$`, i)

		_, err := Compile(pattern)
		require.NoError(t, err)
	}

	require.Equal(t, maxCacheSize, testCacheLen(), "cache should be full at maxCacheSize")

	// One more entry should trigger ~25% eviction + store the new entry.
	_, err := Compile(`^overflow_pattern$`)
	require.NoError(t, err)

	cacheLen := testCacheLen()
	// After evicting ~256 entries (25% of 1024) and adding 1, we expect ~769.
	// Allow a range to account for the exact eviction count.
	require.Less(t, cacheLen, maxCacheSize, "cache should be smaller than maxCacheSize after eviction")
	require.Greater(t, cacheLen, maxCacheSize/2, "cache should retain majority of entries (random 25%% eviction)")

	// Re-compiled patterns should still be cached after compilation.
	re1, _ := Compile(`^pattern_fresh$`)
	re2, _ := Compile(`^pattern_fresh$`)
	assert.Same(t, re1, re2, "same pattern compiled twice should return cached instance")
}

// TestCacheBoundedSizePOSIX verifies the same bounded cache behavior for
// POSIX patterns, which share the same cache with a "posix:" key prefix.
// t.Parallel() is intentionally omitted because this test mutates the
// package-level regexCache via ClearCache, which would race with other
// cache-dependent tests running concurrently.
func TestCacheBoundedSizePOSIX(t *testing.T) {
	ClearCache()

	// Fill the cache to maxCacheSize with POSIX patterns.
	for i := range maxCacheSize {
		pattern := fmt.Sprintf(`^posix_%d$`, i)

		_, err := CompilePOSIX(pattern)
		require.NoError(t, err)
	}

	require.Equal(t, maxCacheSize, testCacheLen(), "cache should be full at maxCacheSize")

	// One more POSIX entry should trigger ~25% eviction.
	_, err := CompilePOSIX(`^posix_overflow$`)
	require.NoError(t, err)

	cacheLen := testCacheLen()
	require.Less(t, cacheLen, maxCacheSize, "cache should be smaller than maxCacheSize after eviction")
	require.Greater(t, cacheLen, maxCacheSize/2, "cache should retain majority of entries")
}
