// Package backoff provides retry delay helpers with exponential growth and jitter.
//
// Use ExponentialWithJitter for retry loops and WaitContext to wait while
// respecting cancellation and deadlines.
package backoff
