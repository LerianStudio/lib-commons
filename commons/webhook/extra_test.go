//go:build unit

package webhook

import (
	"testing"

	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestWithLogger covers the WithLogger option.
func TestWithLogger_SetsLogger(t *testing.T) {
	t.Parallel()

	lister := &mockLister{endpoints: []Endpoint{}}
	d := NewDeliverer(lister)

	logger := libLog.NewNop()
	opt := WithLogger(logger)
	opt(d)
	assert.Equal(t, logger, d.logger)
}

func TestWithLogger_NilIgnored(t *testing.T) {
	t.Parallel()

	lister := &mockLister{endpoints: []Endpoint{}}
	d := NewDeliverer(lister)
	originalLogger := d.logger

	opt := WithLogger(nil)
	opt(d)
	// nil should be ignored - logger should remain unchanged
	assert.Equal(t, originalLogger, d.logger)
}

// TestWithTracer covers the WithTracer option.
func TestWithTracer_SetsTracer(t *testing.T) {
	t.Parallel()

	lister := &mockLister{endpoints: []Endpoint{}}
	d := NewDeliverer(lister)

	tracer := noop.NewTracerProvider().Tracer("test")
	opt := WithTracer(tracer)
	opt(d)
	assert.Equal(t, tracer, d.tracer)
}

func TestWithTracer_NilIgnored(t *testing.T) {
	t.Parallel()

	lister := &mockLister{endpoints: []Endpoint{}}
	d := NewDeliverer(lister)
	originalTracer := d.tracer

	opt := WithTracer(nil)
	opt(d)
	// nil should be ignored
	assert.Equal(t, originalTracer, d.tracer)
}
