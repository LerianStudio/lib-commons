// Package otelscope builds the tracer a package of this library emits spans
// with: the instrumentation scope FC-7 pins, and a provider that is resolved
// on every span rather than frozen at package initialisation.
package otelscope

import (
	"context"
	"reflect"
	"sync/atomic"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/buildid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// Tracer returns the tracer for a package of this library, scoped to the
// lib-commons module path as linked into the running binary plus the version
// of that module.
//
// The scope is resolved once, the TracerProvider on every span. A tracer taken
// at package initialisation therefore follows the provider a service installs
// later, and keeps following it when the service replaces it: the global
// delegation the OTel SDK does for an early tracer happens exactly once, so a
// tracer held across two otel.SetTracerProvider calls would still be writing
// into the first, possibly already shut down, provider.
//
// The tracer the provider hands back is cached against the provider's identity,
// so the per-span cost on request-rate paths is one atomic load and a
// comparison; the provider's own mutex is taken again only after a swap.
func Tracer(pkg string) *Scoped {
	name, version := buildid.Scope(pkg)

	return &Scoped{name: name, opts: []trace.TracerOption{trace.WithInstrumentationVersion(version)}}
}

// Scoped is the tracer Tracer returns.
type Scoped struct {
	embedded.Tracer

	name   string
	opts   []trace.TracerOption
	cached atomic.Pointer[resolved]
}

// resolved pairs a provider with the tracer it handed out for this scope.
type resolved struct {
	provider trace.TracerProvider
	tracer   trace.Tracer
}

// Start resolves the provider and hands the span straight back to the caller,
// who owns ending it.
//
//nolint:spancheck // pass-through: this is the tracer, not a span user.
func (s *Scoped) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return s.tracer().Start(ctx, name, opts...)
}

func (s *Scoped) tracer() trace.Tracer {
	p := otel.GetTracerProvider()

	if r := s.cached.Load(); r != nil && r.provider == p {
		return r.tracer
	}

	t := p.Tracer(s.name, s.opts...)

	// Comparing two interfaces whose dynamic type is not comparable panics, so
	// a provider of such a type is never cached: it is resolved on every span
	// instead. Every provider in the SDK is a pointer or an empty struct.
	if reflect.ValueOf(p).Comparable() {
		s.cached.Store(&resolved{provider: p, tracer: t})
	}

	return t
}
