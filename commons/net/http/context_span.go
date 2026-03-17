package http

import (
	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// isNilSpan reports whether span is nil, including typed-nil interface values
// where a concrete nil pointer is stored in a trace.Span interface.
// This prevents panics when calling methods on a typed-nil span.
func isNilSpan(span trace.Span) bool {
	return nilcheck.Interface(span)
}

// SetHandlerSpanAttributes adds tenant_id and context_id attributes to a trace span.
func SetHandlerSpanAttributes(span trace.Span, tenantID, contextID uuid.UUID) {
	if isNilSpan(span) {
		return
	}

	span.SetAttributes(attribute.String("tenant.id", tenantID.String()))

	if contextID != uuid.Nil {
		span.SetAttributes(attribute.String("context.id", contextID.String()))
	}
}

// SetTenantSpanAttribute adds tenant_id attribute to a trace span.
func SetTenantSpanAttribute(span trace.Span, tenantID uuid.UUID) {
	if isNilSpan(span) {
		return
	}

	span.SetAttributes(attribute.String("tenant.id", tenantID.String()))
}

// SetExceptionSpanAttributes adds tenant_id and exception_id attributes to a trace span.
func SetExceptionSpanAttributes(span trace.Span, tenantID, exceptionID uuid.UUID) {
	if isNilSpan(span) {
		return
	}

	span.SetAttributes(attribute.String("tenant.id", tenantID.String()))
	span.SetAttributes(attribute.String("exception.id", exceptionID.String()))
}

// SetDisputeSpanAttributes adds tenant_id and dispute_id attributes to a trace span.
func SetDisputeSpanAttributes(span trace.Span, tenantID, disputeID uuid.UUID) {
	if isNilSpan(span) {
		return
	}

	span.SetAttributes(attribute.String("tenant.id", tenantID.String()))
	span.SetAttributes(attribute.String("dispute.id", disputeID.String()))
}
