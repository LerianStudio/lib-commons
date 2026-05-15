// Package server provides server lifecycle and graceful shutdown helpers.
//
// Use this package to coordinate signal handling, shutdown deadlines, and ordered
// resource cleanup for HTTP/gRPC service processes.
//
// ServerManager supports Fiber HTTP servers via WithHTTPServer, stdlib
// *http.Server instances via WithStdlibHTTPServer, and gRPC servers via
// WithGRPCServer. The Fiber and stdlib HTTP variants are mutually exclusive;
// either HTTP variant can be composed with gRPC. For stdlib servers, a zero
// ReadHeaderTimeout is upgraded to a safe default before launch so callers do
// not accidentally expose Slowloris-prone listeners.
package server
