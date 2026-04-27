// Package mongo provides MongoDB adapters for outbox repository contracts.
//
// The phase-1 repository supports durable outbox persistence and dispatcher
// replay flows, but does not join caller-supplied SQL transactions.
package mongo
