// Package errgroup coordinates goroutines that share a cancellation context.
//
// The first goroutine error cancels the group context and is returned by Wait;
// recovered panics are converted into errors.
package errgroup
