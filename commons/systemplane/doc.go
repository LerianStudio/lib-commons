// Copyright 2025 Lerian Studio.

// Package systemplane provides a dual-backend runtime configuration library.
//
// It allows services to hot-reload a small set of operational knobs (log levels,
// feature flags, rate limits, circuit-breaker thresholds) without a pod restart.
// Reads are low-contention (read-locked) and nil-receiver safe; writes are persisted to either
// Postgres (with LISTEN/NOTIFY change-feed) or MongoDB (with change-streams or
// optional polling).
//
// Construct a Client with [NewPostgres] or [NewMongoDB], register keys with
// [Client.Register], call [Client.Start] to hydrate initial values and begin
// listening for changes, and read values with the typed accessors ([Client.GetString],
// [Client.GetInt], etc.). Use [Client.OnChange] to react to live updates.
//
// Settings that imply resource teardown (DB DSNs, secrets, TLS material, listen
// addresses) belong in environment variables, not in the runtime config plane.
package systemplane
