// Package server provides server lifecycle and graceful shutdown helpers.
//
// Use this package to coordinate signal handling, shutdown deadlines, and ordered
// resource cleanup for HTTP/gRPC service processes.
//
// ServerManager supports Fiber HTTP servers via WithHTTPServer, stdlib
// *http.Server instances via WithStdlibHTTPServer, pre-bound stdlib listeners
// via WithStdlibHTTPListener, and gRPC servers via WithGRPCServer. The Fiber
// and stdlib HTTP variants are mutually exclusive; either HTTP variant can be
// composed with gRPC. A second stdlib *http.Server on its own port goes through
// WithAdditionalStdlibHTTPServer (or WithAdditionalStdlibHTTPListener), which
// composes with every other slot and, at shutdown, drains concurrently with the
// main HTTP server under one shared shutdownTimeout budget; gRPC and the admin
// app follow with their own budgets. For stdlib servers, a zero
// ReadHeaderTimeout is upgraded to a safe default before launch so callers do
// not accidentally expose Slowloris-prone listeners. Every HTTP drain, Fiber
// and stdlib alike, is bounded by shutdownTimeout; past the deadline an active
// Fiber connection is abandoned, not closed, and can serve further keep-alive
// requests until the process exits, so shutdown hooks must tolerate a late
// request against closed resources. Shutdown hooks run after
// HTTP/gRPC drain and before telemetry/logger/license shutdown.
package server
