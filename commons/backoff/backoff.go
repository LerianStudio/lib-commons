package backoff

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	mrand "math/rand/v2"
	"time"
)

const maxShift = 62

// Exponential calculates exponential delay based on attempt number.
// The delay is calculated as base * 2^attempt with overflow protection.
// Negative attempts are treated as 0.
func Exponential(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}

	if attempt < 0 {
		attempt = 0
	} else if attempt > maxShift {
		attempt = maxShift
	}

	multiplier := int64(1 << attempt)

	baseInt := int64(base)
	if baseInt > math.MaxInt64/multiplier {
		return time.Duration(math.MaxInt64)
	}

	return time.Duration(baseInt * multiplier)
}

// FullJitter returns a random duration in the range [0, delay).
// Uses crypto/rand for secure randomness, falling back to math/rand if crypto fails.
// Returns 0 for zero or negative delays.
func FullJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}

	n, err := rand.Int(rand.Reader, big.NewInt(int64(delay)))
	if err != nil {
		return time.Duration(cryptoFallbackRand(int64(delay)))
	}

	return time.Duration(n.Int64())
}

// fallbackDivisor is used when crypto/rand fails completely.
const fallbackDivisor = 2

// cryptoFallbackRand provides a fallback random number generator when crypto/rand fails.
// It uses a defense-in-depth strategy with two fallback layers:
//   - Layer 1: Attempt to seed a math/rand PRNG via crypto/rand. Even though
//     FullJitter's crypto/rand.Int already failed, rand.Read uses a different
//     code path (raw bytes vs big.Int) and may succeed independently.
//   - Layer 2: If even seeding fails, return a deterministic midpoint
//     (maxValue / 2) to provide a reasonable jitter value without blocking.
//
// This ensures backoff jitter never stalls, even under severe entropy exhaustion.
func cryptoFallbackRand(maxValue int64) int64 {
	var seed [8]byte

	_, err := rand.Read(seed[:])
	if err != nil {
		return maxValue / fallbackDivisor
	}

	rng := mrand.New(
		mrand.NewPCG(binary.LittleEndian.Uint64(seed[:]), 0),
	) // #nosec G404 -- Fallback when crypto/rand fails

	return rng.Int64N(maxValue)
}

// ExponentialWithJitter combines exponential backoff with full jitter.
// Returns a random duration in [0, base * 2^attempt).
// This implements the "Full Jitter" strategy recommended by AWS.
func ExponentialWithJitter(base time.Duration, attempt int) time.Duration {
	exponentialDelay := Exponential(base, attempt)

	return FullJitter(exponentialDelay)
}

// WaitContext sleeps for the specified duration but respects context cancellation.
// Returns nil if the sleep completes, or an error if the context is cancelled.
// Returns immediately (nil) for zero or negative durations.
// A nil context is normalized to context.Background().
func WaitContext(ctx context.Context, duration time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if duration <= 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("context done: %w", ctx.Err())
	}
}
