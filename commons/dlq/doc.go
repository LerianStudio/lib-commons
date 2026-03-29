// Package dlq provides a Redis-backed dead letter queue with exponential backoff retry.
//
// Messages that fail processing are enqueued into tenant-isolated Redis lists
// and retried with configurable exponential backoff. A background Consumer polls
// these lists, invokes a caller-provided RetryFunc, and discards messages that
// either succeed or exhaust their retry budget.
package dlq
