package http

import (
	"context"
	"errors"
	"sync"

	"github.com/gofiber/fiber/v2"
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
	ErrMissingResourceID   = errors.New("resource ID is required")
	ErrInvalidResourceID   = errors.New("resource ID must be a valid UUID")
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
			return
		}
	}

	resourceErrorRegistry = append(resourceErrorRegistry, mapping)
}

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
