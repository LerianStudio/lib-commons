package http

import (
	"context"
	"errors"
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

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

	missingErr = normalizeIDValidationError(missingErr, ErrMissingContextID)
	invalidErr = normalizeIDValidationError(invalidErr, ErrInvalidContextID)

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

	missingErr = normalizeIDValidationError(missingErr, ErrMissingResourceID)
	invalidErr = normalizeIDValidationError(invalidErr, ErrInvalidResourceID)

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

func normalizeIDValidationError(err, fallback error) error {
	if err != nil {
		return err
	}

	return fallback
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
