package http

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// TenantExtractor extracts tenant ID string from a request context.
type TenantExtractor func(ctx context.Context) string

// IDLocation defines where a resource ID should be extracted from.
type IDLocation string

const (
	// IDLocationParam extracts the ID from a URL path parameter.
	IDLocationParam IDLocation = "param"
	// IDLocationQuery extracts the ID from a query string parameter.
	IDLocationQuery IDLocation = "query"
)

// ErrInvalidIDLocation indicates an unsupported ID source location.
var ErrInvalidIDLocation = errors.New("invalid id location")

// Sentinel errors for context ownership verification.
var (
	ErrMissingContextID    = errors.New("context ID is required")
	ErrInvalidContextID    = errors.New("context ID must be a valid UUID")
	ErrTenantIDNotFound    = errors.New("tenant ID not found in request context")
	ErrTenantExtractorNil  = errors.New("tenant extractor is not configured")
	ErrInvalidTenantID     = errors.New("invalid tenant ID format")
	ErrContextNotFound     = errors.New("context not found")
	ErrContextNotOwned     = errors.New("context does not belong to the requesting tenant")
	ErrContextAccessDenied = errors.New("access to context denied")
	ErrContextNotActive    = errors.New("context is not active")
	ErrContextLookupFailed = errors.New("context lookup failed")
)

// ErrVerifierNotConfigured indicates that no ownership verifier was provided.
var ErrVerifierNotConfigured = errors.New("ownership verifier is not configured")

// ErrLookupFailed indicates an ownership lookup failed unexpectedly.
var ErrLookupFailed = errors.New("resource lookup failed")

// Sentinel errors for exception ownership verification.
//
// Deprecated: Domain-specific errors should be defined in consuming services.
// Use RegisterResourceErrors to register custom resource error mappings instead.
var (
	ErrMissingExceptionID    = errors.New("exception ID is required")
	ErrInvalidExceptionID    = errors.New("exception ID must be a valid UUID")
	ErrExceptionNotFound     = errors.New("exception not found")
	ErrExceptionAccessDenied = errors.New("access to exception denied")
)

// Sentinel errors for dispute ownership verification.
//
// Deprecated: Domain-specific errors should be defined in consuming services.
// Use RegisterResourceErrors to register custom resource error mappings instead.
var (
	ErrMissingDisputeID    = errors.New("dispute ID is required")
	ErrInvalidDisputeID    = errors.New("dispute ID must be a valid UUID")
	ErrDisputeNotFound     = errors.New("dispute not found")
	ErrDisputeAccessDenied = errors.New("access to dispute denied")
)

// ResourceErrorMapping defines how a resource type's ownership errors should be classified.
// Register mappings via RegisterResourceErrors to extend classifyResourceOwnershipError
// without modifying this library.
type ResourceErrorMapping struct {
	// NotFoundErr is matched via errors.Is to detect "not found" responses from verifiers.
	NotFoundErr error
	// AccessDeniedErr is matched via errors.Is to detect "access denied" responses.
	AccessDeniedErr error
}

// resourceErrorRegistry holds registered resource-specific error mappings.
// Protected by registryMu for concurrent safety.
var (
	resourceErrorRegistry []ResourceErrorMapping
	registryMu            sync.RWMutex
)

func init() {
	// Register legacy exception/dispute errors for backward compatibility.
	resourceErrorRegistry = []ResourceErrorMapping{
		{NotFoundErr: ErrExceptionNotFound, AccessDeniedErr: ErrExceptionAccessDenied},
		{NotFoundErr: ErrDisputeNotFound, AccessDeniedErr: ErrDisputeAccessDenied},
	}
}

// RegisterResourceErrors adds a resource error mapping to the global registry.
// Safe for concurrent use. Call at service initialization to register domain-specific
// error pairs so that classifyResourceOwnershipError can recognize them.
//
// Example:
//
//	func init() {
//	    http.RegisterResourceErrors(http.ResourceErrorMapping{
//	        NotFoundErr:     ErrInvoiceNotFound,
//	        AccessDeniedErr: ErrInvoiceAccessDenied,
//	    })
//	}
func RegisterResourceErrors(mapping ResourceErrorMapping) {
	registryMu.Lock()
	defer registryMu.Unlock()

	// Detect duplicate registrations by comparing error sentinel pointers.
	for _, existing := range resourceErrorRegistry {
		if errors.Is(existing.NotFoundErr, mapping.NotFoundErr) && errors.Is(existing.AccessDeniedErr, mapping.AccessDeniedErr) {
			return // Already registered, skip duplicate
		}
	}

	resourceErrorRegistry = append(resourceErrorRegistry, mapping)
}

// TenantOwnershipVerifier validates ownership using tenant and resource IDs.
type TenantOwnershipVerifier func(ctx context.Context, tenantID, resourceID uuid.UUID) error

// ResourceOwnershipVerifier validates ownership using resource ID only.
type ResourceOwnershipVerifier func(ctx context.Context, resourceID uuid.UUID) error

// ParseAndVerifyTenantScopedID extracts and validates tenant + resource IDs.
func ParseAndVerifyTenantScopedID(
	fiberCtx *fiber.Ctx,
	idName string,
	location IDLocation,
	verifier TenantOwnershipVerifier,
	tenantExtractor TenantExtractor,
	missingErr error,
	invalidErr error,
	accessErr error,
) (uuid.UUID, uuid.UUID, error) {
	if fiberCtx == nil {
		return uuid.Nil, uuid.Nil, ErrContextNotFound
	}

	if verifier == nil {
		return uuid.Nil, uuid.Nil, ErrVerifierNotConfigured
	}

	resourceID, ctx, tenantID, err := parseTenantAndResourceID(
		fiberCtx,
		idName,
		location,
		tenantExtractor,
		missingErr,
		invalidErr,
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	if err := verifier(ctx, tenantID, resourceID); err != nil {
		return uuid.Nil, uuid.Nil, classifyOwnershipError(err, accessErr)
	}

	return resourceID, tenantID, nil
}

// ParseAndVerifyResourceScopedID extracts and validates tenant + resource IDs,
// then verifies resource ownership where tenant is implicit in the verifier.
func ParseAndVerifyResourceScopedID(
	fiberCtx *fiber.Ctx,
	idName string,
	location IDLocation,
	verifier ResourceOwnershipVerifier,
	tenantExtractor TenantExtractor,
	missingErr error,
	invalidErr error,
	accessErr error,
	verificationLabel string,
) (uuid.UUID, uuid.UUID, error) {
	if fiberCtx == nil {
		return uuid.Nil, uuid.Nil, ErrContextNotFound
	}

	if verifier == nil {
		return uuid.Nil, uuid.Nil, ErrVerifierNotConfigured
	}

	resourceID, ctx, tenantID, err := parseTenantAndResourceID(
		fiberCtx,
		idName,
		location,
		tenantExtractor,
		missingErr,
		invalidErr,
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	if err := verifier(ctx, resourceID); err != nil {
		return uuid.Nil, uuid.Nil, classifyResourceOwnershipError(verificationLabel, err, accessErr)
	}

	return resourceID, tenantID, nil
}

// parseTenantAndResourceID extracts and validates both tenant and resource UUIDs
// from the Fiber request context, returning them along with the Go context.
func parseTenantAndResourceID(
	fiberCtx *fiber.Ctx,
	idName string,
	location IDLocation,
	tenantExtractor TenantExtractor,
	missingErr error,
	invalidErr error,
) (uuid.UUID, context.Context, uuid.UUID, error) {
	ctx := fiberCtx.UserContext()

	if tenantExtractor == nil {
		return uuid.Nil, ctx, uuid.Nil, ErrTenantExtractorNil
	}

	resourceIDStr, err := getIDValue(fiberCtx, idName, location)
	if err != nil {
		return uuid.Nil, ctx, uuid.Nil, err
	}

	if resourceIDStr == "" {
		return uuid.Nil, ctx, uuid.Nil, missingErr
	}

	resourceID, err := uuid.Parse(resourceIDStr)
	if err != nil {
		return uuid.Nil, ctx, uuid.Nil, fmt.Errorf("%w: %s", invalidErr, resourceIDStr)
	}

	tenantIDStr := tenantExtractor(ctx)
	if tenantIDStr == "" {
		return uuid.Nil, ctx, uuid.Nil, ErrTenantIDNotFound
	}

	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		return uuid.Nil, ctx, uuid.Nil, fmt.Errorf("%w: %w", ErrInvalidTenantID, err)
	}

	return resourceID, ctx, tenantID, nil
}

// getIDValue retrieves the raw ID string from the Fiber context using the
// specified location (path parameter or query string).
func getIDValue(fiberCtx *fiber.Ctx, idName string, location IDLocation) (string, error) {
	if fiberCtx == nil {
		return "", ErrContextNotFound
	}

	switch location {
	case IDLocationParam:
		return fiberCtx.Params(idName), nil
	case IDLocationQuery:
		return fiberCtx.Query(idName), nil
	default:
		return "", ErrInvalidIDLocation
	}
}

// classifyOwnershipError maps a verifier error to the appropriate sentinel,
// substituting accessErr when a custom access-denied error is provided.
func classifyOwnershipError(err, accessErr error) error {
	switch {
	case errors.Is(err, ErrContextNotFound):
		return ErrContextNotFound
	case errors.Is(err, ErrContextNotOwned):
		if accessErr != nil {
			return accessErr
		}

		return ErrContextNotOwned
	case errors.Is(err, ErrContextNotActive):
		return ErrContextNotActive
	case errors.Is(err, ErrContextAccessDenied):
		if accessErr != nil {
			return accessErr
		}

		return ErrContextAccessDenied
	default:
		return fmt.Errorf("%w: %w", ErrContextLookupFailed, err)
	}
}

// classifyResourceOwnershipError maps a resource-scoped verifier error to the
// appropriate sentinel using the global resource error registry.
// This allows consuming services to register their own domain-specific error
// mappings without modifying the shared library.
func classifyResourceOwnershipError(label string, err, accessErr error) error {
	registryMu.RLock()

	registry := make([]ResourceErrorMapping, len(resourceErrorRegistry))
	copy(registry, resourceErrorRegistry)
	registryMu.RUnlock()

	for _, mapping := range registry {
		if mapping.NotFoundErr != nil && errors.Is(err, mapping.NotFoundErr) {
			return err
		}

		if mapping.AccessDeniedErr != nil && errors.Is(err, mapping.AccessDeniedErr) {
			if accessErr != nil {
				return accessErr
			}

			return err
		}
	}

	return fmt.Errorf("%s %w: %w", label, ErrLookupFailed, err)
}

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
