package opentelemetry

import "go.opentelemetry.io/otel/trace"

// HandleSpanError mirrors the canonical lib-commons helper that records an
// error on the span and sets its status. Stubbed here so the crosscut
// analyzer's selector-match (and selectorFromPackage) lights up.
func HandleSpanError(span trace.Span, message string, err error) {
	_ = span
	_ = message
	_ = err
}
